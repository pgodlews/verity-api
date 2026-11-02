package main

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func executeStream(ctx context.Context, conn *grpc.ClientConn, full string, md protoreflect.MethodDescriptor, messages []proto.Message, opts streamConfig, start time.Time) (*spi.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{
		ClientStreams: md.IsStreamingClient(),
		ServerStreams: md.IsStreamingServer(),
	}, full)
	if err != nil {
		return finishStream(err, nil, []byte("[]"), 0, false, start), nil
	}

	// One sender and one receiver are allowed concurrently by grpc-go. Drain
	// responses during sends so bidi flow control cannot deadlock large calls.
	// The unbuffered channel bounds prefetch to one extra inbound message.
	type received struct {
		message proto.Message
		err     error
	}
	replies := make(chan received)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		// RecvMsg owns the final server status, even when SendMsg returns EOF
		// because the server stopped reading before all requests were sent.
		_ = sendStreamMessages(ctx, stream, messages, opts.sendInterval)
	}()
	go func() {
		defer workers.Done()
		for {
			msg := dynamicpb.NewMessage(md.Output())
			err := stream.RecvMsg(msg)
			select {
			case replies <- received{message: msg, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	// Every exit cancels blocked sends/receives and joins both workers before
	// the connection is closed by Execute.
	defer func() {
		cancel()
		workers.Wait()
	}()

	var idle *time.Timer
	var idleC <-chan time.Time
	if opts.idleTimeout > 0 {
		idle = time.NewTimer(opts.idleTimeout)
		idleC = idle.C
		defer idle.Stop()
	}
	body := []byte{'['}
	count := 0
	truncated := false
	var callErr error
collect:
	for {
		select {
		case <-ctx.Done():
			callErr = status.FromContextError(ctx.Err()).Err()
			break collect
		case <-idleC:
			callErr = status.Error(codes.DeadlineExceeded, "grpc stream idle timeout exceeded")
			break collect
		case reply := <-replies:
			if reply.err != nil {
				callErr = reply.err
				break collect
			}
			b, err := marshalReply(reply.message)
			if err != nil {
				return nil, err
			}
			separator := 0
			if count > 0 {
				separator = 1
			}
			// Include commas and the closing bracket in the collection budget.
			if len(b) > opts.maxBytes-len(body)-separator-1 {
				return nil, verr.Newf(verr.CodeTransport, "grpc stream collected JSON exceeds stream.maxBytes (%d bytes)", opts.maxBytes)
			}
			if separator > 0 {
				body = append(body, ',')
			}
			body = append(body, b...)
			count++
			if count >= opts.maxMessages {
				truncated = true
				break collect
			}
			if idle != nil {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(opts.idleTimeout)
			}
		}
	}
	body = append(body, ']')
	cancel()
	workers.Wait()
	return finishStream(callErr, stream, body, count, truncated, start), nil
}

func sendStreamMessages(ctx context.Context, stream grpc.ClientStream, messages []proto.Message, interval time.Duration) error {
	for i, message := range messages {
		if i > 0 && interval > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stream.SendMsg(message); err != nil {
			return err
		}
	}
	return stream.CloseSend()
}

func finishStream(err error, stream grpc.ClientStream, body []byte, count int, truncated bool, start time.Time) *spi.Response {
	code, message := codes.OK, ""
	if err != nil && !errors.Is(err, io.EOF) {
		st := status.Convert(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			st = status.FromContextError(err)
		}
		code, message = st.Code(), st.Message()
	}
	var hdr, trl metadata.MD
	if stream != nil {
		hdr, _ = stream.Header()
		trl = stream.Trailer()
	}
	resp := mapStatus(code, message, hdr, trl, body, time.Since(start))
	// These synthetic response fields use headers so they survive VPP/1 and
	// are available to existing header assertions, captures and reporters.
	resp.Headers.Set("messageCount", strconv.Itoa(count))
	resp.Headers.Set("truncated", strconv.FormatBool(truncated))
	return resp
}
