package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func retryRun(t *testing.T, ctx context.Context, p *model.Project, opts Options) *spi.RunResult {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Second
	}
	res, err := New(bootstrap.Default()).Run(ctx, p, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func oneStepProject(step model.Step) *model.Project {
	return &model.Project{
		SchemaVersion: "1.0",
		Name:          "retry",
		Environments:  []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{
			Name:  "s",
			Cases: []model.TestCase{{Name: "c", Steps: []model.Step{step}}},
		}},
	}
}

func statusStep(url string, want int, retry *model.Retry, timeoutMs int) model.Step {
	return model.Step{
		Name:      "call",
		Protocol:  "rest",
		Request:   map[string]any{"url": url},
		TimeoutMs: timeoutMs,
		Retry:     retry,
		Assertions: []model.Assertion{
			{Type: "status", Value: want},
		},
	}
}

func scriptedServer(t *testing.T, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(hits.Add(1))
		code := codes[len(codes)-1]
		if i-1 < len(codes) {
			code = codes[i-1]
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Attempt", fmt.Sprintf("%d", i))
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"n":%d}`, i)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestStepRetryModes(t *testing.T) {
	tests := []struct {
		name         string
		codes        []int
		until        string
		count        int
		delayMs      int
		wantAssert   int
		wantHits     int32
		wantAttempts int
		wantStep     spi.Status
		minDuration  time.Duration
	}{
		{
			name:  "passed retries assertion failures then succeeds",
			codes: []int{500, 500, 200}, until: model.RetryUntilPassed, count: 4,
			wantHits: 3, wantAttempts: 3, wantStep: spi.StatusPassed,
		},
		{
			name:  "passed stops at the first success",
			codes: []int{500, 200, 500}, until: model.RetryUntilPassed, count: 5,
			wantHits: 2, wantAttempts: 2, wantStep: spi.StatusPassed,
		},
		{
			name:  "passed exhausts the attempt cap",
			codes: []int{500}, until: model.RetryUntilPassed, count: 2,
			wantHits: 3, wantAttempts: 3, wantStep: spi.StatusFailed,
		},
		{
			name:  "status retries 500",
			codes: []int{500, 200}, until: model.RetryUntilStatus, count: 3,
			wantHits: 2, wantAttempts: 2, wantStep: spi.StatusPassed,
		},
		{
			name:  "status retries 429",
			codes: []int{429, 200}, until: model.RetryUntilStatus, count: 3,
			wantHits: 2, wantAttempts: 2, wantStep: spi.StatusPassed,
		},
		{
			name:  "status retries 599",
			codes: []int{599, 200}, until: model.RetryUntilStatus, count: 3,
			wantHits: 2, wantAttempts: 2, wantStep: spi.StatusPassed,
		},
		{
			name:  "status does not retry 400",
			codes: []int{400, 200}, until: model.RetryUntilStatus, count: 3,
			wantHits: 1, wantAttempts: 1, wantStep: spi.StatusFailed,
		},
		{
			name:  "status does not retry 499",
			codes: []int{499, 200}, until: model.RetryUntilStatus, count: 3,
			wantHits: 1, wantAttempts: 1, wantStep: spi.StatusFailed,
		},
		{
			name:  "status does not retry 428",
			codes: []int{428, 200}, until: model.RetryUntilStatus, count: 3,
			wantHits: 1, wantAttempts: 1, wantStep: spi.StatusFailed,
		},
		{
			name:  "passed does not retry a matching 502",
			codes: []int{502, 200}, until: model.RetryUntilPassed, count: 3,
			wantAssert: 502, wantHits: 1, wantAttempts: 1, wantStep: spi.StatusPassed,
		},
		{
			name:  "status retries 502 even when the assertion matches",
			codes: []int{502, 200}, until: model.RetryUntilStatus, count: 3,
			wantAssert: 502, wantHits: 2, wantAttempts: 2, wantStep: spi.StatusFailed,
		},
		{
			name:     "no retry policy is a single attempt",
			codes:    []int{500, 200},
			wantHits: 1, wantAttempts: 1, wantStep: spi.StatusFailed,
		},
		{
			name:  "delay is included in the reported duration",
			codes: []int{500, 500, 200}, until: model.RetryUntilPassed, count: 2, delayMs: 80,
			wantHits: 3, wantAttempts: 3, wantStep: spi.StatusPassed,
			minDuration: 140 * time.Millisecond,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := scriptedServer(t, tc.codes...)
			wantAssert := tc.wantAssert
			if wantAssert == 0 {
				wantAssert = 200
			}
			var policy *model.Retry
			if tc.until != "" {
				policy = &model.Retry{Count: tc.count, DelayMs: tc.delayMs, Until: tc.until}
			}
			res := retryRun(t, nil, oneStepProject(statusStep(srv.URL, wantAssert, policy, 0)), Options{})
			step := res.Suites[0].Cases[0].Steps[0]
			if step.Status != tc.wantStep {
				t.Fatalf("status %s, want %s (err=%v)", step.Status, tc.wantStep, step.Error)
			}
			if step.Attempts != tc.wantAttempts {
				t.Fatalf("attempts %d, want %d", step.Attempts, tc.wantAttempts)
			}
			if got := hits.Load(); got != tc.wantHits {
				t.Fatalf("hits %d, want %d", got, tc.wantHits)
			}
			if tc.minDuration > 0 && time.Duration(step.DurationMS)*time.Millisecond < tc.minDuration {
				t.Fatalf("duration %dms, want >= %s", step.DurationMS, tc.minDuration)
			}
			if step.Response != nil && tc.wantStep == spi.StatusPassed {
				if got := step.Response.Headers["X-Attempt"]; got != fmt.Sprintf("%d", tc.wantHits) {
					t.Fatalf("final response attempt header %q, want %d", got, tc.wantHits)
				}
			}
		})
	}
}

func TestRetryTransportThenSuccess(t *testing.T) {
	for _, until := range []string{model.RetryUntilPassed, model.RetryUntilStatus} {
		t.Run(until, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				if n == 1 {
					hj, ok := w.(http.Hijacker)
					if !ok {
						t.Errorf("response writer is not a hijacker")
						return
					}
					conn, _, err := hj.Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					conn.Close()
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			t.Cleanup(srv.Close)

			step := statusStep(srv.URL, 200, &model.Retry{Count: 2, Until: until}, 0)
			res := retryRun(t, nil, oneStepProject(step), Options{})
			got := res.Suites[0].Cases[0].Steps[0]
			if got.Status != spi.StatusPassed {
				t.Fatalf("status %s, want passed (err=%v)", got.Status, got.Error)
			}
			if got.Attempts != 2 || hits.Load() != 2 {
				t.Fatalf("attempts=%d hits=%d, want 2 and 2", got.Attempts, hits.Load())
			}
		})
	}
}

func TestRetryTimeoutIsPerAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	step := statusStep(srv.URL, 200, &model.Retry{Count: 1, Until: model.RetryUntilStatus}, 50)
	start := time.Now()
	// The run default is far longer than the step timeout. If timeoutMs were
	// ignored, the handler's sleep would finish and the step would pass.
	res := retryRun(t, nil, oneStepProject(step), Options{Timeout: 5 * time.Second})
	elapsed := time.Since(start)
	got := res.Suites[0].Cases[0].Steps[0]
	if got.Status != spi.StatusError {
		t.Fatalf("status %s, want error (timeoutMs should fail each attempt)", got.Status)
	}
	if got.Error == nil || got.Error.Code != string(verr.CodeTransport) {
		t.Fatalf("error %#v, want transport_error", got.Error)
	}
	if got.Attempts != 2 || hits.Load() != 2 {
		t.Fatalf("attempts=%d hits=%d, want 2 and 2", got.Attempts, hits.Load())
	}
	if elapsed > 800*time.Millisecond {
		t.Fatalf("elapsed %s; timeout looks shared across attempts or ignored", elapsed)
	}
}

func TestRetryTimeoutThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			time.Sleep(500 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	step := statusStep(srv.URL, 200, &model.Retry{Count: 2, Until: model.RetryUntilPassed}, 50)
	start := time.Now()
	res := retryRun(t, nil, oneStepProject(step), Options{Timeout: 5 * time.Second})
	elapsed := time.Since(start)
	got := res.Suites[0].Cases[0].Steps[0]
	if got.Status != spi.StatusPassed {
		t.Fatalf("status %s, want passed (err=%v)", got.Status, got.Error)
	}
	if got.Attempts != 2 || hits.Load() != 2 {
		t.Fatalf("attempts=%d hits=%d, want 2 and 2", got.Attempts, hits.Load())
	}
	if elapsed > 800*time.Millisecond {
		t.Fatalf("elapsed %s; first attempt did not time out on its own budget", elapsed)
	}
	if got.Response == nil || !strings.Contains(got.Response.Body, `"ok":true`) {
		t.Fatalf("final response = %#v", got.Response)
	}
}

func TestRunTimeoutUsedWhenStepTimeoutUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	step := statusStep(srv.URL, 200, nil, 0)
	start := time.Now()
	res := retryRun(t, nil, oneStepProject(step), Options{Timeout: 80 * time.Millisecond})
	elapsed := time.Since(start)
	got := res.Suites[0].Cases[0].Steps[0]
	if got.Status != spi.StatusError || got.Attempts != 1 {
		t.Fatalf("status %s attempts %d, want error and 1", got.Status, got.Attempts)
	}
	if elapsed > 350*time.Millisecond {
		t.Fatalf("run timeout not applied, elapsed %s", elapsed)
	}
}

func TestRetryCancelDuringDelay(t *testing.T) {
	var hits atomic.Int32
	first := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		once.Do(func() { close(first) })
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"n":1}`)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-first:
			time.Sleep(150 * time.Millisecond)
			cancel()
		case <-t.Context().Done():
		}
	}()

	step := statusStep(srv.URL, 200, &model.Retry{
		Count: 2, DelayMs: 5000, Until: model.RetryUntilPassed,
	}, 0)
	start := time.Now()
	res := retryRun(t, ctx, oneStepProject(step), Options{})
	elapsed := time.Since(start)
	got := res.Suites[0].Cases[0].Steps[0]
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed %s; delay did not observe cancellation", elapsed)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d, want 1", hits.Load())
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts %d, want 1", got.Attempts)
	}
	if got.Status != spi.StatusError {
		t.Fatalf("status %s, want error", got.Status)
	}
	if got.Error == nil || !strings.Contains(got.Error.Message, "retry delay cancelled") {
		t.Fatalf("error %#v, want retry delay cancelled", got.Error)
	}
	if got.Response == nil || got.Response.StatusCode != 500 {
		t.Fatalf("expected the attempt's response to be kept, got %#v", got.Response)
	}
}

func TestRetryCapturesFinalAttemptOnly(t *testing.T) {
	var hits atomic.Int32
	var used atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/use/") {
			used.Store(r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Attempt", fmt.Sprintf("%d", n))
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"id":"early"}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"final"}`)
	}))
	t.Cleanup(srv.Close)

	step := statusStep(srv.URL+"/item", 200, &model.Retry{Count: 4, Until: model.RetryUntilPassed}, 0)
	step.Transfers = []model.TransferSpec{{Name: "id", From: "jsonpath", Expr: "$.id"}}
	next := model.Step{
		Name:     "use",
		Protocol: "rest",
		Request:  map[string]any{"url": srv.URL + "/use/${id}"},
		Assertions: []model.Assertion{
			{Type: "status", Value: 200},
		},
	}

	p := oneStepProject(step)
	p.Suites[0].Cases[0].Steps = append(p.Suites[0].Cases[0].Steps, next)
	res := retryRun(t, nil, p, Options{})
	first := res.Suites[0].Cases[0].Steps[0]
	second := res.Suites[0].Cases[0].Steps[1]
	if first.Status != spi.StatusPassed || first.Attempts != 3 {
		t.Fatalf("first status %s attempts %d", first.Status, first.Attempts)
	}
	if first.Response == nil || strings.Contains(first.Response.Body, "early") || !strings.Contains(first.Response.Body, "final") {
		t.Fatalf("response body %q, want the final attempt only", bodyOf(first))
	}
	if first.Response.Headers["X-Attempt"] != "3" {
		t.Fatalf("response attempt header %q", first.Response.Headers["X-Attempt"])
	}
	if len(first.Captures) != 1 || first.Captures[0].Name != "id" || first.Captures[0].Value != "final" {
		t.Fatalf("captures %#v", first.Captures)
	}
	if second.Status != spi.StatusPassed {
		t.Fatalf("second status %s err=%v", second.Status, second.Error)
	}
	if path, _ := used.Load().(string); path != "/use/final" {
		t.Fatalf("follow-up path %q, want /use/final", path)
	}
}

func TestRetryDoesNotTransferEarlierAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/use") {
			w.Header().Set("X-Path", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			return
		}
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"id":"early"}`)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("no hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)

	step := statusStep(srv.URL+"/item", 200, &model.Retry{Count: 1, Until: model.RetryUntilPassed}, 0)
	step.Transfers = []model.TransferSpec{{Name: "id", From: "jsonpath", Expr: "$.id"}}
	next := model.Step{
		Name:     "use",
		Protocol: "rest",
		Request:  map[string]any{"url": srv.URL + "/use/${id}"},
		Assertions: []model.Assertion{
			{Type: "status", Value: 200},
		},
	}
	p := oneStepProject(step)
	p.Suites[0].Cases[0].Steps = append(p.Suites[0].Cases[0].Steps, next)
	res := retryRun(t, nil, p, Options{})
	first := res.Suites[0].Cases[0].Steps[0]
	second := res.Suites[0].Cases[0].Steps[1]
	if first.Status != spi.StatusError || first.Attempts != 2 {
		t.Fatalf("first status %s attempts %d err=%v", first.Status, first.Attempts, first.Error)
	}
	if len(first.Captures) != 0 {
		t.Fatalf("captures %#v, want none from the failed final attempt", first.Captures)
	}
	if second.Request == nil {
		t.Fatal("second step recorded no request")
	}
	if strings.Contains(second.Request.URL, "early") {
		t.Fatalf("earlier attempt leaked into the next step: %s", second.Request.URL)
	}
	if !strings.Contains(second.Request.URL, "${id}") {
		t.Fatalf("url %s, want the unresolved ${id}", second.Request.URL)
	}
}

func TestRetryPassDoesNotTripBail(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		n := hits[r.URL.Path]
		mu.Unlock()
		if r.URL.Path == "/flaky" && n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	flaky := statusStep(srv.URL+"/flaky", 200, &model.Retry{Count: 2, Until: model.RetryUntilPassed}, 0)
	p := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{
			{Name: "flaky", Steps: []model.Step{flaky}},
			restCase("next", srv.URL+"/next", 200),
		},
	}})
	res := retryRun(t, nil, p, Options{Bail: true})
	if res.Summary.Bailed {
		t.Fatal("a step that passed after retries tripped bail")
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("status %s, want passed", res.Summary.Status)
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"flaky", "next"},
		[]spi.Status{spi.StatusPassed, spi.StatusPassed})
	if res.Suites[0].Cases[0].Steps[0].Attempts != 2 {
		t.Fatalf("attempts %d, want 2", res.Suites[0].Cases[0].Steps[0].Attempts)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/flaky"] != 2 || hits["/next"] != 1 {
		t.Fatalf("hits %v", hits)
	}
}

func TestRetryExhaustedTripsBail(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	bad := statusStep(srv.URL+"/bad", 200, &model.Retry{Count: 1, Until: model.RetryUntilPassed}, 0)
	p := bailProject([]model.Suite{{
		Name: "s",
		Cases: []model.TestCase{
			{Name: "bad", Steps: []model.Step{bad}},
			restCase("next", srv.URL+"/next", 200),
		},
	}})
	res := retryRun(t, nil, p, Options{Bail: true})
	if !res.Summary.Bailed || res.Summary.Status != spi.StatusFailed {
		t.Fatalf("bailed=%v status=%s", res.Summary.Bailed, res.Summary.Status)
	}
	assertCaseStatuses(t, res.Suites[0].Cases,
		[]string{"bad", "next"},
		[]spi.Status{spi.StatusFailed, spi.StatusSkipped})
	if res.Suites[0].Cases[0].Steps[0].Attempts != 2 {
		t.Fatalf("attempts %d, want 2", res.Suites[0].Cases[0].Steps[0].Attempts)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/bad"] != 2 || hits["/next"] != 0 {
		t.Fatalf("hits %v", hits)
	}
}

func TestRetrySkipsPluginError(t *testing.T) {
	step := model.Step{
		Name:     "grpc",
		Protocol: "grpc",
		Request:  map[string]any{},
		Retry:    &model.Retry{Count: 5, Until: model.RetryUntilPassed},
	}
	res := retryRun(t, nil, oneStepProject(step), Options{})
	got := res.Suites[0].Cases[0].Steps[0]
	if got.Status != spi.StatusError || got.Attempts != 1 {
		t.Fatalf("status %s attempts %d, want one plugin error", got.Status, got.Attempts)
	}
}

func TestRetrySkipsConfigError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	step := statusStep(srv.URL, 200, &model.Retry{Count: 3, Until: model.RetryUntilPassed}, 0)
	step.Request["bodyFile"] = "no-such-upload.bin"
	res := retryRun(t, nil, oneStepProject(step), Options{})
	got := res.Suites[0].Cases[0].Steps[0]
	if got.Status != spi.StatusError || got.Attempts != 1 {
		t.Fatalf("status %s attempts %d, want one config error", got.Status, got.Attempts)
	}
	if got.Error == nil || got.Error.Code != string(verr.CodeConfig) {
		t.Fatalf("error %#v, want config_error", got.Error)
	}
	if hits.Load() != 0 {
		t.Fatalf("server was hit %d times", hits.Load())
	}
}

func bodyOf(step spi.StepResult) string {
	if step.Response == nil {
		return ""
	}
	return step.Response.Body
}
