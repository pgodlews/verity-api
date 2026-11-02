package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestCancelMidRunReportsUnstartedWorkAndRunsTeardown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/block" {
			once.Do(func() { close(started) })
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	u := srv.URL
	project := bailProject([]model.Suite{
		{
			Name: "first",
			Cases: []model.TestCase{
				{
					Name: "slow",
					Steps: []model.Step{
						restStep(u+"/block", 200),
						restStep(u+"/next", 200),
					},
				},
				restCase("queued", u+"/queued", 200),
			},
			Teardown: &model.Hook{Name: "cleanup", Steps: []model.Step{
				restStep(u+"/teardown", 200),
			}},
		},
		{
			Name: "second",
			Cases: []model.TestCase{
				restCase("other", u+"/other", 200),
			},
		},
	})
	// restStep uses the step name "call". Name the blocking step so the
	// report is easy to read; the URL is what the handler keys on.
	project.Suites[0].Cases[0].Steps[0].Name = "block"
	project.Suites[0].Cases[0].Steps[1].Name = "next"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(5 * time.Second):
		}
	}()

	res, err := New(bootstrap.Default()).Run(ctx, project, Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Aborted {
		t.Fatal("summary.aborted should be set")
	}
	if res.Summary.Status != spi.StatusError {
		t.Fatalf("status %s, want error (the in-flight step failed on cancel)", res.Summary.Status)
	}
	if len(res.Suites) != 2 {
		t.Fatalf("suites %d, want 2 (the suite that never started is still reported)", len(res.Suites))
	}

	slow := res.Suites[0].Cases[0]
	if slow.Name != "slow" || slow.Status != spi.StatusError {
		t.Fatalf("slow case: %s %s", slow.Name, slow.Status)
	}
	if len(slow.Steps) != 2 {
		t.Fatalf("slow steps %d, want 2", len(slow.Steps))
	}
	if slow.Steps[0].Status != spi.StatusError || slow.Steps[0].Attempts != 1 {
		t.Fatalf("in-flight step status=%s attempts=%d, want error and 1 attempt", slow.Steps[0].Status, slow.Steps[0].Attempts)
	}
	if slow.Steps[1].Status != spi.StatusAborted || slow.Steps[1].Attempts != 0 || slow.Steps[1].Request != nil {
		t.Fatalf("step after cancel: %+v, want aborted with no request", slow.Steps[1])
	}

	queued := res.Suites[0].Cases[1]
	if queued.Status != spi.StatusAborted || len(queued.Steps) != 1 || queued.Steps[0].Status != spi.StatusAborted {
		t.Fatalf("queued case: %+v", queued)
	}
	td := res.Suites[0].Cases[2]
	if td.Name != "teardown:cleanup" || td.Status != spi.StatusPassed {
		t.Fatalf("teardown: %s %s, want passed", td.Name, td.Status)
	}
	other := res.Suites[1].Cases[0]
	if other.Name != "other" || other.Status != spi.StatusAborted || other.Steps[0].Status != spi.StatusAborted {
		t.Fatalf("second suite: %+v", other)
	}
	if res.Summary.StepsAborted < 3 {
		t.Fatalf("stepsAborted=%d, want at least the three steps that never started", res.Summary.StepsAborted)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits["/block"] != 1 || hits["/teardown"] != 1 {
		t.Fatalf("hits=%v, want block and teardown once each", hits)
	}
	for _, p := range []string{"/next", "/queued", "/other"} {
		if hits[p] != 0 {
			t.Fatalf("path %s ran after cancel: %v", p, hits)
		}
	}
}

func TestCancelBeforeStartMarksEveryStepAborted(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	project := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{{
			Name: "c",
			Steps: []model.Step{
				restStep(srv.URL+"/a", 200),
				{Name: "skip-me", Protocol: "rest", Skip: true, Request: map[string]any{"url": srv.URL + "/b"}},
				restStep(srv.URL+"/c", 200),
			},
		}},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := New(bootstrap.Default()).Run(ctx, project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Aborted || res.Summary.Status != spi.StatusAborted {
		t.Fatalf("aborted=%v status=%s", res.Summary.Aborted, res.Summary.Status)
	}
	steps := res.Suites[0].Cases[0].Steps
	if len(steps) != 3 {
		t.Fatalf("steps %d, want 3", len(steps))
	}
	if steps[0].Status != spi.StatusAborted || steps[2].Status != spi.StatusAborted {
		t.Fatalf("statuses %s, %s, want aborted", steps[0].Status, steps[2].Status)
	}
	if steps[1].Status != spi.StatusSkipped {
		t.Fatalf("explicit skip status %s, want skipped", steps[1].Status)
	}
	if hits != 0 {
		t.Fatalf("cancelled run made %d requests", hits)
	}
}

func TestCancelBetweenFeedRows(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	project := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{{
			Name: "rows",
			Feed: &model.Feed{Type: "json", Inline: []map[string]string{
				{"n": "1"}, {"n": "2"}, {"n": "3"},
			}},
			Steps: []model.Step{restStep(srv.URL, 200)},
		}},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(5 * time.Second):
		}
	}()

	res, err := New(bootstrap.Default()).Run(ctx, project, Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cases := res.Suites[0].Cases
	if len(cases) != 3 {
		t.Fatalf("cases %d, want 3 feed rows", len(cases))
	}
	if cases[0].Status != spi.StatusError || cases[0].Iteration != 0 {
		t.Fatalf("row 0: status %s iteration %d", cases[0].Status, cases[0].Iteration)
	}
	for i := 1; i < 3; i++ {
		if cases[i].Status != spi.StatusAborted || cases[i].Iteration != i || cases[i].Steps[0].Status != spi.StatusAborted {
			t.Fatalf("row %d: %+v", i, cases[i])
		}
	}
	if hits != 1 {
		t.Fatalf("hits %d, want 1", hits)
	}
	if !res.Summary.Aborted {
		t.Fatal("summary.aborted should be set")
	}
}

func TestOnEventOrderAndRedaction(t *testing.T) {
	const secret = "event-bearer-token-xyz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"` + secret + `"}`))
	}))
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "events",
		Environments:  []model.Environment{{Name: "default", Default: true}},
		Auth: []model.AuthDef{{
			Name: "api", Type: "bearer",
			Config: map[string]any{"token": secret},
		}},
		Suites: []model.Suite{{
			Name: "s", Parallel: 4,
			Auth: &model.AuthRef{Use: []string{"api"}},
			Cases: []model.TestCase{
				{Name: "c0", Steps: []model.Step{restStep(srv.URL+"/0", 200), restStep(srv.URL+"/1", 200)}},
				{Name: "c1", Steps: []model.Step{restStep(srv.URL+"/2", 200)}},
				{Name: "c2", Steps: []model.Step{restStep(srv.URL+"/3", 200)}},
				{Name: "c3", Steps: []model.Step{restStep(srv.URL+"/4", 200)}},
			},
		}},
	}

	var mu sync.Mutex
	var events []spi.RunEvent
	opts := Options{OnEvent: func(ev spi.RunEvent) {
		// Copy immediately. The runner may reuse nothing, but the callback
		// must tolerate being invoked from several goroutines.
		mu.Lock()
		defer mu.Unlock()
		cp := ev
		if ev.Step != nil {
			step := *ev.Step
			cp.Step = &step
		}
		events = append(events, cp)
	}}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("status %s", res.Summary.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	byCase := map[string][]string{}
	var leaked bool
	for _, ev := range events {
		byCase[ev.Case] = append(byCase[ev.Case], ev.Kind)
		if ev.Step == nil {
			continue
		}
		raw, mErr := json.Marshal(ev.Step)
		if mErr != nil {
			t.Fatal(mErr)
		}
		if strings.Contains(string(raw), secret) {
			leaked = true
		}
		if !strings.Contains(string(raw), secrets.Mask) {
			t.Fatalf("event step for %s did not mask the bearer token: %s", ev.Case, raw)
		}
	}
	if leaked {
		t.Fatal("secret leaked into an OnEvent step result")
	}
	want := map[string][]string{
		"c0": {spi.EventCaseStarted, spi.EventStepDone, spi.EventStepDone, spi.EventCaseDone},
		"c1": {spi.EventCaseStarted, spi.EventStepDone, spi.EventCaseDone},
		"c2": {spi.EventCaseStarted, spi.EventStepDone, spi.EventCaseDone},
		"c3": {spi.EventCaseStarted, spi.EventStepDone, spi.EventCaseDone},
	}
	for name, kinds := range want {
		got := byCase[name]
		if strings.Join(got, ",") != strings.Join(kinds, ",") {
			t.Fatalf("case %s events %v, want %v", name, got, kinds)
		}
	}

	// The step handed to the callback matches the final, redacted result.
	final := res.Suites[0].Cases[0].Steps[0]
	var seen *spi.StepResult
	for _, ev := range events {
		if ev.Kind == spi.EventStepDone && ev.Case == "c0" && ev.StepIndex == 0 {
			seen = ev.Step
			break
		}
	}
	if seen == nil {
		t.Fatal("missing step_done for c0 step 0")
	}
	if seen.Status != final.Status || seen.Request == nil || final.Request == nil {
		t.Fatalf("event step not comparable with the final result")
	}
	if seen.Request.Headers["Authorization"] != final.Request.Headers["Authorization"] {
		t.Fatalf("event auth header %q, final %q", seen.Request.Headers["Authorization"], final.Request.Headers["Authorization"])
	}
	if seen.Response == nil || seen.Response.Body != final.Response.Body {
		t.Fatalf("event body not the final redacted body")
	}
}
