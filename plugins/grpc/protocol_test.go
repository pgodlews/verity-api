package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
	"github.com/pgodlews/verity-api/plugins/grpc/internal/echo"
)

func TestBuildRequest(t *testing.T) {
	p := grpcProtocol{}
	ok, err := p.BuildRequest(context.Background(), map[string]any{
		"target":    "unix:///sock",
		"method":    "/verity.testpb.Echo/Echo",
		"plaintext": true,
		"message":   map[string]any{"message": "hello", "n": float64(2)},
		"metadata":  map[string]any{"x-token": "abc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok.URL != "unix:///sock" || ok.Method != "verity.testpb.Echo/Echo" {
		t.Fatalf("request: %+v", ok)
	}
	if ok.Headers.Get("x-token") != "abc" {
		t.Fatalf("metadata not copied to headers: %v", ok.Headers)
	}
	var body map[string]any
	if err := json.Unmarshal(ok.Body, &body); err != nil || body["message"] != "hello" {
		t.Fatalf("body %s: %v", ok.Body, err)
	}

	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing target", map[string]any{"method": "pkg.Svc/M"}, "target"},
		{"bad method", map[string]any{"target": "t", "method": "Echo"}, "method"},
		{"message array", map[string]any{"target": "t", "method": "pkg.Svc/M", "message": []any{1}}, "message"},
		{"message string", map[string]any{"target": "t", "method": "pkg.Svc/M", "message": "[]"}, "message"},
		{"plaintext and tls", map[string]any{
			"target": "t", "method": "pkg.Svc/M", "plaintext": true,
			"tls": map[string]any{"insecure": true},
		}, "plaintext"},
		{"bad metadata", map[string]any{
			"target": "t", "method": "pkg.Svc/M",
			"metadata": map[string]any{"Bad Key": "x"},
		}, "metadata"},
		{"binary metadata", map[string]any{
			"target": "t", "method": "pkg.Svc/M",
			"metadata": map[string]any{"payload-bin": "aa"},
		}, "binary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.BuildRequest(context.Background(), tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
			if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeConfig {
				t.Fatalf("code: %v", err)
			}
		})
	}
}

func TestEchoReflection(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	resp := callEcho(t, context.Background(), target, "", map[string]any{
		"message": "hello", "n": float64(2),
	}, nil)
	assertEcho(t, resp, "hello", float64(2))
	if resp.Headers.Get("x-echo") != "ok" || resp.Headers.Get("x-trailer") != "yes" {
		t.Fatalf("headers: %v", resp.Headers)
	}
	if resp.Headers.Get("Grpc-Status") != "0" || resp.Status != "OK" || resp.StatusCode != 0 {
		t.Fatalf("status mapping: %d %s %v", resp.StatusCode, resp.Status, resp.Headers)
	}
}

func TestEchoDescriptorSet(t *testing.T) {
	target := startEcho(t, echo.ReflectionOff)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo.pb"), echo.DescriptorSet(), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := callEcho(t, context.Background(), target, dir, map[string]any{
		"message": "file", "n": float64(7),
	}, map[string]any{"descriptorSet": "echo.pb"})
	assertEcho(t, resp, "file", float64(7))
}

func TestEchoV1AlphaReflection(t *testing.T) {
	target := startEcho(t, echo.ReflectionV1Alpha)
	resp := callEcho(t, context.Background(), target, "", map[string]any{"message": "alpha"}, nil)
	assertEcho(t, resp, "alpha", nil)
}

func TestMetadataAndAuthHeader(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	const token = "metadata-token-value-xyz"
	resp := callEcho(t, context.Background(), target, "", map[string]any{"message": "m"}, map[string]any{
		"metadata": map[string]any{"x-token": token},
	})
	var doc map[string]any
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["seen_metadata"] != token {
		t.Fatalf("metadata not echoed: %s", resp.Body)
	}

	p := grpcProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"target": target, "method": echo.MethodEcho, "plaintext": true,
		"message": map[string]any{"message": "auth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req.Headers.Set("Authorization", "Bearer "+token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := p.Execute(ctx, &spi.ProtocolContext{}, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["seen_metadata"] != "Bearer "+token {
		t.Fatalf("auth header not sent as metadata: %s", out.Body)
	}
}

func TestServerStatusIsResponse(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	p := grpcProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"target": target, "method": echo.MethodFail, "plaintext": true,
		"message": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := p.Execute(ctx, &spi.ProtocolContext{}, req)
	if err != nil {
		t.Fatalf("server status must be a response, got %v", err)
	}
	if resp.StatusCode != 5 || resp.Status != "NOT_FOUND" {
		t.Fatalf("status: %d %s", resp.StatusCode, resp.Status)
	}
	if resp.Headers.Get("Grpc-Status") != "5" || resp.Headers.Get("Grpc-Message") != "missing widget" {
		t.Fatalf("headers: %v", resp.Headers)
	}
	if resp.Headers.Get("x-from-server") != "fail" {
		t.Fatalf("response header missing: %v", resp.Headers)
	}
	if string(resp.Body) != "{}" {
		t.Fatalf("body: %s", resp.Body)
	}
}

func TestDeadlineIsStatus(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	p := grpcProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"target": target, "method": echo.MethodWait, "plaintext": true,
		"message": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	resp, err := p.Execute(ctx, &spi.ProtocolContext{}, req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("deadline must be a response, got %v", err)
	}
	if resp.StatusCode != 4 || resp.Status != "DEADLINE_EXCEEDED" {
		t.Fatalf("status: %d %s body %s", resp.StatusCode, resp.Status, resp.Body)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("deadline took %s", elapsed)
	}
}

func TestDialError(t *testing.T) {
	dir, err := os.MkdirTemp("", "vgrpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	target := "unix://" + filepath.Join(dir, "missing.sock")
	p := grpcProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"target": target, "method": echo.MethodEcho, "plaintext": true,
		"message": map[string]any{"message": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = p.Execute(ctx, &spi.ProtocolContext{}, req)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("dial took %s", elapsed)
	}
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeTransport {
		t.Fatalf("want transport_error, got %v", err)
	}
}

func TestDescriptorRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "echo.pb"), echo.DescriptorSet(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.pb"), []byte("not a descriptor"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(dir, filepath.Join(outside, "echo.pb"))
	if err != nil {
		t.Fatal(err)
	}
	p := grpcProtocol{}
	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"missing", map[string]any{"descriptorSet": "missing.pb"}, "not found"},
		{"invalid", map[string]any{"descriptorSet": "bad.pb"}, "descriptor"},
		{"outside", map[string]any{"descriptorSet": rel}, "outside"},
		{"unknown method", map[string]any{"descriptorSet": "echo.pb"}, "not found"},
	}
	if err := os.WriteFile(filepath.Join(dir, "echo.pb"), echo.DescriptorSet(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := echo.MethodEcho
			if tc.name == "unknown method" {
				method = "verity.testpb.Echo/Missing"
			}
			raw := map[string]any{
				"target": "unix:///does/not/matter", "method": method, "plaintext": true,
				"message": map[string]any{},
			}
			for k, v := range tc.raw {
				raw[k] = v
			}
			req, err := p.BuildRequest(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = p.Execute(ctx, &spi.ProtocolContext{Dir: dir}, req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
			if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeConfig {
				t.Fatalf("code: %v", err)
			}
		})
	}
}

func TestReflectionRequired(t *testing.T) {
	target := startEcho(t, echo.ReflectionOff)
	p := grpcProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"target": target, "method": echo.MethodEcho, "plaintext": true,
		"message": map[string]any{"message": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = p.Execute(ctx, &spi.ProtocolContext{}, req)
	if err == nil || !strings.Contains(err.Error(), "reflection") {
		t.Fatalf("got %v", err)
	}
	if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeConfig {
		t.Fatalf("code: %v", err)
	}
}

func TestTransportCredsFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := transportCreds(dir, false, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := transportCreds(dir, false, false, map[string]any{"insecure": true, "serverName": "svc"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := transportCreds(dir, false, false, map[string]any{"caFile": "ca.pem"})
	if err == nil || !strings.Contains(err.Error(), "caFile") {
		t.Fatalf("got %v", err)
	}
	outside := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(dir, outside)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transportCreds(dir, false, false, map[string]any{"caFile": rel})
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("got %v", err)
	}
}

func TestJSONStringMessage(t *testing.T) {
	target := startEcho(t, echo.ReflectionBoth)
	resp := callEcho(t, context.Background(), target, "", `{"message":"from-string","n":3}`, nil)
	assertEcho(t, resp, "from-string", float64(3))
}

func startEcho(t *testing.T, mode echo.Reflection) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vgrpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := echo.NewServer(mode)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	return "unix://" + sock
}

func callEcho(t *testing.T, _ context.Context, target, dir string, message any, extra map[string]any) *spi.Response {
	t.Helper()
	raw := map[string]any{
		"target": target, "method": echo.MethodEcho, "plaintext": true, "message": message,
	}
	for k, v := range extra {
		raw[k] = v
	}
	p := grpcProtocol{}
	req, err := p.BuildRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := p.Execute(ctx, &spi.ProtocolContext{Dir: dir}, req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func assertEcho(t *testing.T, resp *spi.Response, message string, n any) {
	t.Helper()
	if resp.StatusCode != 0 {
		t.Fatalf("status %d %s body %s", resp.StatusCode, resp.Status, resp.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if doc["message"] != message {
		t.Fatalf("message: %s", resp.Body)
	}
	if n != nil && doc["n"] != n {
		t.Fatalf("n: %v", doc["n"])
	}
	if bytes := resp.Body; strings.Contains(string(bytes), "seenMetadata") {
		t.Fatalf("response used JSON names: %s", resp.Body)
	}
}
