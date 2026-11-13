package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// Per-request signatures are registered per attempt, so the run-wide
// redactor does not grow with every request, yet each signature is masked.
func TestSignaturesDoNotGrowRedactor(t *testing.T) {
	red := secrets.NewRedactor()
	p, err := newHMAC(map[string]any{"secret": "hmac-key-for-growth-test", "message": "${method} ${path} ${nonce}"}, red)
	if err != nil {
		t.Fatal(err)
	}
	base := red.LiteralCount()
	for i := 0; i < 500; i++ {
		ctx, scoped, done := red.AttemptScope(context.Background())
		req := &spi.Request{Method: "GET", URL: "http://127.0.0.1/r?i=" + strconv.Itoa(i), Headers: http.Header{}, Query: map[string]string{}}
		if err := p.Apply(ctx, req); err != nil {
			t.Fatal(err)
		}
		sig := req.Headers.Get("X-Signature")
		if sig == "" || strings.Contains(scoped.Redact("echo "+sig), sig) {
			t.Fatalf("signature not masked within its attempt: %q", sig)
		}
		done()
	}
	if got := red.LiteralCount(); got != base {
		t.Fatalf("redactor grew from %d to %d literals over 500 signatures", base, got)
	}

	// Without an attempt scope the signature is still registered.
	req := &spi.Request{Method: "GET", URL: "http://127.0.0.1/r", Headers: http.Header{}, Query: map[string]string{}}
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if sig := req.Headers.Get("X-Signature"); strings.Contains(red.Redact(sig), sig) {
		t.Fatal("signature outside an attempt was not registered")
	}
}

func TestDigestNonceHistoryIsBounded(t *testing.T) {
	provider, _ := newDigest(map[string]any{"username": "u", "password": "digest-password-1"}, secrets.NewRedactor())
	responder := provider.(spi.ChallengeResponder)
	for i := 0; i < maxDigestNonces+50; i++ {
		req, _ := http.NewRequest("GET", "/resource", nil)
		if _, err := responder.RespondChallenge(context.Background(), req, &http.Response{
			StatusCode: 401,
			Header:     http.Header{"Www-Authenticate": []string{`Digest realm="r", nonce="n` + strconv.Itoa(i) + `", qop="auth"`}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	d := provider.(*digestProvider)
	if len(d.nonces) != maxDigestNonces || len(d.nonceOrder) != maxDigestNonces {
		t.Fatalf("nonce history %d/%d, want %d", len(d.nonces), len(d.nonceOrder), maxDigestNonces)
	}
}
