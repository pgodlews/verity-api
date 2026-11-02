package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

// runScopeServer issues a token at /login and records the Authorization
// header of every /data call.
func runScopeServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "tok-run-scope-1234", "region": "eu"})
		default:
			mu.Lock()
			seen = append(seen, r.Header.Get("Authorization")+"|"+r.URL.Query().Get("region"))
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func dataStep(base string) model.Step {
	return model.Step{
		Name:     "data",
		Protocol: "rest",
		Request: map[string]any{
			"url":     base + "/data?region=${region}",
			"headers": map[string]any{"Authorization": "Bearer ${token}"},
		},
	}
}

func TestRunScopedTransferFeedsLaterSuites(t *testing.T) {
	srv, seen := runScopeServer(t)
	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "runscope",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"token": "unset", "region": "env-region"}}},
		Suites: []model.Suite{
			{Name: "login", Cases: []model.TestCase{{Name: "login", Steps: []model.Step{{
				Name: "login", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/login"},
				Transfers: []model.TransferSpec{
					{Name: "token", From: "jsonpath", Expr: "$.token", Scope: model.TransferScopeRun},
					// Case scope (the default): not visible to other suites.
					{Name: "region", From: "jsonpath", Expr: "$.region"},
				},
			}}}}},
			{Name: "api", Cases: []model.TestCase{
				{Name: "a", Steps: []model.Step{dataStep(srv.URL)}},
				{Name: "b", Steps: []model.Step{dataStep(srv.URL)}},
			}},
			{Name: "shadow", Vars: map[string]string{"token": "suite-wins"},
				Cases: []model.TestCase{{Name: "c", Steps: []model.Step{dataStep(srv.URL)}}}},
		},
	}
	res := retryRun(t, nil, p, Options{})
	if res.Summary.Failed+res.Summary.Errors != 0 {
		t.Fatalf("summary %+v", res.Summary)
	}
	got := seen()
	want := []string{
		"Bearer tok-run-scope-1234|env-region",
		"Bearer tok-run-scope-1234|env-region",
		"Bearer suite-wins|env-region",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requests\n got %v\nwant %v", got, want)
	}
	// The captured token is a credential: masked in the capture list and in
	// the later request it was sent with.
	login := res.Suites[0].Cases[0].Steps[0]
	if login.Captures[0].Value == "tok-run-scope-1234" {
		t.Fatalf("run-scoped token shown in captures: %+v", login.Captures)
	}
	out, _ := json.Marshal(res.Suites[1])
	if strings.Contains(string(out), "tok-run-scope-1234") {
		t.Fatalf("token leaked into a later suite's result: %s", out)
	}
}

func TestRunScopedTransferIgnoredByCLIOverride(t *testing.T) {
	srv, seen := runScopeServer(t)
	p := &model.Project{
		SchemaVersion: "1.0",
		Name:          "runscope",
		Environments:  []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{
			{Name: "login", Cases: []model.TestCase{{Name: "login", Steps: []model.Step{{
				Name: "login", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/login"},
				Transfers: []model.TransferSpec{{Name: "token", From: "jsonpath", Expr: "$.token", Scope: model.TransferScopeRun}},
			}}}}},
			{Name: "api", Cases: []model.TestCase{{Name: "a", Steps: []model.Step{dataStep(srv.URL)}}}},
		},
	}
	retryRun(t, nil, p, Options{ExtraVars: map[string]string{"token": "from-cli", "region": "r"}})
	if got := seen(); len(got) != 1 || got[0] != "Bearer from-cli|r" {
		t.Fatalf("--var must win over run-scoped captures in later cases: %v", got)
	}
}

func TestTransferScopeValidation(t *testing.T) {
	p := oneStepProject(model.Step{Name: "s", Protocol: "rest", Request: map[string]any{"url": "http://x"},
		Transfers: []model.TransferSpec{{Name: "a", From: "body", Scope: "global"}}})
	errs := p.Validate()
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "scope") {
		t.Fatalf("want scope validation error, got %v", errs)
	}
}
