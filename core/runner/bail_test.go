package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

func bailProject(suites []model.Suite) *model.Project {
	return &model.Project{
		SchemaVersion: "1.0",
		Name:          "bail",
		Environments: []model.Environment{{
			Name: "default", Default: true,
		}},
		Suites: suites,
	}
}

func restCase(name, url string, want int) model.TestCase {
	return model.TestCase{
		Name: name,
		Steps: []model.Step{{
			Name:     "call",
			Protocol: "rest",
			Request:  map[string]any{"url": url},
			Assertions: []model.Assertion{
				{Type: "status", Value: want},
			},
		}},
	}
}

func restStep(url string, want int) model.Step {
	return restCase("", url, want).Steps[0]
}

func assertCaseStatuses(t *testing.T, got []spi.CaseResult, names []string, want []spi.Status) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d cases, want %d (%v)", len(got), len(want), caseNames(got))
	}
	for i := range want {
		if got[i].Name != names[i] || got[i].Status != want[i] {
			t.Fatalf("case %d: got %q %s, want %q %s", i, got[i].Name, got[i].Status, names[i], want[i])
		}
		if want[i] == spi.StatusSkipped && len(got[i].Steps) != 0 {
			t.Fatalf("skipped case %q ran %d steps", got[i].Name, len(got[i].Steps))
		}
	}
}

func caseNames(cases []spi.CaseResult) []string {
	out := make([]string, len(cases))
	for i, c := range cases {
		out[i] = c.Name
	}
	return out
}

func TestBailSerialSkipsLaterCasesAndSuites(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/bad" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	u := srv.URL

	project := bailProject([]model.Suite{
		{
			Name: "first",
			Cases: []model.TestCase{
				restCase("ok", u+"/ok", 200),
				restCase("bad", u+"/bad", 200),
				restCase("later", u+"/later", 200),
			},
		},
		{
			Name: "second",
			Setup: &model.Hook{Name: "prep", Steps: []model.Step{
				restStep(u+"/setup2", 200),
			}},
			Cases: []model.TestCase{
				restCase("other", u+"/other", 200),
			},
			Teardown: &model.Hook{Name: "cleanup", Steps: []model.Step{
				restStep(u+"/teardown2", 200),
			}},
		},
	})

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Bail: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Bailed {
		t.Fatal("summary should say the run bailed")
	}
	if res.Summary.Status != spi.StatusFailed {
		t.Fatalf("status %s, want failed", res.Summary.Status)
	}
	if len(res.Suites) != 2 {
		t.Fatalf("got %d suites, want 2", len(res.Suites))
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"ok", "bad", "later"},
		[]spi.Status{spi.StatusPassed, spi.StatusFailed, spi.StatusSkipped})
	assertCaseStatuses(t, res.Suites[1].Cases,
		[]string{"setup:prep", "other", "teardown:cleanup"},
		[]spi.Status{spi.StatusSkipped, spi.StatusSkipped, spi.StatusSkipped})

	mu.Lock()
	defer mu.Unlock()
	if hits["/ok"] != 1 || hits["/bad"] != 1 {
		t.Fatalf("expected the passing and failing cases to run, hits=%v", hits)
	}
	for _, p := range []string{"/later", "/setup2", "/other", "/teardown2"} {
		if hits[p] != 0 {
			t.Fatalf("path %s was requested after bail: %v", p, hits)
		}
	}
}

func TestBailFailedSetupStillRunsTeardown(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/setup" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	u := srv.URL

	project := bailProject([]model.Suite{{
		Name: "s",
		Setup: &model.Hook{Name: "prep", Steps: []model.Step{
			restStep(u+"/setup", 200),
		}},
		Cases: []model.TestCase{
			restCase("case", u+"/case", 200),
		},
		Teardown: &model.Hook{Name: "cleanup", Steps: []model.Step{
			restStep(u+"/teardown", 200),
		}},
	}})

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Bail: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Bailed || res.Summary.Status != spi.StatusFailed {
		t.Fatalf("bailed=%v status=%s", res.Summary.Bailed, res.Summary.Status)
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"setup:prep", "case", "teardown:cleanup"},
		[]spi.Status{spi.StatusFailed, spi.StatusSkipped, spi.StatusPassed})

	mu.Lock()
	defer mu.Unlock()
	if hits["/setup"] != 1 || hits["/teardown"] != 1 || hits["/case"] != 0 {
		t.Fatalf("hits=%v", hits)
	}
}

func TestBailParallelInFlightFinishes(t *testing.T) {
	holdEntered := make(chan struct{})
	var onceHold sync.Once
	var extra int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail":
			select {
			case <-holdEntered:
			case <-time.After(3 * time.Second):
				t.Errorf("failing request did not observe the in-flight request")
			}
			w.WriteHeader(500)
		case "/hold":
			onceHold.Do(func() { close(holdEntered) })
			// Stay in flight until the failing case has recorded bail and the
			// scheduler has skipped the cases waiting on the pool. The fail
			// handler returns as soon as we have entered, so this sleep is
			// the overlap, not the signal.
			time.Sleep(500 * time.Millisecond)
			w.WriteHeader(200)
		default:
			atomic.AddInt32(&extra, 1)
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()
	u := srv.URL

	project := bailProject([]model.Suite{{
		Name:     "parallel",
		Parallel: 2,
		Cases: []model.TestCase{
			restCase("fail", u+"/fail", 200),
			restCase("hold", u+"/hold", 200),
			restCase("skip-a", u+"/skip-a", 200),
			restCase("skip-b", u+"/skip-b", 200),
		},
	}})

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Bail: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Bailed {
		t.Fatal("summary should say the run bailed")
	}
	if res.Summary.Status != spi.StatusFailed {
		t.Fatalf("status %s, want failed", res.Summary.Status)
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"fail", "hold", "skip-a", "skip-b"},
		[]spi.Status{spi.StatusFailed, spi.StatusPassed, spi.StatusSkipped, spi.StatusSkipped})
	if got := atomic.LoadInt32(&extra); got != 0 {
		t.Fatalf("skipped cases made %d requests", got)
	}
	if res.Summary.Steps != 2 {
		t.Fatalf("steps=%d, want 2 (the two cases that started)", res.Summary.Steps)
	}
}

func TestBailTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	u := srv.URL
	srv.Close()

	var extra int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&extra, 1)
		w.WriteHeader(200)
	}))
	defer live.Close()

	project := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{
			restCase("down", u+"/down", 200),
			restCase("next", live.URL+"/next", 200),
		},
	}})
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Bail: true, Parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Bailed || res.Summary.Status != spi.StatusError {
		t.Fatalf("bailed=%v status=%s, want bailed error", res.Summary.Bailed, res.Summary.Status)
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"down", "next"},
		[]spi.Status{spi.StatusError, spi.StatusSkipped})
	if got := atomic.LoadInt32(&extra); got != 0 {
		t.Fatalf("case after the error ran (%d hits)", got)
	}
}

func TestBailDisabledRunsEveryCase(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/bad" {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	u := srv.URL

	project := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{
			restCase("ok", u+"/ok", 200),
			restCase("bad", u+"/bad", 200),
			restCase("after", u+"/after", 200),
		},
	}})
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Parallel: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Bailed {
		t.Fatal("bail flag was not set; summary must not say bailed")
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"ok", "bad", "after"},
		[]spi.Status{spi.StatusPassed, spi.StatusFailed, spi.StatusPassed})
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("hits=%d, want 3", got)
	}
}

func TestBailAllPassDoesNotMarkBailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	project := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{
			restCase("a", srv.URL+"/a", 200),
			restCase("b", srv.URL+"/b", 200),
		},
	}})
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{Bail: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Bailed || res.Summary.Status != spi.StatusPassed {
		t.Fatalf("bailed=%v status=%s", res.Summary.Bailed, res.Summary.Status)
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"a", "b"},
		[]spi.Status{spi.StatusPassed, spi.StatusPassed})
}
