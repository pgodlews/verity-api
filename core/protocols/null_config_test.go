package protocols

import (
	"context"
	"strings"
	"testing"
)

func TestNullRequestFieldsAreNotNilText(t *testing.T) {
	req, err := RESTProtocol{}.BuildRequest(context.Background(), map[string]any{
		"method":  "GET",
		"url":     "http://127.0.0.1/x",
		"headers": map[string]any{"X-Null": nil, "X-Num": 3, "X-Str": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Headers["X-Null"]; ok {
		t.Fatalf("null header sent: %q", req.Headers.Get("X-Null"))
	}
	if req.Headers.Get("X-Num") != "3" || req.Headers.Get("X-Str") != "v" {
		t.Fatalf("headers = %v", req.Headers)
	}

	body, err := buildEnvelope(map[string]any{"operation": "Op", "namespace": "urn:x", "parameters": map[string]any{"a": nil}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "<nil>") || !strings.Contains(body, "<a></a>") {
		t.Fatalf("envelope = %s", body)
	}
	if got := stringField(map[string]any{"url": nil}, "url"); got != "" {
		t.Fatalf("stringField(nil) = %q", got)
	}
}
