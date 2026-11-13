package runner

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

// Step vars are local to their step and applied in sorted name order;
// transfers still reach later steps.
func TestStepVarsAreStepLocal(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.URL.RawQuery
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"cap"}`))
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "vars",
		Environments: []model.Environment{{Name: "default", Default: true, Variables: map[string]string{"v": "env"}}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{
			{
				Name: "first", Protocol: "rest",
				Vars:      map[string]string{"a": "1", "b": "${a}-2", "v": "step"},
				Request:   map[string]any{"url": srv.URL + "/first?b=${b}&v=${v}"},
				Transfers: []model.TransferSpec{{Name: "id", From: "jsonpath", Expr: "$.id"}},
			},
			{Name: "second", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/second?v=${v}&id=${id}"}},
		}}}}},
	}
	retryRun(t, nil, p, Options{})
	mu.Lock()
	defer mu.Unlock()
	if seen["/first"] != "b=1-2&v=step" {
		t.Errorf("first step saw %q", seen["/first"])
	}
	if seen["/second"] != "v=env&id=cap" {
		t.Errorf("second step saw %q, want the step var gone and the transfer kept", seen["/second"])
	}
}
