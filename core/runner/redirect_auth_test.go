package runner

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

func runProjectYAML(t *testing.T, yaml string, vars map[string]string) *spi.RunResult {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	project, err := model.Load(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{ExtraVars: vars, FollowRedirects: true})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// An API key header set by auth must not reach another origin through a
// redirect. Go only strips Authorization and Cookie itself.
func TestAuthHeadersDoNotFollowCrossOriginRedirect(t *testing.T) {
	const key = "api-key-value-0123456789abcdef"
	var mu sync.Mutex
	got := map[string]string{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("X-Api-Key")
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("X-Api-Key")
		mu.Unlock()
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
		case "/home":
			http.Redirect(w, r, "/stayed", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer origin.Close()
	t.Setenv("API_KEY", key)
	res := runProjectYAML(t, `
schemaVersion: "1.0"
name: redirect
auth:
  - name: key
    type: apikey
    config: { in: header, name: X-Api-Key, value: "${env:API_KEY}" }
suites:
  - name: s
    auth: { use: [key] }
    cases:
      - name: c
        steps:
          - name: away
            protocol: rest
            request: { method: GET, url: "${ORIGIN}/away" }
            assertions: [{ type: status, value: 204 }]
          - name: home
            protocol: rest
            request: { method: GET, url: "${ORIGIN}/home" }
            assertions: [{ type: status, value: 204 }]
`, map[string]string{"ORIGIN": origin.URL})
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("run %s", res.Summary.Status)
	}
	if got["/away"] != key || got["/home"] != key {
		t.Fatalf("first hop missing the key: %+v", got)
	}
	if got["/stayed"] != key {
		t.Fatal("a same-origin redirect should keep the auth header")
	}
	if got["/landed"] != "" {
		t.Fatal("the API key was forwarded to another origin")
	}
}

// A signature covers the original URL, so a signed request returns the 3xx
// instead of replaying a stale signature somewhere else.
func TestSignedRequestDoesNotFollowRedirects(t *testing.T) {
	var landed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		landed = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("HMAC_SECRET", "hmac-secret-value-0123456789")
	res := runProjectYAML(t, `
schemaVersion: "1.0"
name: signed-redirect
auth:
  - name: signer
    type: hmac
    config: { secret: "${env:HMAC_SECRET}" }
suites:
  - name: s
    auth: { use: [signer] }
    cases:
      - name: c
        steps:
          - name: start
            protocol: rest
            request: { method: GET, url: "${BASE}/start" }
            assertions: [{ type: status, value: 302 }]
`, map[string]string{"BASE": srv.URL})
	if res.Summary.Status != spi.StatusPassed || landed {
		t.Fatalf("run %s landed %v", res.Summary.Status, landed)
	}
}

// Introspection posts its own body, so it needs its own signature. The
// server rejects any POST whose HMAC does not match that request's body.
func TestIntrospectionIsSignedForItsOwnBody(t *testing.T) {
	const secret = "hmac-secret-value-0123456789"
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		if r.Header.Get("X-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "__schema") {
			_, _ = io.WriteString(w, `{"data":{"__schema":{"queryType":{"name":"Query"},"mutationType":null,"subscriptionType":null,"directives":[],"types":[`+
				`{"kind":"OBJECT","name":"Query","fields":[{"name":"hello","args":[],"type":{"kind":"SCALAR","name":"String","ofType":null},"isDeprecated":false,"deprecationReason":null}],"inputFields":null,"interfaces":[],"enumValues":null,"possibleTypes":null},`+
				`{"kind":"SCALAR","name":"String","fields":null,"inputFields":null,"interfaces":null,"enumValues":null,"possibleTypes":null}]}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"hello":"hi"}}`)
	}))
	defer srv.Close()
	t.Setenv("HMAC_SECRET", secret)
	res := runProjectYAML(t, `
schemaVersion: "1.0"
name: signed-introspection
auth:
  - name: signer
    type: hmac
    config: { secret: "${env:HMAC_SECRET}" }
suites:
  - name: s
    auth: { use: [signer] }
    cases:
      - name: c
        steps:
          - name: hello
            protocol: graphql
            schema: { introspect: true }
            request: { url: "${BASE}/graphql", query: "{ hello }" }
            assertions: [{ type: graphql_schema }]
`, map[string]string{"BASE": srv.URL})
	step := res.Suites[0].Cases[0].Steps[0]
	if step.Status != spi.StatusPassed {
		t.Fatalf("status %s error %+v assertions %+v", step.Status, step.Error, step.Assertions)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "__schema") {
		t.Fatalf("expected the operation and a signed introspection, got %d requests", len(bodies))
	}
}
