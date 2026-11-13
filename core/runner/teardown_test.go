package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// setTeardownTimeout shortens the cancelled-run grace budget for one test.
func setTeardownTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := teardownTimeout
	teardownTimeout = d
	t.Cleanup(func() { teardownTimeout = old })
}

// A run that was not cancelled must not cap teardown at the grace budget:
// a slow teardown step with its own timeoutMs passes.
func TestTeardownIgnoresGraceBudgetWhenNotCancelled(t *testing.T) {
	setTeardownTimeout(t, 50*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	slow := restStep(srv.URL+"/slow", 200)
	slow.TimeoutMs = 3000
	project := bailProject([]model.Suite{{
		Name:     "s",
		Cases:    []model.TestCase{restCase("c", srv.URL+"/fast", 200)},
		Teardown: &model.Hook{Name: "cleanup", Steps: []model.Step{slow, slow}},
	}})
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Suites[0].Cases {
		if c.Status != spi.StatusPassed {
			t.Fatalf("case %q = %s, want passed", c.Name, c.Status)
		}
	}
}

// Cancelling while a teardown step runs interrupts that step, and the
// remaining teardown steps still run under the grace budget.
func TestTeardownHonoursCancelThenRunsRemainingStepsInGrace(t *testing.T) {
	setTeardownTimeout(t, 5*time.Second)
	inTeardown := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/block" {
			once.Do(func() { close(inTeardown) })
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	project := bailProject([]model.Suite{{
		Name:  "s",
		Cases: []model.TestCase{restCase("c", srv.URL+"/fast", 200)},
		Teardown: &model.Hook{Name: "cleanup", Steps: []model.Step{
			restStep(srv.URL+"/block", 200),
			restStep(srv.URL+"/after", 200),
		}},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-inTeardown:
			cancel()
		case <-time.After(5 * time.Second):
		}
	}()
	start := time.Now()
	res, err := New(bootstrap.Default()).Run(ctx, project, Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("run took %v; cancel did not interrupt the teardown step", elapsed)
	}
	mu.Lock()
	after := hits["/after"]
	mu.Unlock()
	if after != 1 {
		t.Fatalf("remaining teardown step ran %d times, want 1", after)
	}
	if !res.Summary.Aborted {
		t.Fatal("summary.aborted should be set")
	}
}
