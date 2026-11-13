package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// A token captured by a login step is usable by a later step's auth in the
// same case, and two cases with different captured tokens do not share a
// provider (each sends its own token).
func TestAuthUsesTokenCapturedEarlierInTheCase(t *testing.T) {
	var logins atomic.Int32
	var mu sync.Mutex
	var used []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			n := logins.Add(1)
			fmt.Fprintf(w, `{"accessToken":"captured-token-value-%04d"}`, n)
			return
		}
		mu.Lock()
		used = append(used, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	steps := []model.Step{
		{
			Name: "login", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/login"},
			Transfers: []model.TransferSpec{{Name: "accessToken", From: "jsonpath", Expr: "$.accessToken"}},
		},
		{
			Name: "call", Protocol: "rest", Auth: &model.AuthRef{Use: []string{"api"}},
			Request:    map[string]any{"url": srv.URL + "/data"},
			Assertions: []model.Assertion{{Type: "status", Value: 200}},
		},
	}
	p := &model.Project{
		SchemaVersion: "1.0", Name: "captured",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Auth:         []model.AuthDef{{Name: "api", Type: "bearer", Config: map[string]any{"token": "${accessToken}"}}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{
			{Name: "first", Steps: steps},
			{Name: "second", Steps: steps},
		}}},
	}
	res := retryRun(t, nil, p, Options{})
	for _, c := range res.Suites[0].Cases {
		if c.Status != spi.StatusPassed {
			t.Fatalf("case %s = %s: %+v", c.Name, c.Status, c.Steps)
		}
	}
	want := []string{"Bearer captured-token-value-0001", "Bearer captured-token-value-0002"}
	if fmt.Sprint(used) != fmt.Sprint(want) {
		t.Fatalf("Authorization headers %q, want %q", used, want)
	}
}
