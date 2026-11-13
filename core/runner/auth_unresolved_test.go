package runner

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// An auth config that still holds a ${...} after interpolation must fail the
// step with auth_error instead of sending "Bearer ${accessToken}".
func TestAuthConfigWithUnresolvedReferenceFails(t *testing.T) {
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sent.Add(1)
		}
		_, _ = w.Write([]byte(`{"token":"t"}`))
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "auth",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Auth: []model.AuthDef{
			{Name: "api", Type: "bearer", Config: map[string]any{"token": "${accessToken}"}},
			// The hmac message template's own placeholders are not references.
			{Name: "sig", Type: "hmac", Config: map[string]any{"secret": "k3y-value-0001", "header": "X-Sig", "message": "${method}\n${path}\n${body}"}},
		},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{
			{Name: "bearer", Steps: []model.Step{{
				Name: "call", Protocol: "rest", Auth: &model.AuthRef{Use: []string{"api"}},
				Request: map[string]any{"url": srv.URL},
			}}},
			{Name: "hmac", Steps: []model.Step{{
				Name: "call", Protocol: "rest", Auth: &model.AuthRef{Use: []string{"sig"}},
				Request: map[string]any{"url": srv.URL},
			}}},
		}}},
	}
	res := retryRun(t, nil, p, Options{})
	bearer := res.Suites[0].Cases[0].Steps[0]
	if bearer.Error == nil || bearer.Error.Code != string(verr.CodeAuth) {
		t.Fatalf("bearer step error = %+v, want %s", bearer.Error, verr.CodeAuth)
	}
	if sent.Load() != 0 {
		t.Fatal("a request carried the unresolved Authorization header")
	}
	if st := res.Suites[0].Cases[1].Steps[0].Status; st != spi.StatusPassed {
		t.Fatalf("hmac step = %s: %+v", st, res.Suites[0].Cases[1].Steps[0].Error)
	}
}
