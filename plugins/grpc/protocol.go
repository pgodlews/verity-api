package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Response mapping (unary):
//
//	statusCode   gRPC code number (0 = OK). status assertions compare this.
//	status       canonical code name (OK, NOT_FOUND, DEADLINE_EXCEEDED, …)
//	body         protojson of the reply (EmitUnpopulated false, UseProtoNames true)
//	headers      response headers, then trailers; Grpc-Status and Grpc-Message
//	             are always set from the status (decoded message, not the
//	             percent-encoded HTTP/2 trailer)
//
// A status returned by the server, including DEADLINE_EXCEEDED from the
// attempt deadline, is a response. Dial failures are transport_error.
// Descriptor and config problems are config_error. Metadata is never logged.
type grpcProtocol struct{}

func (grpcProtocol) Type() string { return "grpc" }

func (grpcProtocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	target, method, err := validateTargetMethod(raw)
	if err != nil {
		return nil, err
	}
	if err := validateTransport(raw); err != nil {
		return nil, err
	}
	if err := validateDescriptorPath(raw); err != nil {
		return nil, err
	}
	body, err := requestBody(raw)
	if err != nil {
		return nil, err
	}
	if _, err := parseStreamConfig(raw); err != nil {
		return nil, err
	}
	headers, err := metadataHeaders(raw["metadata"])
	if err != nil {
		return nil, err
	}
	return &spi.Request{
		Method:  method,
		URL:     target,
		Headers: headers,
		Query:   map[string]string{},
		Cookies: map[string]string{},
		Body:    body,
		Config:  raw,
	}, nil
}

func (p grpcProtocol) Execute(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return mapStatus(codes.DeadlineExceeded, "deadline exceeded", nil, nil, nil, time.Since(start)), nil
		}
		return nil, verr.Wrap(verr.CodeTransport, err, "grpc call")
	}
	if req == nil {
		return nil, verr.New(verr.CodeConfig, "grpc request is missing")
	}
	dir, allow := "", false
	if pctx != nil {
		dir = pctx.Dir
		allow = pctx.AllowExternalFiles
	}
	cfg := req.Config
	target := req.URL
	method := req.Method
	if target == "" || method == "" {
		var err error
		target, method, err = validateTargetMethod(cfg)
		if err != nil {
			return nil, err
		}
	}
	service, meth, err := splitMethod(method)
	if err != nil {
		return nil, err
	}

	var files *protoregistry.Files
	var md protoreflect.MethodDescriptor
	var messages []proto.Message
	if path := stringField(cfg, "descriptorSet"); path != "" {
		set, err := readProjectFile(dir, allow, path, "descriptorSet")
		if err != nil {
			return nil, err
		}
		files, err = cachedDescriptorSet(set)
		if err != nil {
			return nil, err
		}
		md, err = findMethod(files, service, meth)
		if err != nil {
			return nil, err
		}
		messages, err = decodeRequestMessages(md, req)
		if err != nil {
			return nil, err
		}
	}

	plaintext, tlsMap, err := transportConfig(cfg)
	if err != nil {
		return nil, err
	}
	creds, tlsKey, err := transportCredsKeyed(dir, allow, plaintext, tlsMap)
	if err != nil {
		return nil, err
	}
	pairs, err := outgoingMetadata(req.Headers)
	if err != nil {
		return nil, err
	}
	outMD := pairs.asMD()

	// The connection is cached for later steps (see cache.go), not closed.
	key := connKey(target, plaintext, tlsKey)
	conn, err := conns.get(ctx, key, target, creds)
	if err != nil {
		return nil, err
	}

	if md == nil {
		files, err = cachedReflect(ctx, key, conn, service, meth)
		if err != nil {
			return nil, err
		}
		md, err = findMethod(files, service, meth)
		if err != nil {
			return nil, err
		}
		messages, err = decodeRequestMessages(md, req)
		if err != nil {
			return nil, err
		}
	}

	callCtx := ctx
	if len(outMD) > 0 {
		callCtx = metadata.NewOutgoingContext(ctx, outMD)
	}
	full := "/" + service + "/" + meth
	if md.IsStreamingClient() || md.IsStreamingServer() {
		opts, err := parseStreamConfig(cfg)
		if err != nil {
			return nil, err
		}
		return executeStream(callCtx, conn, full, md, messages, opts, start)
	}
	reply := dynamicpb.NewMessage(md.Output())
	var hdr, trl metadata.MD
	invokeErr := conn.Invoke(callCtx, full, messages[0], reply, grpc.Header(&hdr), grpc.Trailer(&trl))
	dur := time.Since(start)
	return finishCall(invokeErr, reply, hdr, trl, dur)
}

func finishCall(invokeErr error, reply proto.Message, hdr, trl metadata.MD, dur time.Duration) (*spi.Response, error) {
	if invokeErr == nil {
		body, err := marshalReply(reply)
		if err != nil {
			return nil, err
		}
		return mapStatus(codes.OK, "", hdr, trl, body, dur), nil
	}
	if st, ok := status.FromError(invokeErr); ok {
		body, err := marshalReply(reply)
		if err != nil {
			body = []byte("{}")
		}
		return mapStatus(st.Code(), st.Message(), hdr, trl, body, dur), nil
	}
	if errors.Is(invokeErr, context.DeadlineExceeded) {
		return mapStatus(codes.DeadlineExceeded, "deadline exceeded", hdr, trl, nil, dur), nil
	}
	return nil, verr.Wrap(verr.CodeTransport, invokeErr, "grpc call")
}

var (
	marshalReplyOpts = protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}
	unmarshalOpts    = protojson.UnmarshalOptions{DiscardUnknown: false}
)

func marshalReply(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return []byte("{}"), nil
	}
	b, err := marshalReplyOpts.Marshal(msg)
	if err != nil {
		return nil, verr.New(verr.CodeInternal, "encode grpc reply: "+err.Error())
	}
	if len(b) == 0 {
		return []byte("{}"), nil
	}
	return b, nil
}

func decodeMessage(md protoreflect.MethodDescriptor, body []byte) (proto.Message, error) {
	msg := dynamicpb.NewMessage(md.Input())
	payload := bytesTrim(body)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if err := unmarshalOpts.Unmarshal(payload, msg); err != nil {
		return nil, verr.New(verr.CodeConfig, "decode grpc message: "+err.Error())
	}
	return msg, nil
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func mapStatus(code codes.Code, message string, hdr, trl metadata.MD, body []byte, dur time.Duration) *spi.Response {
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	h := http.Header{}
	appendMD(h, hdr)
	appendMD(h, trl)
	h.Set("Grpc-Status", strconv.Itoa(int(code)))
	h.Set("Grpc-Message", oneLine(message))
	return &spi.Response{
		StatusCode:  int(code),
		Status:      codeName(code),
		Headers:     h,
		Body:        body,
		Duration:    dur,
		ContentType: "application/json",
	}
}

func appendMD(h http.Header, md metadata.MD) {
	for k, vs := range md {
		if strings.HasSuffix(k, "-bin") {
			continue
		}
		for _, v := range vs {
			if strings.ContainsAny(v, "\r\n") {
				continue
			}
			h.Add(k, v)
		}
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

func codeName(c codes.Code) string {
	names := []string{
		"OK",
		"CANCELLED",
		"UNKNOWN",
		"INVALID_ARGUMENT",
		"DEADLINE_EXCEEDED",
		"NOT_FOUND",
		"ALREADY_EXISTS",
		"PERMISSION_DENIED",
		"RESOURCE_EXHAUSTED",
		"FAILED_PRECONDITION",
		"ABORTED",
		"OUT_OF_RANGE",
		"UNIMPLEMENTED",
		"INTERNAL",
		"UNAVAILABLE",
		"DATA_LOSS",
		"UNAUTHENTICATED",
	}
	if int(c) >= 0 && int(c) < len(names) {
		return names[c]
	}
	return c.String()
}
