package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestEncodeDecodeHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Add("X-Multi", "val1")
	h.Add("X-Multi", "val2")

	encoded := EncodeHeaders(h)
	if encoded["Content-Type"] != "application/json" {
		t.Fatalf("expected single string for Content-Type, got %v", encoded["Content-Type"])
	}
	multi, ok := encoded["X-Multi"].([]string)
	if !ok || len(multi) != 2 || multi[0] != "val1" || multi[1] != "val2" {
		t.Fatalf("expected []string with 2 items for X-Multi, got %v", encoded["X-Multi"])
	}

	// Test decoding
	decoded := http.Header{}
	DecodeHeaders(encoded, decoded)
	if decoded.Get("Content-Type") != "application/json" {
		t.Fatalf("decode Content-Type failed: %s", decoded.Get("Content-Type"))
	}
	vals := decoded.Values("X-Multi")
	if len(vals) != 2 || vals[0] != "val1" || vals[1] != "val2" {
		t.Fatalf("decode X-Multi failed: %v", vals)
	}
}

func TestRequestAndResponseHeaderRoundTrip(t *testing.T) {
	req := &spi.Request{
		Method:  "POST",
		URL:     "http://example.com/api",
		Headers: http.Header{},
		Cookies: map[string]string{"sid": "123"},
		Body:    []byte(`hello`),
	}
	req.Headers.Add("Accept", "text/plain")
	req.Headers.Add("X-List", "a")
	req.Headers.Add("X-List", "b")

	encReq := EncodeRequest(req)
	decReq, err := DecodeRequest(encReq)
	if err != nil {
		t.Fatalf("decodeRequest error: %v", err)
	}
	if decReq.Cookies["sid"] != "123" {
		t.Fatalf("expected cookie sid=123, got %v", decReq.Cookies)
	}
	if len(decReq.Headers.Values("X-List")) != 2 {
		t.Fatalf("expected 2 X-List values, got %v", decReq.Headers.Values("X-List"))
	}

	resp := &spi.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Headers:    http.Header{},
		Body:       []byte(`ok`),
		Duration:   50 * time.Millisecond,
	}
	resp.Headers.Add("Set-Cookie", "c1=v1")
	resp.Headers.Add("Set-Cookie", "c2=v2")

	encResp := EncodeResponse(resp)
	decResp, err := DecodeResponse(encResp)
	if err != nil {
		t.Fatalf("decodeResponse error: %v", err)
	}
	if len(decResp.Headers.Values("Set-Cookie")) != 2 {
		t.Fatalf("expected 2 Set-Cookie headers, got %v", decResp.Headers.Values("Set-Cookie"))
	}
	if decResp.Duration != 50*time.Millisecond {
		t.Fatalf("duration = %s, want 50ms", decResp.Duration)
	}
}

func TestProtocolProxyPassesDirAndDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "protocol_echo")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer c.Close()

	dir := t.TempDir()
	proxy := &protocolProxy{client: c, typ: "echo"}
	resp, err := proxy.Execute(context.Background(), &spi.ProtocolContext{
		Dir:                dir,
		AllowExternalFiles: true,
		Client:             &http.Client{Timeout: 2 * time.Second},
	}, &spi.Request{Headers: http.Header{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Duration != 15*time.Millisecond {
		t.Fatalf("duration %s", resp.Duration)
	}
	var echoed map[string]any
	if err := json.Unmarshal(resp.Body, &echoed); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if echoed["dir"] != dir || echoed["allowExternalFiles"] != true {
		t.Fatalf("echoed %+v", echoed)
	}
	if echoed["deadline"] == "" {
		t.Fatalf("missing deadline: %+v", echoed)
	}
}

func TestProtocolProxyPreservesConfigError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "protocol_config_error")
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer c.Close()
	proxy := &protocolProxy{client: c, typ: "echo"}
	_, err = proxy.Execute(context.Background(), &spi.ProtocolContext{}, &spi.Request{Headers: http.Header{}})
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeConfig || ve.Message != "bad descriptor" {
		t.Fatalf("got %v", err)
	}
	if ve.Details["file"] != "echo.pb" {
		t.Fatalf("details: %+v", ve.Details)
	}
}

func TestAuthProxyApplyAndConcurrentLayer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "auth_mock")
	if err != nil {
		t.Fatalf("Launch auth_mock failed: %v", err)
	}
	defer c.Close()

	proxy := &authProxy{
		client: c,
		typ:    "mock_auth",
		config: map[string]any{},
	}

	// Verify concurrent Layer() calls do not race
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := proxy.Layer()
			if l != spi.LayerMessage {
				t.Errorf("expected LayerMessage, got %v", l)
			}
		}()
	}
	wg.Wait()

	// Verify Apply propagates cookies and multi-value headers
	req := &spi.Request{
		Method:  "GET",
		URL:     "http://example.com",
		Headers: http.Header{},
	}
	if err := proxy.Apply(ctx, req); err != nil {
		t.Fatalf("proxy.Apply failed: %v", err)
	}
	if req.Cookies["session"] != "abc12345" {
		t.Fatalf("expected cookie session=abc12345, got %v", req.Cookies)
	}
	if len(req.Headers.Values("X-Added")) != 2 {
		t.Fatalf("expected 2 X-Added headers, got %v", req.Headers.Values("X-Added"))
	}
}
