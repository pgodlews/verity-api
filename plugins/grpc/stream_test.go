package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
	"github.com/pgodlews/verity-api/plugins/grpc/internal/echo"
)

func TestStreamingKinds(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.pb"), echo.DescriptorSet(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			method  string
			payload map[string]any
			want    []string
		}{
			{"server", "ServerStream", map[string]any{"message": map[string]any{"message": "a"}}, []string{"a", "a", "a"}},
			{"server JSON string", "ServerStream", map[string]any{"message": `{"message":"a"}`}, []string{"a", "a", "a"}},
			{"empty server", "ServerStream", map[string]any{"message": map[string]any{"n": -1}}, []string{}},
			{"client", "ClientStream", map[string]any{"messages": []any{map[string]any{"message": "a"}, map[string]any{"message": "b"}}}, []string{"a,b"}},
			{"bidi", "Stream", map[string]any{"messages": []any{map[string]any{"message": "a"}, map[string]any{"message": "b"}}}, []string{"a", "b"}},
			{"empty client", "ClientStream", map[string]any{"messages": []any{}}, []string{""}},
			{"empty bidi", "Stream", map[string]any{"messages": []any{}}, []string{}},
		} {
			source := "reflection/"
			if descriptor {
				source = "descriptor/"
			}
			t.Run(source+tc.name, func(t *testing.T) {
				raw := streamRaw(target, tc.method, tc.payload)
				if descriptor {
					raw["descriptorSet"] = "echo.pb"
				}
				resp := executeStreaming(t, context.Background(), dir, raw)
				if resp.StatusCode != 0 || resp.Headers.Get("truncated") != "false" {
					t.Fatalf("response: %+v", resp)
				}
				docs := streamDocuments(t, resp)
				got := make([]string, 0, len(docs))
				for i, doc := range docs {
					message, _ := doc["message"].(string)
					got = append(got, message)
					if tc.method == "ServerStream" && doc["n"] != float64(i+1) {
						t.Fatalf("out of order: %s", resp.Body)
					}
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("messages %v, want %v", got, tc.want)
				}
				if resp.Headers.Get("x-echo") != "ok" || resp.Headers.Get("x-trailer") != "yes" {
					t.Fatalf("metadata: %v", resp.Headers)
				}
			})
		}
	}
}

func TestStreamLimitsAndStatus(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	for _, tc := range []struct {
		name      string
		method    string
		payload   map[string]any
		code      int
		count     int
		truncated string
	}{
		{"truncate server", "ServerStream", map[string]any{"stream": map[string]any{"maxMessages": 2}}, 0, 2, "true"},
		{"default cap", "ServerStream", map[string]any{"message": map[string]any{"n": 1001}}, 0, 1000, "true"},
		{"truncate bidi", "Stream", map[string]any{
			"messages": []any{map[string]any{"message": "a"}, map[string]any{"message": "b"}},
			"stream":   map[string]any{"maxMessages": 1, "sendIntervalMs": 10000},
		}, 0, 1, "true"},
		{"server error", "ServerStream", map[string]any{"message": map[string]any{"message": "fail"}}, 5, 2, "false"},
		{"client error before sends finish", "ClientStream", map[string]any{
			"messages": []any{map[string]any{"message": "fail"}, map[string]any{"message": "unread"}},
			"stream":   map[string]any{"sendIntervalMs": 10000},
		}, 5, 0, "false"},
		{"bidi error before sends finish", "Stream", map[string]any{
			"messages": []any{map[string]any{"message": "fail"}, map[string]any{"message": "unread"}},
			"stream":   map[string]any{"sendIntervalMs": 10000},
		}, 5, 1, "false"},
		{"idle before first response", "ClientStream", map[string]any{
			"messages": []any{map[string]any{}, map[string]any{}},
			"stream":   map[string]any{"idleTimeoutMs": 100, "sendIntervalMs": 10000},
		}, 4, 0, "false"},
		{"idle with partial body", "ServerStream", map[string]any{
			"message": map[string]any{"message": "wait"},
			"stream":  map[string]any{"idleTimeoutMs": 100},
		}, 4, 1, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			resp := executeStreaming(t, context.Background(), "", streamRaw(target, tc.method, tc.payload))
			if time.Since(start) > 3*time.Second {
				t.Fatal("stream did not stop promptly")
			}
			if resp.StatusCode != tc.code || resp.Headers.Get("truncated") != tc.truncated || len(streamDocuments(t, resp)) != tc.count {
				t.Fatalf("status %d, headers %v, body %s", resp.StatusCode, resp.Headers, resp.Body)
			}
			if tc.code == 5 && resp.Headers.Get("Grpc-Message") != "stream stopped" {
				t.Fatalf("server error lost: %v", resp.Headers)
			}
			if tc.code == 4 && !strings.Contains(resp.Headers.Get("Grpc-Message"), "idle timeout") {
				t.Fatalf("idle error lost: %v", resp.Headers)
			}
		})
	}
}

func TestStreamByteLimit(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	for _, tc := range []struct {
		limit int
		fail  bool
	}{
		{10, true}, // One {"n":1} fits; the second message exceeds the total.
		{100, false},
	} {
		raw := streamRaw(target, "ServerStream", map[string]any{
			"stream": map[string]any{"maxBytes": tc.limit},
		})
		req, err := (grpcProtocol{}).BuildRequest(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := (grpcProtocol{}).Execute(ctx, &spi.ProtocolContext{}, req)
		cancel()
		if tc.fail {
			ve, ok := verr.As(err)
			if !ok || ve.Code != verr.CodeTransport || !strings.Contains(err.Error(), "stream.maxBytes") {
				t.Fatalf("limit error: %v", err)
			}
		} else if err != nil || len(streamDocuments(t, resp)) != 3 {
			t.Fatalf("below limit: %v", err)
		}
	}
}

func TestStreamConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
	}{
		{"both messages", map[string]any{"message": map[string]any{}, "messages": []any{}}},
		{"null messages", map[string]any{"messages": nil}},
		{"object messages", map[string]any{"messages": map[string]any{}}},
		{"scalar message", map[string]any{"messages": []any{1}}},
		{"null message", map[string]any{"messages": []any{nil}}},
		{"string message", map[string]any{"messages": []any{`{}`}}},
		{"bad stream", map[string]any{"stream": nil}},
		{"zero cap", map[string]any{"stream": map[string]any{"maxMessages": 0}}},
		{"negative cap", map[string]any{"stream": map[string]any{"maxMessages": -1}}},
		{"small byte cap", map[string]any{"stream": map[string]any{"maxBytes": 1}}},
		{"fraction", map[string]any{"stream": map[string]any{"idleTimeoutMs": 0.5}}},
		{"negative interval", map[string]any{"stream": map[string]any{"sendIntervalMs": -1}}},
		{"string cap", map[string]any{"stream": map[string]any{"maxMessages": "2"}}},
		{"duration overflow", map[string]any{"stream": map[string]any{"idleTimeoutMs": int64(1 << 62)}}},
		{"unknown", map[string]any{"stream": map[string]any{"kind": "bidi"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"target": "${GRPC_TARGET}", "method": echo.Service + "/Stream"}
			for k, v := range tc.raw {
				raw[k] = v
			}
			_, err := (grpcProtocol{}).BuildRequest(context.Background(), raw)
			if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeConfig {
				t.Fatalf("want config_error, got %v", err)
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.pb"), echo.DescriptorSet(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method  string
		payload map[string]any
	}{
		{"Echo", map[string]any{"messages": []any{}}},
		{"ServerStream", map[string]any{"messages": []any{}}},
		{"ClientStream", map[string]any{"message": map[string]any{}}},
		{"Stream", map[string]any{"message": map[string]any{}}},
		{"Stream", map[string]any{"messages": []any{map[string]any{"unknown": 1}}}},
	} {
		raw := streamRaw("${GRPC_TARGET}", tc.method, tc.payload)
		raw["descriptorSet"] = "echo.pb"
		req, err := (grpcProtocol{}).BuildRequest(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		_, err = (grpcProtocol{}).Execute(context.Background(), &spi.ProtocolContext{Dir: dir}, req)
		if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeConfig {
			t.Fatalf("%s: want config_error before dialing, got %v", tc.method, err)
		}
	}
}

// Exercise cancellation after the server sends its first response. Connection
// setup is outside the deadline; the server must observe cancellation promptly.
func TestStreamDeadlineAndCancel(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			files, err := filesFromDescriptorSet(echo.DescriptorSet())
			if err != nil {
				t.Fatal(err)
			}
			md, err := findMethod(files, echo.Service, "ServerStream")
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			stopped := make(chan struct{})
			lis := bufconn.Listen(1 << 20)
			srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
				in := dynamicpb.NewMessage(md.Input())
				if err := stream.RecvMsg(in); err != nil {
					return err
				}
				if err := stream.SendMsg(dynamicpb.NewMessage(md.Output())); err != nil {
					return err
				}
				close(started)
				<-stream.Context().Done()
				close(stopped)
				return status.FromContextError(stream.Context().Err()).Err()
			}))
			go func() { _ = srv.Serve(lis) }()
			t.Cleanup(func() {
				srv.Stop()
				_ = lis.Close()
			})
			creds, err := transportCreds("", false, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := grpc.NewClient("passthrough:///stream-test", grpc.WithTransportCredentials(creds),
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			readyCtx, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer readyCancel()
			if err := waitReady(readyCtx, conn); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
			}
			defer cancel()
			done := make(chan *spi.Response, 1)
			go func() {
				resp, err := executeStream(ctx, conn, "/"+echo.Service+"/ServerStream", md,
					[]proto.Message{dynamicpb.NewMessage(md.Input())}, streamConfig{maxMessages: 1000, maxBytes: 1 << 20}, time.Now())
				if err != nil {
					done <- nil
					return
				}
				done <- resp
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("server did not start")
			}
			if !deadline {
				cancel()
			}
			select {
			case resp := <-done:
				code := int(codes.Canceled)
				if deadline {
					code = int(codes.DeadlineExceeded)
				}
				if resp == nil || resp.StatusCode != code {
					t.Fatalf("cancel response: %+v", resp)
				}
				if docs := streamDocuments(t, resp); (deadline && len(docs) != 1) || len(docs) > 1 {
					t.Fatalf("partial body: %s", resp.Body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stream did not stop")
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("server did not observe cancellation")
			}
		})
	}
}

func streamRaw(target, method string, payload map[string]any) map[string]any {
	raw := map[string]any{"target": target, "method": echo.Service + "/" + method, "plaintext": true}
	for k, v := range payload {
		raw[k] = v
	}
	return raw
}

func executeStreaming(t *testing.T, ctx context.Context, dir string, raw map[string]any) *spi.Response {
	t.Helper()
	req, err := (grpcProtocol{}).BuildRequest(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := (grpcProtocol{}).Execute(ctx, &spi.ProtocolContext{Dir: dir}, req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func streamDocuments(t *testing.T, resp *spi.Response) []map[string]any {
	t.Helper()
	var docs []map[string]any
	if resp == nil || json.Unmarshal(resp.Body, &docs) != nil || docs == nil {
		t.Fatalf("stream body is not an array: %+v", resp)
	}
	if resp.Headers.Get("messageCount") != strconv.Itoa(len(docs)) {
		t.Fatalf("messageCount differs from body: %v %s", resp.Headers, resp.Body)
	}
	return docs
}

func TestBidiFlowControlAndOrdering(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	messages := make([]any, 64)
	for i := range messages {
		messages[i] = map[string]any{"message": strings.Repeat("x", 64<<10), "n": i + 1}
	}
	resp := executeStreaming(t, context.Background(), "", streamRaw(target, "Stream", map[string]any{"messages": messages}))
	docs := streamDocuments(t, resp)
	if resp.StatusCode != 0 || len(docs) != len(messages) {
		t.Fatalf("status %d, message count %d", resp.StatusCode, len(docs))
	}
	for i, doc := range docs {
		if doc["n"] != float64(i+1) {
			t.Fatalf("message %d out of order", i)
		}
	}
}

func TestStreamSendInterval(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	start := time.Now()
	resp := executeStreaming(t, context.Background(), "", streamRaw(target, "ClientStream", map[string]any{
		"messages": []any{map[string]any{}, map[string]any{}, map[string]any{}},
		"stream":   map[string]any{"sendIntervalMs": 50},
	}))
	if resp.StatusCode != 0 || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("send pacing: status %d, elapsed %s", resp.StatusCode, time.Since(start))
	}
}

func TestStreamConfigDefaults(t *testing.T) {
	opts, err := parseStreamConfig(nil)
	if err != nil || opts.maxMessages != 1000 || opts.maxBytes != 16<<20 || opts.idleTimeout != 0 || opts.sendInterval != 0 {
		t.Fatalf("defaults: %+v, %v", opts, err)
	}
	opts, err = parseStreamConfig(map[string]any{"stream": map[string]any{
		"maxMessages": float64(2), "maxBytes": json.Number("100"), "idleTimeoutMs": 0, "sendIntervalMs": 1,
	}})
	if err != nil || opts.maxMessages != 2 || opts.maxBytes != 100 || opts.idleTimeout != 0 || opts.sendInterval != time.Millisecond {
		t.Fatalf("numeric options: %+v, %v", opts, err)
	}
}
