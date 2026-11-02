package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// TestParallelCaseExecution runs many cases with parallelism > 1 and asserts
// (a) all of them execute, (b) they actually overlap in time. Run with -race to
// validate concurrency safety of the runner.
func TestParallelCaseExecution(t *testing.T) {
	var inFlight, maxInFlight int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cur := atomic.AddInt32(&inFlight, 1)
		// Track the high-water mark of concurrent in-flight requests.
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	const n = 8
	cases := make([]model.TestCase, n)
	for i := 0; i < n; i++ {
		cases[i] = model.TestCase{
			Name: "c", Steps: []model.Step{{
				Name: "hit", Protocol: "rest",
				Request:    map[string]any{"url": "${BASE_URL}/"},
				Assertions: []model.Assertion{{Type: "status", Value: 200}},
			}},
		}
	}
	project := &model.Project{
		SchemaVersion: "1.0", Name: "parallel",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Suites: []model.Suite{{Name: "s", Cases: cases}},
	}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Parallel: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("expected all passed, got %s", res.Summary.Status)
	}
	if res.Summary.Steps != n {
		t.Fatalf("expected %d steps executed, got %d", n, res.Summary.Steps)
	}
	if got := atomic.LoadInt32(&maxInFlight); got < 2 {
		t.Fatalf("expected concurrent execution (maxInFlight>=2), got %d", got)
	}
	if got := atomic.LoadInt32(&maxInFlight); got > 4 {
		t.Fatalf("parallelism exceeded the configured limit of 4, got %d", got)
	}
}

// TestParallelCaseDeclarationOrder verifies that cases completing out of order
// (due to differing network latencies) are always assembled in project declaration order.
func TestParallelCaseDeclarationOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/c0":
			time.Sleep(60 * time.Millisecond)
		case "/c1":
			time.Sleep(40 * time.Millisecond)
		case "/c2":
			time.Sleep(20 * time.Millisecond)
		case "/c3":
			time.Sleep(0)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	names := []string{"case-0", "case-1", "case-2", "case-3"}
	cases := make([]model.TestCase, len(names))
	for i, name := range names {
		cases[i] = model.TestCase{
			Name: name,
			Steps: []model.Step{{
				Name:     "step",
				Protocol: "rest",
				Request:  map[string]any{"url": srv.URL + "/c" + string('0'+byte(i))},
				Assertions: []model.Assertion{
					{Type: "status", Value: 200},
				},
			}},
		}
	}

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "order-test",
		Environments: []model.Environment{
			{Name: "default", Default: true},
		},
		Suites: []model.Suite{{
			Name:     "suite",
			Parallel: 4,
			Cases:    cases,
		}},
	}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Parallel: 4})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Suites[0].Cases) != len(names) {
		t.Fatalf("expected %d cases, got %d", len(names), len(res.Suites[0].Cases))
	}
	for i, wantName := range names {
		gotName := res.Suites[0].Cases[i].Name
		if gotName != wantName {
			t.Fatalf("case at index %d out of order: got %q, want %q", i, gotName, wantName)
		}
	}
}
