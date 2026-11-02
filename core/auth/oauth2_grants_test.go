package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestOAuth2Grants(t *testing.T) {
	for _, typ := range []string{"oauth2", "oauth2_client_credentials"} {
		for _, grant := range []string{"", "client_credentials", "password", "refresh_token"} {
			for _, style := range []string{"header", "body"} {
				t.Run(typ+"/"+grant+"/"+style, func(t *testing.T) {
					clientSecret, password, refresh, access := rand.Text(), rand.Text()+" &+=", rand.Text()+" &+=", rand.Text()
					wantGrant := grant
					if wantGrant == "" {
						wantGrant = "client_credentials"
					}
					want := url.Values{"grant_type": {wantGrant}, "scope": {"read write"}}
					switch grant {
					case "password":
						want.Set("username", "user &+=")
						want.Set("password", password)
					case "refresh_token":
						want.Set("refresh_token", refresh)
					}
					if style == "body" {
						want.Set("client_id", "client")
						want.Set("client_secret", clientSecret)
					}
					var calls atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
							t.Error("expected POST with form content type")
						}
						if err := r.ParseForm(); err != nil || !reflect.DeepEqual(r.PostForm, want) {
							t.Error("incorrect token request form")
						}
						if style == "header" {
							u, p, ok := r.BasicAuth()
							if !ok || u != "client" || p != clientSecret {
								t.Error("incorrect Basic credentials")
							}
						} else if r.Header.Get("Authorization") != "" {
							t.Error("body auth must not send Basic credentials")
						}
						_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, ExpiresIn: 3600})
					}))
					defer srv.Close()
					reg := registry.New()
					Register(reg)
					factory, err := reg.AuthFactory(typ)
					if err != nil {
						t.Fatal(err)
					}
					red := secrets.NewRedactor()
					prov, err := factory(map[string]any{
						"tokenUrl": srv.URL, "clientId": "client", "clientSecret": clientSecret,
						"grant": grant, "username": "user &+=", "password": password,
						"refreshToken": refresh, "scope": "read write", "authStyle": style,
					}, red)
					if err != nil {
						t.Fatal(err)
					}
					if prov.Type() != typ || prov.Layer() != spi.LayerMessage {
						t.Fatal("incorrect provider identity or layer")
					}
					for range 3 {
						req := &spi.Request{}
						if err := prov.Apply(context.Background(), req); err != nil {
							t.Fatal(err)
						}
						if req.Headers.Get("Authorization") != "Bearer "+access {
							t.Fatal("incorrect access token")
						}
					}
					if calls.Load() != 1 {
						t.Fatalf("expected one cached fetch, got %d", calls.Load())
					}
					for _, secret := range []string{clientSecret, password, refresh, access, base64.StdEncoding.EncodeToString([]byte("client:" + clientSecret))} {
						if red.Redact(secret) != secrets.Mask {
							t.Error("secret was not registered for redaction")
						}
					}
				})
			}
		}
	}
}

func TestOAuth2RefreshRotation(t *testing.T) {
	for _, grant := range []string{"client_credentials", "password", "refresh_token"} {
		t.Run(grant, func(t *testing.T) {
			initial, rotated, access := rand.Text(), rand.Text(), rand.Text()
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				_ = r.ParseForm()
				wantGrant, wantRefresh := "refresh_token", rotated
				if n == 1 {
					wantGrant, wantRefresh = grant, ""
					if grant == "refresh_token" {
						wantRefresh = initial
					}
				}
				if r.PostForm.Get("grant_type") != wantGrant || r.PostForm.Get("refresh_token") != wantRefresh {
					t.Error("incorrect grant or rotated refresh token")
				}
				if n > 1 && (r.PostForm.Has("username") || r.PostForm.Has("password")) {
					t.Error("refresh request included user credentials")
				}
				response := tokenResponse{AccessToken: access, ExpiresIn: 3600}
				if n == 1 {
					response.RefreshToken = rotated
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			cfg := map[string]any{
				"tokenUrl": srv.URL, "clientId": "client", "clientSecret": rand.Text(),
				"grant": grant, "username": "user", "password": rand.Text(), "refreshToken": initial,
			}
			red := secrets.NewRedactor()
			prov, err := newOAuth2(cfg, red)
			if err != nil {
				t.Fatal(err)
			}
			p := prov.(*oauth2Provider)
			for range 3 {
				if err := p.Apply(context.Background(), newReq()); err != nil {
					t.Fatal(err)
				}
				// Enter the default skew window without sleeping.
				p.cached.expiresAt = time.Now().Add(15 * time.Second)
			}
			if calls.Load() != 3 {
				t.Fatalf("expected three fetches, got %d", calls.Load())
			}
			if cfg["refreshToken"] != initial {
				t.Fatal("rotation mutated the project config")
			}
			if red.Redact(rotated) != secrets.Mask || red.Redact(initial) != secrets.Mask {
				t.Fatal("refresh tokens were not redacted")
			}
		})
	}
}

func TestOAuth2ConfiguredRefreshTokenIsNotSentUntilIssued(t *testing.T) {
	for _, grant := range []string{"client_credentials", "password"} {
		t.Run(grant, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if r.PostForm.Get("grant_type") != grant || r.PostForm.Get("refresh_token") != "" {
					t.Error("configured refresh token was sent before the endpoint issued one")
				}
				if grant == "password" && r.PostForm.Get("password") == "" {
					t.Error("password grant omitted the password")
				}
				_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: rand.Text(), ExpiresIn: 3600})
			}))
			defer srv.Close()
			prov, err := newOAuth2(map[string]any{
				"tokenUrl": srv.URL, "clientId": "client", "clientSecret": rand.Text(),
				"grant": grant, "username": "user", "password": rand.Text(),
				"refreshToken": rand.Text(),
			}, secrets.NewRedactor())
			if err != nil {
				t.Fatal(err)
			}
			p := prov.(*oauth2Provider)
			for range 2 {
				if err := p.Apply(context.Background(), newReq()); err != nil {
					t.Fatal(err)
				}
				// Expire the cache without sleeping through the skew window.
				p.cached.expiresAt = time.Now()
			}
			if calls.Load() != 2 {
				t.Fatalf("expected two fetches, got %d", calls.Load())
			}
		})
	}
}

func TestOAuth2RequiredFields(t *testing.T) {
	for _, grant := range []string{"client_credentials", "password", "refresh_token"} {
		fields := []string{"tokenUrl", "clientId", "clientSecret"}
		if grant == "password" {
			fields = append(fields, "username", "password")
		}
		if grant == "refresh_token" {
			fields = append(fields, "refreshToken")
		}
		for _, field := range fields {
			t.Run(grant+"/"+field, func(t *testing.T) {
				cfg := map[string]any{"grant": grant}
				for _, key := range fields {
					cfg[key] = rand.Text()
				}
				delete(cfg, field)
				_, err := newOAuth2(cfg, secrets.NewRedactor())
				ve, ok := verr.As(err)
				if !ok || ve.Code != verr.CodeConfig || !strings.Contains(ve.Message, grant) || !strings.Contains(ve.Message, "'"+field+"'") {
					t.Fatalf("expected config error naming grant and missing field: %v", err)
				}
			})
		}
	}
	for _, field := range []string{"grant", "authStyle"} {
		t.Run("invalid/"+field, func(t *testing.T) {
			cfg := map[string]any{"tokenUrl": "${TOKEN_URL}", "clientId": "client", "clientSecret": rand.Text()}
			cfg[field] = rand.Text()
			_, err := newOAuth2(cfg, secrets.NewRedactor())
			ve, ok := verr.As(err)
			if !ok || ve.Code != verr.CodeConfig || !strings.Contains(ve.Message, field) {
				t.Fatalf("expected config error for %s: %v", field, err)
			}
		})
	}
}

func TestOAuth2TokenErrors(t *testing.T) {
	for _, failure := range []string{"status", "invalid_json", "missing_access_token"} {
		t.Run(failure, func(t *testing.T) {
			password, refresh := rand.Text(), rand.Text()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch failure {
				case "status":
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(password + " " + refresh))
				case "invalid_json":
					_, _ = w.Write([]byte("{"))
				case "missing_access_token":
					_ = json.NewEncoder(w).Encode(tokenResponse{RefreshToken: rand.Text()})
				}
			}))
			defer srv.Close()
			prov, err := newOAuth2(map[string]any{
				"tokenUrl": srv.URL, "clientId": "client", "clientSecret": rand.Text(),
				"grant": "password", "username": "user", "password": password, "refreshToken": refresh,
			}, secrets.NewRedactor())
			if err != nil {
				t.Fatal(err)
			}
			err = prov.Apply(context.Background(), newReq())
			ve, ok := verr.As(err)
			if !ok || ve.Code != verr.CodeAuth {
				t.Fatalf("expected auth error, got %v", err)
			}
			if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), refresh) {
				t.Fatal("credentials leaked into token endpoint error")
			}
		})
	}
}
