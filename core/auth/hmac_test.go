package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestHMACDefaultSignsBody(t *testing.T) {
	const secret = "hmac-secret-value-0123456789"
	red := secrets.NewRedactor()
	p, err := newHMAC(map[string]any{"secret": secret}, red)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("signed-body")
	req := &spi.Request{Method: "POST", URL: "https://example.test/hooks", Headers: http.Header{}, Body: body}
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if req.Headers.Get("X-Signature") != want {
		t.Fatalf("got %s", req.Headers.Get("X-Signature"))
	}
	if red.Redact(secret) == secret || red.Redact(want) == want {
		t.Fatal("secret or signature was not redacted")
	}
}

func TestHMACTemplateAndEncoding(t *testing.T) {
	const secret = "hmac-secret-value-0123456789"
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name   string
		config map[string]any
		hash   func() hash.Hash
		enc    string
	}{
		{"sha256 hex prefix", map[string]any{"algorithm": "sha256", "encoding": "hex", "prefix": "sha256="}, sha256.New, "hex"},
		{"sha1", map[string]any{"algorithm": "SHA1"}, sha1.New, "hex"},
		{"sha512 base64", map[string]any{"algorithm": "sha512", "encoding": "base64"}, sha512.New, "base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{
				"secret": secret, "header": "X-Partner",
				"timestampHeader": "X-Timestamp", "nonceHeader": "X-Nonce",
				"message": "${method}\n${path}\n${query}\n${timestamp}\n${nonce}\n${body}",
			}
			for k, v := range tc.config {
				cfg[k] = v
			}
			p, err := newHMAC(cfg, secrets.NewRedactor())
			if err != nil {
				t.Fatal(err)
			}
			prov := p.(*hmacProvider)
			prov.now = func() time.Time { return when }
			prov.nonce = func() (string, error) { return "fixed-nonce", nil }
			req := &spi.Request{
				Method: "POST", URL: "https://example.test/hooks?b=2&a=1",
				Headers: http.Header{}, Query: map[string]string{"c": "3"}, Body: []byte("body"),
			}
			if err := prov.Apply(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if req.Headers.Get("X-Timestamp") != "1577934245" || req.Headers.Get("X-Nonce") != "fixed-nonce" {
				t.Fatalf("headers %v", req.Headers)
			}
			msg := "POST\n/hooks\na=1&b=2&c=3\n1577934245\nfixed-nonce\nbody"
			mac := hmac.New(tc.hash, []byte(secret))
			mac.Write([]byte(msg))
			sum := mac.Sum(nil)
			var want string
			if tc.enc == "base64" {
				want = base64.StdEncoding.EncodeToString(sum)
			} else {
				want = hex.EncodeToString(sum)
			}
			if prefix, _ := tc.config["prefix"].(string); prefix != "" {
				want = prefix + want
			}
			if req.Headers.Get("X-Partner") != want {
				t.Fatalf("got %s want %s", req.Headers.Get("X-Partner"), want)
			}
		})
	}
}

func TestHMACResignsOnRetry(t *testing.T) {
	p, err := newHMAC(map[string]any{
		"secret": "hmac-secret-value-0123456789", "timestampHeader": "X-Timestamp", "nonceHeader": "X-Nonce",
		"message": "${timestamp}.${nonce}.${body}",
	}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	prov := p.(*hmacProvider)
	n := 0
	prov.now = func() time.Time {
		n++
		return time.Unix(1_700_000_000, 0).UTC().Add(time.Duration(n) * time.Second)
	}
	nonces := []string{"n1", "n2"}
	i := 0
	prov.nonce = func() (string, error) {
		v := nonces[i]
		i++
		return v, nil
	}
	first := &spi.Request{Method: "POST", URL: "https://example.test/h", Headers: http.Header{}, Body: []byte("a")}
	second := &spi.Request{Method: "POST", URL: first.URL, Headers: http.Header{}, Body: first.Body}
	if err := prov.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := prov.Apply(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if first.Headers.Get("X-Signature") == second.Headers.Get("X-Signature") || first.Headers.Get("X-Nonce") == second.Headers.Get("X-Nonce") || first.Headers.Get("X-Timestamp") == second.Headers.Get("X-Timestamp") {
		t.Fatalf("first %v second %v", first.Headers, second.Headers)
	}
}

func TestHMACConfigErrors(t *testing.T) {
	red := secrets.NewRedactor()
	if _, err := newHMAC(map[string]any{}, red); err == nil {
		t.Fatal("missing secret")
	}
	if _, err := newHMAC(map[string]any{"secret": "x", "algorithm": "md5"}, red); err == nil {
		t.Fatal("bad algorithm")
	}
	if _, err := newHMAC(map[string]any{"secret": "x", "encoding": "raw"}, red); err == nil {
		t.Fatal("bad encoding")
	}
	if _, err := newHMAC(map[string]any{"secret": "x", "header": "Bad Name"}, red); err == nil {
		t.Fatal("bad header")
	}
	p, err := newHMAC(map[string]any{"secret": "hmac-secret-value-0123456789", "message": "${path}"}, red)
	if err != nil {
		t.Fatal(err)
	}
	err = p.Apply(context.Background(), &spi.Request{URL: "://", Headers: http.Header{}})
	if err == nil || !strings.Contains(err.Error(), "auth_error") {
		t.Fatalf("got %v", err)
	}
}

func TestHMACSignsTheEscapedWireQuery(t *testing.T) {
	const secret = "hmac-secret-value-0123456789"
	p, err := newHMAC(map[string]any{"secret": secret, "message": "${method}\n${query}"}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	req := &spi.Request{Method: "GET", URL: "http://host/p?a=x y&b=ü", Headers: http.Header{}}
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("GET\na=x%20y&b=%C3%BC"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got := req.Headers.Get("X-Signature"); got != want {
		t.Fatalf("got %s, want signature over escaped query %s", got, want)
	}
}
