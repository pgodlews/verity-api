package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestPKCE(t *testing.T) {
	proof, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(proof)
	if string(encoded) != "{}" {
		t.Fatal("PKCE proof is serializable")
	}
	other, _ := NewPKCE()
	sum := sha256.Sum256([]byte(proof.Verifier))
	if len(proof.Verifier) != 43 || len(proof.State) != 43 || proof.Verifier == other.Verifier || proof.State == other.State || proof.Challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("invalid or reused PKCE values")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	target, err := AuthorizeURL(map[string]any{
		"authUrl": srv.URL + "?prompt=consent", "clientId": "client", "scopes": []any{"read", "write"},
	}, "/callback", proof)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	q := u.Query()
	for key, want := range map[string]string{
		"client_id": "client", "response_type": "code", "code_challenge_method": "S256",
		"code_challenge": proof.Challenge, "state": proof.State, "redirect_uri": "/callback",
		"scope": "read write", "prompt": "consent",
	} {
		if q.Get(key) != want {
			t.Errorf("incorrect authorize parameter %s", key)
		}
	}
	if strings.Contains(target, proof.Verifier) {
		t.Fatal("verifier in authorization URL")
	}
	if _, err := AuthorizeURL(map[string]any{}, "", proof); err == nil {
		t.Fatal("missing authorization endpoint accepted")
	}
}

func TestLoopback(t *testing.T) {
	for _, mode := range []string{"code", "denied", "cancelled", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			state, issued := rand.Text(), rand.Text()
			flow, err := NewLoopback(0, state)
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			callback := func(q url.Values) int {
				resp, err := http.Get(flow.RedirectURL + "?" + q.Encode())
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if strings.Contains(string(body), state) || strings.Contains(string(body), issued) {
					t.Error("callback page echoed credentials")
				}
				return resp.StatusCode
			}
			if status := callback(url.Values{"state": {"wrong"}, "code": {issued}}); status != 400 {
				t.Fatal("bad state accepted")
			}
			if status := callback(url.Values{"state": {state}, "code": {}}); status != 400 {
				t.Fatal("missing code accepted")
			}
			if status := callback(url.Values{"state": {state, state}, "code": {issued}}); status != 400 {
				t.Fatal("duplicate state accepted")
			}
			timeout := time.Second
			if mode == "timeout" {
				timeout = time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			switch mode {
			case "cancelled":
				cancel()
			case "code":
				if status := callback(url.Values{"state": {state}, "code": {issued}}); status != 200 {
					t.Fatal("code rejected")
				}
			case "denied":
				if status := callback(url.Values{"state": {state}, "error": {"access_denied"}, "error_description": {rand.Text()}}); status != 200 {
					t.Fatal("denial rejected")
				}
			}
			code, err := flow.Wait(ctx)
			if mode == "code" {
				if err != nil || code != issued {
					t.Fatalf("callback failed: %v", err)
				}
				if status := callback(url.Values{"state": {state}, "code": {issued}}); status != 409 {
					t.Fatal("callback replay accepted")
				}
			} else if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeAuth {
				t.Fatalf("expected auth error: %v", err)
			}
		})
	}
	for _, port := range []int{-1, 65536} {
		if _, err := NewLoopback(port, "state"); err == nil {
			t.Fatal("invalid port accepted")
		}
	}
}

func TestPKCELoginExchangeAndSessionRefresh(t *testing.T) {
	for _, style := range []string{"public", "header", "body"} {
		t.Run(style, func(t *testing.T) {
			access, refresh, rotated, secret, code := rand.Text(), rand.Text(), rand.Text(), rand.Text(), rand.Text()
			var calls atomic.Int32
			var proofMu sync.Mutex
			var challenge, redirect string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				n := calls.Add(1)
				if r.PostForm.Get("client_id") != "client" && style != "header" {
					t.Error("missing client id")
				}
				if style == "public" && (r.Header.Get("Authorization") != "" || r.PostForm.Get("client_secret") != "") {
					t.Error("public client sent credentials")
				}
				if style == "header" {
					id, password, ok := r.BasicAuth()
					if !ok || id != "client" || password != secret {
						t.Error("incorrect client credentials")
					}
				}
				if style == "body" && r.PostForm.Get("client_secret") != secret {
					t.Error("missing body credentials")
				}
				if n == 1 {
					verifier := r.PostForm.Get("code_verifier")
					sum := sha256.Sum256([]byte(verifier))
					proofMu.Lock()
					valid := r.PostForm.Get("redirect_uri") == redirect && base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
					proofMu.Unlock()
					if !valid || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != code {
						t.Error("invalid code exchange")
					}
					_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, RefreshToken: refresh, ExpiresIn: 1})
				} else {
					if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != refresh {
						t.Error("invalid session refresh")
					}
					if r.PostForm.Get("scope") != "read write" {
						t.Error("scope missing on refresh")
					}
					_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, RefreshToken: rotated, ExpiresIn: 3600})
				}
			}))
			defer srv.Close()
			cfg := map[string]any{
				"grant": "authorization_code", "authUrl": srv.URL, "tokenUrl": srv.URL,
				"clientId": "client", "scopes": []string{"read", "write"},
			}
			if style != "public" {
				cfg["clientSecret"] = secret
				cfg["authStyle"] = style
			}
			loginRed := secrets.NewRedactor()
			session, err := Login(context.Background(), cfg, loginRed, func(target string) error {
				u, _ := url.Parse(target)
				q := u.Query()
				proofMu.Lock()
				challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
				proofMu.Unlock()
				resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "code": {code}}.Encode())
				if err != nil {
					return err
				}
				resp.Body.Close()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Only one parallel caller refreshes the short-lived login token;
			// later callers use it with their own run redactor.
			var wg sync.WaitGroup
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					red := secrets.NewRedactor()
					req := &spi.Request{}
					if err := session.Provider(red).Apply(context.Background(), req); err != nil {
						t.Error(err)
						return
					}
					if req.Headers.Get("Authorization") != "Bearer "+access {
						t.Error("missing session bearer")
					}
					for _, value := range []string{access, rotated} {
						if red.Redact(value) != secrets.Mask {
							t.Error("session secret not registered with run redactor")
						}
					}
				}()
			}
			wg.Wait()
			if calls.Load() != 2 {
				t.Fatalf("token calls=%d, want 2", calls.Load())
			}
			for _, value := range []string{access, refresh, code} {
				if loginRed.Redact(value) != secrets.Mask {
					t.Error("login secret not redacted")
				}
			}
		})
	}
}

func TestAuthorizationCodeHeadless(t *testing.T) {
	refresh, access := rand.Text(), rand.Text()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != refresh {
			t.Error("wrong headless grant")
		}
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, ExpiresIn: 3600})
	}))
	defer srv.Close()
	for _, token := range []string{"", "${env:NOT_SET}", refresh} {
		cfg := map[string]any{
			"grant": "authorization_code", "authUrl": srv.URL, "tokenUrl": srv.URL,
			"clientId": "client", "refreshToken": token,
		}
		provider, err := newOAuth2(cfg, secrets.NewRedactor())
		if err != nil {
			t.Fatal(err)
		}
		req := newReq()
		err = provider.Apply(context.Background(), req)
		if token == refresh {
			if err != nil || req.Headers.Get("Authorization") != "Bearer "+access {
				t.Fatalf("headless refresh: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "requires refreshToken for CLI/CI") {
			t.Fatalf("missing actionable error: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("missing refresh token attempted network login")
	}
}

func TestPKCELoginFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("failed login reached token endpoint")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	for _, mode := range []string{"cancelled", "denied", "no_browser", "invalid_port", "invalid_endpoint"} {
		t.Run(mode, func(t *testing.T) {
			cfg := map[string]any{"authUrl": srv.URL, "tokenUrl": srv.URL, "clientId": "client"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if mode == "invalid_port" {
				cfg["redirectPort"] = "not-an-integer"
			}
			if mode == "invalid_endpoint" {
				cfg["authUrl"] = "/authorize"
			}
			open := func(target string) error {
				if mode != "denied" {
					t.Error("invalid login opened browser")
					return nil
				}
				u, _ := url.Parse(target)
				q := u.Query()
				resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "error": {"access_denied"}}.Encode())
				if err != nil {
					return err
				}
				resp.Body.Close()
				return nil
			}
			if mode == "no_browser" {
				open = nil
			}
			if _, err := Login(ctx, cfg, secrets.NewRedactor(), open); err == nil {
				t.Fatal("invalid login succeeded")
			}
		})
	}
}
