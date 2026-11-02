package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

// TestOAuth2ClientCredentialsCachingAndRefresh verifies that the provider:
//   - fetches a token once and reuses it (caching keyed to expiry),
//   - refreshes before expiry (pre-expiry refresh via the skew window).
func TestOAuth2ClientCredentialsCachingAndRefresh(t *testing.T) {
	var tokenCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "client_credentials" {
			w.WriteHeader(400)
			return
		}
		// Confirm Basic client auth was sent (default authStyle=header).
		if u, p, ok := r.BasicAuth(); !ok || u != "id" || p != "shh-very-secret-1234" {
			w.WriteHeader(401)
			return
		}
		n := atomic.AddInt32(&tokenCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-token-number-" + itoa(int(n)),
			"token_type":   "Bearer",
			"expires_in":   2, // short, so we can test pre-expiry refresh
		})
	}))
	defer srv.Close()

	red := secrets.NewRedactor()
	prov, err := newOAuth2ClientCredentials(map[string]any{
		"tokenUrl":           srv.URL,
		"clientId":           "id",
		"clientSecret":       "shh-very-secret-1234",
		"refreshSkewSeconds": "1",
	}, red)
	if err != nil {
		t.Fatal(err)
	}

	// First Apply fetches a token.
	req1 := newReq()
	if err := prov.Apply(context.Background(), req1); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected 1 token fetch, got %d", got)
	}
	if req1.Headers.Get("Authorization") != "Bearer access-token-number-1" {
		t.Fatalf("unexpected auth header: %q", req1.Headers.Get("Authorization"))
	}

	// Immediate second Apply should reuse the cached token (no new fetch).
	req2 := newReq()
	_ = prov.Apply(context.Background(), req2)
	if got := atomic.LoadInt32(&tokenCalls); got != 1 {
		t.Fatalf("expected cached token reuse, but fetched %d times", got)
	}

	// Wait until within the refresh skew window (expires_in=2s, skew=1s).
	time.Sleep(1200 * time.Millisecond)
	req3 := newReq()
	_ = prov.Apply(context.Background(), req3)
	if got := atomic.LoadInt32(&tokenCalls); got != 2 {
		t.Fatalf("expected pre-expiry refresh (2 fetches), got %d", got)
	}
	if req3.Headers.Get("Authorization") != "Bearer access-token-number-2" {
		t.Fatalf("expected refreshed token, got %q", req3.Headers.Get("Authorization"))
	}

	// The fetched tokens must be redacted.
	if red.Redact("access-token-number-1") == "access-token-number-1" {
		t.Fatal("fetched OAuth token was not registered for redaction")
	}
}

func TestOAuth2BodyAuthStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != "secret-value-1234" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "abc123456", "expires_in": 60})
	}))
	defer srv.Close()

	prov, err := newOAuth2ClientCredentials(map[string]any{
		"tokenUrl": srv.URL, "clientId": "id", "clientSecret": "secret-value-1234",
		"authStyle": "body",
	}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	req := newReq()
	if err := prov.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Headers.Get("Authorization") != "Bearer abc123456" {
		t.Fatalf("got %q", req.Headers.Get("Authorization"))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
