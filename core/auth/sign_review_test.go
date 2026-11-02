package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// An HMAC key is bytes: leading and trailing spaces are part of it.
func TestHMACKeepsSecretWhitespace(t *testing.T) {
	const secret = " key with spaces "
	p, err := newHMAC(map[string]any{"secret": secret}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	req := &spi.Request{Method: "POST", URL: "https://example.test/h", Headers: http.Header{}, Body: []byte("b")}
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("b"))
	if req.Headers.Get("X-Signature") != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("signature was made with a trimmed key")
	}
}

func TestHMACHeaderNamesAreTokens(t *testing.T) {
	for name, ok := range map[string]bool{"X-Signature": true, "x_sig.v1~": true, "Bad/Name": false, "Bad Name": false, "a:b": false, "é": false} {
		_, err := newHMAC(map[string]any{"secret": "s", "header": name}, secrets.NewRedactor())
		if (err == nil) != ok {
			t.Fatalf("%q: err=%v, want ok=%v", name, err, ok)
		}
	}
}

// Raw queries are read the way AWS reads them: '+' and ';' are literal, and
// %HH is decoded once before the canonical encoding.
func TestSigV4CanonicalQueryKeepsPlusAndSemicolon(t *testing.T) {
	for raw, want := range map[string]string{
		"q=a+b":         "q=a%2Bb",
		"q=a;b":         "q=a%3Bb",
		"q=a%20b&p=%2F": "p=%2F&q=a%20b",
		"flag&z=1":      "flag=&z=1",
	} {
		got, err := canonicalQuery(raw, nil)
		if err != nil || got != want {
			t.Fatalf("%q: got %q err %v, want %q", raw, got, err, want)
		}
	}
	// With auth query parameters the sender re-encodes the whole query, and
	// the signer must sign that wire form.
	wire := spi.MergeQuery("b=x y&a=1", map[string]string{"key": "v w"})
	if wire != "a=1&b=x%20y&key=v%20w" {
		t.Fatalf("wire %q", wire)
	}
	got, _ := canonicalQuery("b=x y&a=1", map[string]string{"key": "v w"})
	if got != "a=1&b=x%20y&key=v%20w" {
		t.Fatalf("canonical %q", got)
	}
}

// Headers net/http sets from the request's own fields are not signed, so an
// explicit Transfer-Encoding or an empty User-Agent cannot break the signature.
func TestSigV4SkipsTransportOwnedHeaders(t *testing.T) {
	h := http.Header{
		"Content-Length":    {"999"},
		"Transfer-Encoding": {"chunked"},
		"Connection":        {"close"},
		"User-Agent":        {""},
		"X-Amz-Date":        {"20150830T123600Z"},
		"Host":              {"example.amazonaws.com"},
	}
	_, signed := canonicalHeaders(h)
	if signed != "host;x-amz-date" {
		t.Fatalf("signed headers %q", signed)
	}
}

func TestSignersNormalizeRawQueryBeforeParsing(t *testing.T) {
	hmacProvider, err := newHMAC(map[string]any{"secret": "s", "message": "${query}"}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	sigProvider, err := newSigV4(map[string]any{
		"accessKeyId": "access", "secretAccessKey": "secret", "region": "region", "service": "service",
	}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	digestProvider, err := newDigest(map[string]any{"username": "u", "password": "p"}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	for name, provider := range map[string]spi.AuthProvider{"hmac": hmacProvider, "sigv4": sigProvider, "digest": digestProvider} {
		t.Run(name, func(t *testing.T) {
			req := &spi.Request{URL: "http://host/p?q=a\tb", Query: map[string]string{"x": "y"}, Headers: http.Header{}}
			if err := provider.Apply(context.Background(), req); err != nil {
				t.Fatalf("raw control query was rejected before normalization: %v", err)
			}
		})
	}
}

func TestChainSignsAndGuardsRedirects(t *testing.T) {
	sig, err := newHMAC(map[string]any{"secret": "s"}, secrets.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	signed := &Chain{Message: []spi.AuthProvider{sig}}
	if !signed.Signs() || (&Chain{}).Signs() {
		t.Fatal("Signs")
	}
	client := &http.Client{}
	GuardRedirects(client, true, nil)
	if client.CheckRedirect(&http.Request{}, []*http.Request{{}}) != http.ErrUseLastResponse {
		t.Fatal("a signed request must not follow redirects")
	}
}
