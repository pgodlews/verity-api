package load

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestIterationsAreExact(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const vus, iters = 3, 4
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: vus, Iterations: iters, FollowRedirects: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.VUs != vus || rep.Iterations != int64(vus*iters) {
		t.Fatalf("vus=%d iterations=%d, want %d and %d", rep.VUs, rep.Iterations, vus, vus*iters)
	}
	if rep.Summary.Requests != int64(vus*iters) || hits.Load() != int32(vus*iters) {
		t.Fatalf("requests=%d hits=%d, want %d", rep.Summary.Requests, hits.Load(), vus*iters)
	}
	if rep.Summary.AssertionFailures != 0 || rep.Summary.TransportErrors != 0 {
		t.Fatalf("summary %+v", rep.Summary)
	}
}

func TestLoopsCasesInOrder(t *testing.T) {
	var a, b atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			a.Add(1)
		case "/b":
			b.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "load",
		Environments: []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{
				{Name: "a", Steps: []model.Step{getStep("a", srv.URL+"/a")}},
				{Name: "b", Steps: []model.Step{getStep("b", srv.URL+"/b")}},
			},
		}},
	}
	rep, err := Run(context.Background(), bootstrap.Default(), p, Options{VUs: 1, Iterations: 3, FollowRedirects: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Iterations != 3 || a.Load() != 2 || b.Load() != 1 {
		t.Fatalf("iterations=%d a=%d b=%d, want 3, 2, 1", rep.Iterations, a.Load(), b.Load())
	}
}

func TestDurationStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	const window = 400 * time.Millisecond
	start := time.Now()
	rep, err := Run(ctx, bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 2, Duration: window, FollowRedirects: true,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Interrupted {
		t.Fatal("duration did not stop the run before the backstop")
	}
	if elapsed < 350*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("elapsed %s, want about %s", elapsed, window)
	}
	if rep.Iterations < 1 || rep.Summary.Requests < 1 {
		t.Fatalf("iterations=%d requests=%d", rep.Iterations, rep.Summary.Requests)
	}
}

func TestThresholdsPassAndFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(60 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := stepsProject(
		getStep("fast", srv.URL+"/fast"),
		getStep("slow", srv.URL+"/slow"),
	)
	rep, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 2, FollowRedirects: true,
		Thresholds: []string{"p95<5000ms", "fast:p95<5000ms", "slow:p95<20ms", "slow:max<=5s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed {
		t.Fatal("slow step should miss p95<20ms")
	}
	var failed []string
	for _, th := range rep.Thresholds {
		if !th.Passed {
			failed = append(failed, th.Expr)
		}
	}
	if len(failed) != 1 || failed[0] != "slow:p95<20ms" {
		t.Fatalf("failed %v", failed)
	}
	msgs := rep.Failures()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "slow:p95<20ms") {
		t.Fatalf("failures %v", msgs)
	}

	ok, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL+"/fast"), Options{
		VUs: 1, Iterations: 2, FollowRedirects: true,
		Thresholds: []string{"p95<5000ms", "max<=5s", "mean<1s"},
	})
	if err != nil || !ok.Passed {
		t.Fatalf("pass run: err=%v passed=%v failures=%v", err, ok != nil && ok.Passed, ok.Failures())
	}
}

func TestErrorRateAndStatusCodes(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	max := 0.10
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Iterations: 10, FollowRedirects: true, MaxErrorRate: &max,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Requests != 10 || rep.Summary.AssertionFailures != 5 || rep.Summary.TransportErrors != 0 {
		t.Fatalf("summary %+v", rep.Summary)
	}
	if rep.Summary.ErrorRate < 0.49 || rep.Summary.ErrorRate > 0.51 {
		t.Fatalf("error rate %v", rep.Summary.ErrorRate)
	}
	if rep.Passed || rep.ErrorRate == nil || rep.ErrorRate.Passed {
		t.Fatal("10% max should fail a 50% error rate")
	}
	codes := map[int]int64{}
	for _, c := range rep.Summary.StatusCodes {
		codes[c.Code] = c.Count
	}
	if codes[200] != 5 || codes[500] != 5 {
		t.Fatalf("status codes %+v", codes)
	}

	loose := 0.5
	ok, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Iterations: 10, FollowRedirects: true, MaxErrorRate: &loose,
	})
	if err != nil || !ok.Passed {
		t.Fatalf("50%% max should pass: err=%v passed=%v", err, ok != nil && ok.Passed)
	}
}

func TestTransportErrorsCountedSeparately(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	p := stepsProject(model.Step{
		Name: "down", Protocol: "rest",
		Request: map[string]any{"method": "GET", "url": "http://" + addr + "/x"},
	})
	rep, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 1, FollowRedirects: true, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.TransportErrors != 1 || rep.Summary.AssertionFailures != 0 || rep.Summary.Requests != 1 {
		t.Fatalf("summary %+v", rep.Summary)
	}
	if len(rep.Summary.Errors) == 0 {
		t.Fatal("expected an error message")
	}
}

func TestRampUpSpreadsVirtualUsers(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 4, Iterations: 1, RampUp: 400 * time.Millisecond, FollowRedirects: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Iterations != 4 || len(times) != 4 {
		t.Fatalf("iterations=%d hits=%d", rep.Iterations, len(times))
	}
	first, last := times[0], times[0]
	for _, ts := range times[1:] {
		if ts.Before(first) {
			first = ts
		}
		if ts.After(last) {
			last = ts
		}
	}
	gap := last.Sub(first)
	// VU i starts at i * ramp / vus, so the last starts 300ms after the first.
	if gap < 200*time.Millisecond || gap > 800*time.Millisecond {
		t.Fatalf("start spread %s, want about 300ms", gap)
	}
}

func TestThinkTimeBetweenIterations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Iterations: 2, Think: 300 * time.Millisecond, FollowRedirects: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Requests != 2 {
		t.Fatalf("requests %d", rep.Summary.Requests)
	}
	if rep.DurationMs < 250 {
		t.Fatalf("duration %dms, think time was not applied", rep.DurationMs)
	}
}

func TestSetupRunsOnceAndStaysPerVU(t *testing.T) {
	var setupHits, workHits atomic.Int32
	var mu sync.Mutex
	var tickets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/setup":
			setupHits.Add(1)
			fmt.Fprint(w, `{"token":"tok-from-setup"}`)
		case "/issue":
			got := r.Header.Get("X-Ticket")
			mu.Lock()
			tickets = append(tickets, got)
			id := len(tickets)
			mu.Unlock()
			fmt.Fprintf(w, `{"ticket":"%d"}`, id)
		default:
			workHits.Add(1)
			if r.Header.Get("Authorization") != "Bearer tok-from-setup" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	p := &model.Project{
		SchemaVersion: "1.0", Name: "load",
		Environments: []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "api",
			Setup: &model.Hook{Name: "login", Steps: []model.Step{{
				Name: "login", Protocol: "rest",
				Request: map[string]any{"url": srv.URL + "/setup"},
				Transfers: []model.TransferSpec{{
					Name: "token", From: "jsonpath", Expr: "$.token", Scope: model.TransferScopeRun,
				}},
			}}},
			Cases: []model.TestCase{{
				Name: "work",
				Steps: []model.Step{
					{
						Name: "GET widget", Protocol: "rest",
						Request: map[string]any{
							"url":     srv.URL + "/work",
							"headers": map[string]any{"Authorization": "Bearer ${token}"},
						},
						Assertions: []model.Assertion{{Type: "status", Value: 200}},
					},
					{
						Name: "issue", Protocol: "rest",
						Request: map[string]any{
							"url":     srv.URL + "/issue",
							"headers": map[string]any{"X-Ticket": "${ticket}"},
						},
						Transfers: []model.TransferSpec{{
							Name: "ticket", From: "jsonpath", Expr: "$.ticket", Scope: model.TransferScopeRun,
						}},
					},
				},
			}},
		}},
	}
	// Ramp the second user so the first has already captured a ticket.
	rep, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 2, Iterations: 2, RampUp: 400 * time.Millisecond, FollowRedirects: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if setupHits.Load() != 1 {
		t.Fatalf("setup hits %d, want 1", setupHits.Load())
	}
	if workHits.Load() != 4 || rep.Iterations != 4 {
		t.Fatalf("work=%d iterations=%d, want 4 and 4", workHits.Load(), rep.Iterations)
	}
	if rep.Summary.AssertionFailures != 0 || rep.Summary.TransportErrors != 0 {
		t.Fatalf("setup token was not visible: %+v errors=%v", rep.Summary, rep.Summary.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	unset := 0
	for _, tk := range tickets {
		if tk == "" || tk == "${ticket}" {
			unset++
		}
	}
	// Each virtual user sends the placeholder on its first issue, then its own ticket.
	if unset != 2 {
		t.Fatalf("unset tickets %d, headers %v (want one unset per VU)", unset, tickets)
	}
}

func TestCancelWritesPartialReport(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(3 * time.Second):
			cancel()
		}
	}()
	start := time.Now()
	rep, err := Run(ctx, bootstrap.Default(), oneStep(srv.URL+"/block"), Options{
		VUs: 1, Duration: 30 * time.Second, FollowRedirects: true, Timeout: 2 * time.Second,
	})
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	if err != nil {
		t.Fatal(err)
	}
	if rep == nil || !rep.Interrupted {
		t.Fatalf("report %+v", rep)
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"interrupted": true`) {
		t.Fatalf("json %s", buf.String())
	}
}

func TestRemoteTargetRefusedWithoutTraffic(t *testing.T) {
	rt := &rejectRT{}
	p := oneStep("http://203.0.113.10/nope")
	var looked atomic.Int32
	_, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 1, FollowRedirects: true,
		Transport: rt,
		LookupIP: func(string) ([]net.IP, error) {
			looked.Add(1)
			return nil, errors.New("should not be called")
		},
	})
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "allow-remote") {
		t.Fatalf("error %v", err)
	}
	if rt.n.Load() != 0 {
		t.Fatalf("sent %d requests", rt.n.Load())
	}
	if looked.Load() != 0 {
		t.Fatal("resolved an IP literal")
	}
}

func TestSharedTransportReusesConnections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	var dials atomic.Int32
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
	}
	defer tr.CloseIdleConnections()
	const iters = 4
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		// A transport with its own dialer cannot be guarded, so this test opts out.
		VUs: 1, Iterations: iters, FollowRedirects: true, Transport: tr, AllowRemote: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Requests != iters {
		t.Fatalf("requests %d", rep.Summary.Requests)
	}
	if dials.Load() >= int32(iters) {
		t.Fatalf("dials %d, want connection reuse across %d requests", dials.Load(), iters)
	}
	if dials.Load() < 1 {
		t.Fatal("no dial")
	}
}

func TestReportRedactsSecrets(t *testing.T) {
	const secret = "super-secret-token-value"
	t.Setenv("API_TOKEN", secret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q}`, secret)
	}))
	defer srv.Close()
	p := stepsProject(model.Step{
		Name: "GET widget", Protocol: "rest",
		Request: map[string]any{
			"url":     srv.URL + "/secret",
			"headers": map[string]any{"Authorization": "Bearer ${env:API_TOKEN}"},
		},
		Assertions: []model.Assertion{{Type: "jsonpath", Expr: "$.token", Value: "nope"}},
	})
	rep, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 1, FollowRedirects: true,
		Thresholds: []string{"p95<5000ms"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.AssertionFailures != 1 {
		t.Fatalf("summary %+v", rep.Summary)
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	if err := WriteConsole(&console, rep); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(console.String(), secret) {
		t.Fatal("secret leaked into the load report")
	}
	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != "1" || decoded.Project != "load" || len(decoded.Steps) != 1 {
		t.Fatalf("decoded project=%s schema=%s steps=%d", decoded.Project, decoded.SchemaVersion, len(decoded.Steps))
	}
	if decoded.Steps[0].Name != "GET widget" || decoded.Summary.Requests != 1 {
		t.Fatalf("step %+v", decoded.Steps[0])
	}
}

func TestBeforeStartRunsBeforeTraffic(t *testing.T) {
	var started atomic.Bool
	var early atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !started.Load() {
			early.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Iterations: 1, FollowRedirects: true,
		BeforeStart: func(hosts []string) {
			if len(hosts) != 1 {
				t.Errorf("hosts %v", hosts)
			}
			started.Store(true)
		},
	})
	if err != nil || !rep.Passed {
		t.Fatalf("err=%v passed=%v", err, rep != nil && rep.Passed)
	}
	if !started.Load() || early.Load() {
		t.Fatalf("started=%v early=%v", started.Load(), early.Load())
	}
}

func TestRecorderConcurrent(t *testing.T) {
	rec := newRecorder(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rec.record(spi.StepResult{
					Name: "GET widget", Status: spi.StatusPassed, DurationMS: 5,
					Response: &spi.ResponseCapture{StatusCode: 200},
				})
			}
		}()
	}
	wg.Wait()
	summary, steps := rec.snapshot(time.Second)
	if summary.Requests != 800 || len(steps) != 1 || steps[0].Requests != 800 {
		t.Fatalf("summary %+v steps %+v", summary, steps)
	}
}

func TestLoadRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 8, Iterations: 20, FollowRedirects: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Iterations != 160 || rep.Summary.Requests != 160 {
		t.Fatalf("iterations=%d requests=%d", rep.Iterations, rep.Summary.Requests)
	}
}

func TestDialRefusesRedirect(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://203.0.113.10/", http.StatusFound)
	}))
	defer srv.Close()
	_, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Iterations: 3, FollowRedirects: true, Timeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "address") {
		t.Fatalf("error %v", err)
	}
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeConfig {
		t.Fatalf("code %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d, want the redirect source once", hits.Load())
	}
}

func TestDialRefusesRebinding(t *testing.T) {
	var looked atomic.Int32
	_, err := Run(context.Background(), bootstrap.Default(), oneStep("http://app.invalid/"), Options{
		VUs: 1, Iterations: 1, FollowRedirects: true, Timeout: 2 * time.Second,
		LookupIP: func(host string) ([]net.IP, error) {
			if host != "app.invalid" {
				t.Errorf("screen lookup %s", host)
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
		ResolveIP: func(context.Context, string) ([]net.IP, error) {
			looked.Add(1)
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "address") {
		t.Fatalf("error %v", err)
	}
	if looked.Load() < 1 {
		t.Fatal("dial did not resolve")
	}
}

func TestDialRefusesCapturedHost(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"host":"203.0.113.10"}`)
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "capture",
		Environments: []model.Environment{{
			Name: "local", Default: true,
			Variables: map[string]string{"host": "127.0.0.1"},
		}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "c",
				Steps: []model.Step{
					{
						Name: "first", Protocol: "rest",
						Request:    map[string]any{"method": "GET", "url": srv.URL + "/first"},
						Assertions: []model.Assertion{{Type: "status", Value: 200}},
						Transfers: []model.TransferSpec{{
							Name: "host", From: "jsonpath", Expr: "$.host",
						}},
					},
					{
						Name: "second", Protocol: "rest",
						Request: map[string]any{"method": "GET", "url": "http://${host}/next"},
					},
				},
			}},
		}},
	}
	_, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 2, FollowRedirects: true, Timeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "address") {
		t.Fatalf("error %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits %d, want only the first step", hits.Load())
	}
}

func TestDialRefusesOAuthTokenHost(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "oauth",
		Auth: []model.AuthDef{{
			Name: "token", Type: "oauth2",
			Config: map[string]any{
				"tokenUrl":     "http://203.0.113.10/token",
				"clientId":     "client",
				"clientSecret": "secret",
			},
		}},
		Environments: []model.Environment{{Name: "local", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Auth: &model.AuthRef{Use: []string{"token"}},
			Cases: []model.TestCase{{
				Name: "c", Steps: []model.Step{getStep("get", srv.URL)},
			}},
		}},
	}
	_, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 1, FollowRedirects: true, Timeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "address") {
		t.Fatalf("error %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("hits %d, token dial should fail first", hits.Load())
	}
}

func TestDialRefusesPublicProxy(t *testing.T) {
	tr := newPooledTransport(2, 2, guardedDial(nil))
	defer tr.CloseIdleConnections()
	tr.Proxy = func(*http.Request) (*url.URL, error) {
		return url.Parse("http://203.0.113.5:8080")
	}
	client := &http.Client{Transport: tr, Timeout: time.Second}
	_, err := client.Get("http://10.0.0.1/")
	if err == nil || !strings.Contains(err.Error(), "203.0.113.5") || !strings.Contains(err.Error(), "address") {
		t.Fatalf("error %v", err)
	}
}

func TestZeroRequestsFailThresholds(t *testing.T) {
	max := 0.0
	p := stepsProject(model.Step{
		Name: "get", Protocol: "rest", Skip: true,
		Request: map[string]any{"url": "http://127.0.0.1/"},
	})
	rep, err := Run(context.Background(), bootstrap.Default(), p, Options{
		VUs: 1, Iterations: 1,
		Thresholds:   []string{"p95<1ms", "get:p95<1ms"},
		MaxErrorRate: &max,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Passed || rep.Summary.Requests != 0 {
		t.Fatalf("passed=%v requests=%d", rep.Passed, rep.Summary.Requests)
	}
	msgs := strings.Join(rep.Failures(), "\n")
	if !strings.Contains(msgs, "no requests ran") || !strings.Contains(msgs, `step "get" did not run`) {
		t.Fatalf("failures %v", rep.Failures())
	}
}

func TestRampAndThinkCappedByDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	start := time.Now()
	if _, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 2, Duration: 300 * time.Millisecond, RampUp: 30 * time.Second, FollowRedirects: true,
	}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ramp slept %s", elapsed)
	}
	start = time.Now()
	if _, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Duration: 300 * time.Millisecond, Think: 30 * time.Second, FollowRedirects: true,
	}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("think slept %s", elapsed)
	}
}

func TestLatencyKeepsMicroseconds(t *testing.T) {
	rec := newRecorder(nil)
	rec.record(spi.StepResult{
		Name: "get", Status: spi.StatusPassed,
		Duration: 250 * time.Microsecond,
		Response: &spi.ResponseCapture{StatusCode: 200},
	})
	summary, _ := rec.snapshot(time.Second)
	if float64(summary.Latency.MinMs) != 0.25 || float64(summary.Latency.MaxMs) != 0.25 {
		t.Fatalf("latency %+v", summary.Latency)
	}
	b, err := json.Marshal(summary.Latency)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "0.250") {
		t.Fatalf("json %s", b)
	}
}

func TestReportFieldsAlwaysPresent(t *testing.T) {
	rec := newRecorder(nil)
	rec.record(spi.StepResult{Name: "get", Status: spi.StatusPassed, DurationMS: 5})
	summary, steps := rec.snapshot(time.Second)
	rep := &Report{
		Summary: summary,
		Steps:   steps,
		Thresholds: []ThresholdResult{{
			Expr: "p95<1ms", Passed: true, Metric: "p95", ActualMs: 0, LimitMs: 1,
		}},
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `"errors": []`) {
		t.Fatalf("errors omitted: %s", got)
	}
	if !strings.Contains(got, `"actualMs": 0.000`) || !strings.Contains(got, `"limitMs": 1.000`) {
		t.Fatalf("threshold fields: %s", got)
	}
}

func TestFormatLiveAndStart(t *testing.T) {
	if got := FormatLive(4, 12.5, 40, 2); got != "vus=4 rps=12.5 p95=40.0ms errors=2" {
		t.Fatal(got)
	}
	line := FormatStart([]string{"127.0.0.1"}, 3, 0, 30*time.Second, 5*time.Second)
	if !strings.Contains(line, "127.0.0.1") || !strings.Contains(line, "vus=3") || !strings.Contains(line, "duration=30s") || !strings.Contains(line, "ramp-up=5s") {
		t.Fatal(line)
	}
}

func TestOnLiveEmitsProgressSamples(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var (
		mu      sync.Mutex
		samples []LiveStats
	)
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs:             2,
		Duration:        150 * time.Millisecond,
		FollowRedirects: true,
		LiveEvery:       30 * time.Millisecond,
		OnLive: func(st LiveStats) {
			mu.Lock()
			samples = append(samples, st)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Requests < 1 {
		t.Fatalf("requests %d", rep.Summary.Requests)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(samples) == 0 {
		t.Fatal("expected at least one OnLive sample")
	}
	if samples[len(samples)-1].ElapsedMs <= 0 {
		t.Fatalf("elapsedMs %+v", samples[len(samples)-1])
	}
}

type rejectRT struct{ n atomic.Int32 }

func (r *rejectRT) RoundTrip(*http.Request) (*http.Response, error) {
	r.n.Add(1)
	return nil, errors.New("unexpected traffic")
}

func oneStep(url string) *model.Project {
	return stepsProject(getStep("GET widget", url))
}

func stepsProject(steps ...model.Step) *model.Project {
	return &model.Project{
		SchemaVersion: "1.0",
		Name:          "load",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites:        []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: steps}}}},
	}
}

func getStep(name, url string) model.Step {
	return model.Step{
		Name: name, Protocol: "rest",
		Request:    map[string]any{"method": "GET", "url": url},
		Assertions: []model.Assertion{{Type: "status", Value: 200}},
	}
}
