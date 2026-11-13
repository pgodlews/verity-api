package auth

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestDigestChallenges(t *testing.T) {
	for _, tc := range []struct {
		name, header, override string
		retry, fail            bool
	}{
		{name: "other scheme", header: `Basic realm="test"`},
		{name: "combined schemes", header: `Basic realm="test", Digest realm="test, realm", nonce="n", qop="auth-int, auth", algorithm=SHA-256`, retry: true},
		{name: "legacy default", header: `Digest realm="test", nonce="n"`, retry: true},
		{name: "empty opaque", header: `Digest realm="test", nonce="n", opaque=""`, retry: true},
		{name: "sess algorithm", header: `Digest realm="test", nonce="n", algorithm=SHA-256-sess`, retry: true},
		{name: "empty realm", header: `Digest realm="", nonce="n"`, retry: true},
		{name: "strongest", header: `Digest realm="test", nonce="n", algorithm=MD5, Digest realm="test", nonce="n", algorithm=SHA-256`, retry: true},
		{name: "auth-int", header: `Digest realm="test", nonce="n", qop="auth-int"`, fail: true},
		{name: "algorithm", header: `Digest realm="test", nonce="n", algorithm=unknown`, fail: true},
		{name: "missing nonce", header: `Digest realm="test"`, fail: true},
		{name: "override mismatch", header: `Digest realm="test", nonce="n", algorithm=MD5`, override: "SHA-256", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			red := secrets.NewRedactor()
			password := rand.Text()
			provider, err := newDigest(map[string]any{"username": "user", "password": password, "algorithm": tc.override}, red)
			if err != nil {
				t.Fatal(err)
			}
			// A URL without an origin is sufficient: these tests never send it.
			req, _ := http.NewRequest("GET", "/resource", nil)
			resp := &http.Response{StatusCode: 401, Header: http.Header{"Www-Authenticate": []string{tc.header}}}
			retry, err := provider.(spi.ChallengeResponder).RespondChallenge(context.Background(), req, resp)
			if retry != tc.retry || (err != nil) != tc.fail {
				t.Fatalf("retry=%t err=%v", retry, err)
			}
			if err != nil {
				if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeAuth {
					t.Fatalf("wrong error: %v", err)
				}
			}
			if red.Redact(password) != secrets.Mask {
				t.Fatal("password not registered")
			}
			if tc.name == "empty opaque" && !strings.Contains(req.Header.Get("Authorization"), `opaque=""`) {
				t.Fatal("empty opaque was not echoed")
			}
			if tc.name == "sess algorithm" && !strings.Contains(req.Header.Get("Authorization"), "algorithm=SHA-256-sess") {
				t.Fatal("session algorithm was not emitted in canonical form")
			}
			if tc.name == "strongest" && !strings.Contains(req.Header.Get("Authorization"), "algorithm=SHA-256") {
				t.Fatal("did not prefer SHA-256")
			}
		})
	}
}

func TestDigestValidationAndOrigin(t *testing.T) {
	for _, cfg := range []map[string]any{
		{}, {"username": "u"}, {"username": "u", "password": rand.Text(), "algorithm": "invalid"},
	} {
		if _, err := newDigest(cfg, secrets.NewRedactor()); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	provider, _ := newDigest(map[string]any{"username": "u", "password": rand.Text()}, secrets.NewRedactor())
	u := &url.URL{Scheme: "http", Host: rand.Text()}
	req := &http.Request{Method: "GET", URL: u, Header: http.Header{}}
	_, err := provider.(spi.ChallengeResponder).RespondChallenge(context.Background(), req, &http.Response{
		StatusCode: 401,
		Header:     http.Header{"Www-Authenticate": []string{`Digest realm="test", nonce="n", qop="auth"`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	other := &spi.Request{URL: "/resource"}
	if err := provider.Apply(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if other.Headers.Get("Authorization") != "" {
		t.Fatal("cached challenge leaked to another origin")
	}
}

func TestDigestNonceHistory(t *testing.T) {
	provider, _ := newDigest(map[string]any{"username": "user", "password": rand.Text()}, secrets.NewRedactor())
	responder := provider.(spi.ChallengeResponder)
	for i, nonce := range []string{"first", "second", "first"} {
		req, _ := http.NewRequest("GET", "/resource", nil)
		_, err := responder.RespondChallenge(context.Background(), req, &http.Response{
			StatusCode: 401,
			Header:     http.Header{"Www-Authenticate": []string{`Digest realm="test", nonce="` + nonce + `", qop="auth"`}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && !strings.Contains(req.Header.Get("Authorization"), "nc=00000002") {
			t.Fatal("an out-of-order challenge reset a previously used nonce count")
		}
	}
}

func TestDigestResponsesDoNotGrowRedactor(t *testing.T) {
	red := secrets.NewRedactor()
	provider, err := newDigest(map[string]any{"username": "user", "password": "digest-password"}, red)
	if err != nil {
		t.Fatal(err)
	}
	p := provider.(*digestProvider)
	challenge := &digestChallenge{realm: "realm", nonce: "nonce", algorithm: "MD5", qop: "auth"}
	var header, response string
	var done func()
	for i := 0; i < 100_000; i++ {
		var ctx context.Context
		ctx, _, done = red.AttemptScope(context.Background())
		p.mu.Lock()
		header, err = p.sign(ctx, challenge, http.MethodGet, "/resource")
		p.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		done()
	}
	const marker = `response="`
	start := strings.Index(header, marker)
	if start < 0 {
		t.Fatalf("missing digest response: %s", header)
	}
	start += len(marker)
	response = header[start : strings.Index(header[start:], `"`)+start]
	if response == "" {
		t.Fatal("empty digest response")
	}
	if got := red.LiteralCount(); got != 1 {
		t.Fatalf("redactor has %d literals after 100k signatures, want only the password", got)
	}
	ctx, scoped, done := red.AttemptScope(context.Background())
	defer done()
	red.AddTemporary(ctx, response)
	masked := scoped.Redact(header + ` {"echo":"` + response + `"}`)
	if strings.Contains(masked, response) || !strings.Contains(masked, secrets.Mask) {
		t.Fatalf("current digest response or isolated echo was not masked: %s", masked)
	}
	done()
	if got := scoped.Redact(response); got != response {
		t.Fatalf("discarded attempt hash remained registered: %q", got)
	}
}
