package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func newReq() *spi.Request {
	return &spi.Request{Headers: http.Header{}, Query: map[string]string{}, Cookies: map[string]string{}}
}

func TestBasicProvider(t *testing.T) {
	red := secrets.NewRedactor()
	p, err := newBasic(map[string]any{"username": "user", "password": "p@ss-word-1234"}, red)
	if err != nil {
		t.Fatal(err)
	}
	req := newReq()
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got := req.Headers.Get("Authorization")
	if got != "Basic dXNlcjpwQHNzLXdvcmQtMTIzNA==" {
		t.Fatalf("unexpected basic header: %q", got)
	}
	// The password must be redacted.
	if red.Redact(req.Headers.Get("Authorization")) == got {
		t.Fatal("basic credential not redacted")
	}
}

func TestBearerProvider(t *testing.T) {
	red := secrets.NewRedactor()
	p, err := newBearer(map[string]any{"token": "tok-abcdef-123456"}, red)
	if err != nil {
		t.Fatal(err)
	}
	req := newReq()
	_ = p.Apply(context.Background(), req)
	if req.Headers.Get("Authorization") != "Bearer tok-abcdef-123456" {
		t.Fatalf("got %q", req.Headers.Get("Authorization"))
	}
	if red.Redact("Bearer tok-abcdef-123456") == "Bearer tok-abcdef-123456" {
		t.Fatal("bearer token not redacted")
	}
}

func TestAPIKeyPlacements(t *testing.T) {
	red := secrets.NewRedactor()
	cases := []struct {
		in     string
		verify func(*spi.Request) bool
	}{
		{"header", func(r *spi.Request) bool { return r.Headers.Get("X-Key") == "secret-key-123456" }},
		{"query", func(r *spi.Request) bool { return r.Query["X-Key"] == "secret-key-123456" }},
		{"cookie", func(r *spi.Request) bool { return r.Cookies["X-Key"] == "secret-key-123456" }},
	}
	for _, c := range cases {
		p, err := newAPIKey(map[string]any{"name": "X-Key", "value": "secret-key-123456", "in": c.in}, red)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		req := newReq()
		_ = p.Apply(context.Background(), req)
		if !c.verify(req) {
			t.Fatalf("placement %q not applied: %+v", c.in, req)
		}
	}
}

func TestAPIKeyInvalidPlacement(t *testing.T) {
	_, err := newAPIKey(map[string]any{"name": "k", "value": "v123456", "in": "body"}, secrets.NewRedactor())
	if err == nil {
		t.Fatal("expected error for invalid placement")
	}
}

func TestNoneProvider(t *testing.T) {
	p, _ := newNone(nil, secrets.NewRedactor())
	req := newReq()
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(req.Headers) != 0 {
		t.Fatal("none provider should not modify request")
	}
	if p.Layer() != spi.LayerMessage {
		t.Fatal("none should be a message-layer provider")
	}
}
