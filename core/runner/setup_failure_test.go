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

// A failed setup step skips the suite's cases (with setup_failed) and still
// runs teardown; other suites are unaffected.
func TestFailedSetupSkipsSuiteCases(t *testing.T) {
	var caseHits, teardownHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/setup":
			w.WriteHeader(http.StatusInternalServerError)
		case "/case":
			caseHits.Add(1)
		case "/teardown":
			teardownHits.Add(1)
		}
	}))
	defer srv.Close()
	p := bailProject([]model.Suite{
		{
			Name:     "broken",
			Setup:    &model.Hook{Name: "up", Steps: []model.Step{restStep(srv.URL+"/setup", 200)}},
			Teardown: &model.Hook{Name: "down", Steps: []model.Step{restStep(srv.URL+"/teardown", 200)}},
			Cases:    []model.TestCase{restCase("a", srv.URL+"/case", 200), restCase("b", srv.URL+"/case", 200)},
		},
		{Name: "other", Cases: []model.TestCase{restCase("c", srv.URL+"/other", 200)}},
	})
	res := retryRun(t, nil, p, Options{})
	broken := res.Suites[0].Cases
	if len(broken) != 4 || broken[0].Status != spi.StatusFailed {
		t.Fatalf("cases = %+v", broken)
	}
	for _, c := range broken[1:3] {
		if c.Status != spi.StatusSkipped || len(c.Steps) != 1 || c.Steps[0].Error == nil || c.Steps[0].Error.Code != string(verr.CodeSetupFailed) {
			t.Fatalf("case %q = %+v, want skipped with setup_failed", c.Name, c)
		}
	}
	if caseHits.Load() != 0 || teardownHits.Load() != 1 {
		t.Fatalf("case requests %d, teardown requests %d", caseHits.Load(), teardownHits.Load())
	}
	if res.Suites[1].Cases[0].Status != spi.StatusPassed {
		t.Fatalf("other suite = %s", res.Suites[1].Cases[0].Status)
	}
}
