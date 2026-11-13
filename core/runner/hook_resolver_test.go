package runner

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

// A value captured by one setup (or teardown) step is visible to the next
// step of the same hook.
func TestHookStepsShareTransfers(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.URL.Query().Get("id")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"from-hook"}`))
	}))
	defer srv.Close()
	hook := func(prefix string) *model.Hook {
		return &model.Hook{Name: prefix, Steps: []model.Step{
			{Name: "create", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/" + prefix + "-create"},
				Transfers: []model.TransferSpec{{Name: "id", From: "jsonpath", Expr: "$.id"}}},
			{Name: "use", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/" + prefix + "-use?id=${id}"}},
		}}
	}
	p := &model.Project{
		SchemaVersion: "1.0", Name: "hooks",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{
			Name: "s", Setup: hook("setup"), Teardown: hook("teardown"),
			Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{Name: "x", Protocol: "rest", Request: map[string]any{"url": srv.URL + "/case"}}}}},
		}},
	}
	retryRun(t, nil, p, Options{})
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/setup-use", "/teardown-use"} {
		if seen[path] != "from-hook" {
			t.Errorf("%s saw id=%q, want the earlier hook step's capture", path, seen[path])
		}
	}
}
