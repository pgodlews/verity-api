package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestWorkspaceOAuthSession(t *testing.T) {
	access, refresh, rotated := rand.Text(), rand.Text(), rand.Text()
	var exchanges, refreshes, apiCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") == "authorization_code" {
				exchanges.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 1})
			} else {
				refreshes.Add(1)
				if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != refresh {
					t.Error("wrong refresh grant")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": rotated, "expires_in": 3600})
			}
			return
		}
		apiCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+access {
			t.Error("session access token missing")
			w.WriteHeader(401)
		}
		// Every new run's redactor must know these unlabelled token values.
		_, _ = w.Write([]byte(access + " " + rotated))
	}))
	defer srv.Close()
	project := &model.Project{
		SchemaVersion: "1.0", Name: "session",
		Environments: []model.Environment{
			{Name: "first", Default: true, Variables: map[string]string{"ENDPOINT": srv.URL}},
			{Name: "other", Variables: map[string]string{"ENDPOINT": srv.URL}},
		},
		Auth: []model.AuthDef{{Name: "login", Type: "oauth2", Config: map[string]any{
			"grant": "authorization_code", "authUrl": "${ENDPOINT}/authorize", "tokenUrl": "${ENDPOINT}/token",
			"clientId": "client", "refreshToken": "${env:VERITY_TEST_UNUSED_REFRESH}",
		}}},
		Suites: []model.Suite{{Name: "suite", Auth: &model.AuthRef{Use: []string{"login"}}, Cases: []model.TestCase{
			{Name: "case", Steps: []model.Step{{
				Name: "request", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/resource"},
				Assertions: []model.Assertion{{Type: "status", Value: 200}},
			}}},
		}}},
	}
	t.Setenv("VERITY_TEST_UNUSED_REFRESH", "")
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	req := OAuthRequest{Path: path, AuthName: "login"}
	var sessions OAuthSessions
	if signed, err := sessions.OAuthStatus(req); err != nil || signed {
		t.Fatalf("initial status=%t err=%v", signed, err)
	}
	err = sessions.OAuthLogin(context.Background(), req, func(target string) error {
		u, _ := url.Parse(target)
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" {
			t.Error("PKCE not enabled")
		}
		resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "code": {rand.Text()}}.Encode())
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if signed, err := sessions.OAuthStatus(req); err != nil || !signed {
		t.Fatalf("login status=%t err=%v", signed, err)
	}
	explicit := req
	explicit.Environment = "first"
	if signed, _ := sessions.OAuthStatus(explicit); !signed {
		t.Fatal("default environment key differs from explicit selection")
	}
	other := req
	other.Environment = "other"
	if signed, _ := sessions.OAuthStatus(other); signed {
		t.Fatal("session shared across environments")
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := sessions.Run(context.Background(), RunOptions{Path: path})
			if err != nil {
				t.Error(err)
				return
			}
			if result.Summary.Status != spi.StatusPassed {
				t.Error("session run failed")
			}
			out, _ := json.Marshal(result)
			for _, secret := range []string{access, refresh, rotated} {
				if strings.Contains(string(out), secret) {
					t.Error("session token leaked into result")
				}
			}
		}()
	}
	wg.Wait()
	if exchanges.Load() != 1 || refreshes.Load() != 1 || apiCalls.Load() != 6 {
		t.Fatalf("exchanges=%d refreshes=%d calls=%d", exchanges.Load(), refreshes.Load(), apiCalls.Load())
	}
	// Changing resolved client configuration must never reuse an old session.
	changed, err := sessions.Run(context.Background(), RunOptions{Path: path, Vars: map[string]string{"ENDPOINT": srv.URL + "/changed"}})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Summary.Status != spi.StatusError {
		t.Fatal("changed config reused session")
	}
	// Session ownership is explicit; headless workspace runs cannot see it.
	headless, err := Run(context.Background(), RunOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if headless.Summary.Status != spi.StatusError {
		t.Fatal("headless run used desktop session")
	}
	if err := sessions.OAuthLogout(req); err != nil {
		t.Fatal(err)
	}
	if signed, _ := sessions.OAuthStatus(req); signed {
		t.Fatal("logout kept session")
	}
	result, err := sessions.Run(context.Background(), RunOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Status != spi.StatusError || !strings.Contains(result.Suites[0].Cases[0].Steps[0].Error.Message, "requires refreshToken for CLI/CI") {
		t.Fatal("logout did not remove credentials")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("login/run/logout wrote project file")
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatal("OAuth flow wrote extra files")
	}
}

func TestOAuthLogoutSupersedesPendingLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": rand.Text(), "expires_in": 3600})
	}))
	defer srv.Close()
	project := map[string]any{
		"schemaVersion": "1.0", "name": "login", "suites": []any{},
		"auth": []any{map[string]any{"name": "login", "type": "oauth2", "config": map[string]any{
			"grant": "authorization_code", "authUrl": srv.URL, "tokenUrl": srv.URL, "clientId": "client",
		}}},
	}
	data, _ := json.Marshal(project)
	path := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var sessions OAuthSessions
	req := OAuthRequest{Path: path, AuthName: "login"}
	err := sessions.OAuthLogin(context.Background(), req, func(target string) error {
		if err := sessions.OAuthLogout(req); err != nil {
			return err
		}
		u, _ := url.Parse(target)
		q := u.Query()
		resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "code": {rand.Text()}}.Encode())
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "superseded or signed out") {
		t.Fatalf("pending login survived logout: %v", err)
	}
	if signed, _ := sessions.OAuthStatus(req); signed {
		t.Fatal("pending login restored a signed-out session")
	}
}
