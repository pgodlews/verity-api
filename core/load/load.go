// Package load drives a Verity project as a load test. Each virtual user
// executes cases through core/runner, so transfers, assertions, auth, and
// redaction behave the same way they do in `verity run`. One shared
// http.Transport is used for every virtual user (see Run) so connections
// are reused; a step that brings its own TLS config still gets a private
// transport, cached for the run. Unless AllowRemote is set, every dial
// (the shared pool, a private TLS transport, an OAuth2 token request, a
// redirect, and a proxy) refuses an address outside the private allow-list.
package load

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

const (
	// MaxVUs is the hard cap on --vus.
	MaxVUs = 1000
	// WarnVUs is the concurrency above which the CLI warns. The run still
	// starts.
	WarnVUs = 200
)

// Options configures a load run. Set Iterations or Duration, not both.
// Iterations is the number of case runs each virtual user performs. Duration
// is how long virtual users keep starting new case runs after they are
// released; ramp-up happens inside that window.
type Options struct {
	// Redactor shares resolved secrets with the caller for startup/error output.
	Redactor *secrets.Redactor

	Environment string
	ExtraVars   map[string]string
	Suites      []string
	Cases       []string
	VUs         int
	Iterations  int
	Duration    time.Duration
	RampUp      time.Duration
	Think       time.Duration
	Thresholds  []string
	// MaxErrorRate is a fraction (0.01 = 1%). Nil means the rate is not a
	// pass/fail check. Failures still appear in the report.
	MaxErrorRate       *float64
	AllowRemote        bool
	FollowRedirects    bool
	Timeout            time.Duration
	AllowExternalFiles bool
	// Transport, when non-nil, is the HTTP transport shared by every virtual
	// user. Nil builds one pool sized to VUs. The caller keeps ownership of
	// a transport it passed in; a transport this package builds is closed
	// when Run returns.
	Transport http.RoundTripper
	// LookupIP resolves non-literal hosts while screening targets. Nil uses
	// net.LookupIP. IP literals are never looked up and never dialed.
	LookupIP func(string) ([]net.IP, error)
	// ResolveIP resolves non-literal hosts at dial time. Nil uses the system
	// resolver. It can disagree with LookupIP; the dial is what is enforced.
	ResolveIP func(ctx context.Context, host string) ([]net.IP, error)
	// BeforeStart is called after the target check passes and before setup
	// or any virtual user runs. hosts is the redacted, sorted target list.
	BeforeStart func(hosts []string)
	// Live receives a progress line every LiveEvery while the run is going.
	// Nil disables it.
	Live io.Writer
	// OnLive receives the same sample as a struct every LiveEvery (the
	// desktop app forwards it as an event). It is called from one goroutine,
	// never concurrently with itself, and should return quickly. Nil
	// disables it.
	OnLive    func(LiveStats)
	LiveEvery time.Duration
	// AuthSessions supplies process-only desktop OAuth sessions by auth
	// definition name (see runner.Options). Nil for the CLI.
	AuthSessions map[string]*auth.OAuthSession
}

// LiveStats is one progress sample taken while virtual users run. RPS is
// the rate since the previous sample; P95Ms and Errors are cumulative.
// Errors counts assertion failures, transport errors, and other errors.
type LiveStats struct {
	VUs       int     `json:"vus"`
	RPS       float64 `json:"rps"`
	P95Ms     float64 `json:"p95Ms"`
	Errors    int64   `json:"errors"`
	Requests  int64   `json:"requests"`
	ElapsedMs int64   `json:"elapsedMs"`
}

// VUWarning is the stderr warning for a VU count above WarnVUs, or "" when
// no warning is needed.
func VUWarning(vus int) string {
	if vus > WarnVUs && vus <= MaxVUs {
		return fmt.Sprintf("warning: --vus=%d is above %d; a load run can open many connections", vus, WarnVUs)
	}
	return ""
}

// Run executes the load test. A cancelled context stops virtual users, waits
// for the case that is already in flight, and still returns a report with
// Interrupted set. The error is non-nil only when the run could not start
// (bad options, remote target refused, no matching case). Threshold misses
// are reported on the result and do not return an error.
func Run(ctx context.Context, reg *registry.Registry, project *model.Project, opts Options) (*Report, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	thresholds, err := parseThresholds(opts.Thresholds)
	if err != nil {
		return nil, err
	}
	var dial func(context.Context, string, string) (net.Conn, error)
	if !opts.AllowRemote {
		dial = guardedDial(opts.ResolveIP)
	}
	exec, err := prepare(reg, project, opts, dial)
	if err != nil {
		return nil, err
	}
	defer exec.CloseIdleTransports()
	hosts, err := screenTargets(exec, opts.AllowRemote, opts.LookupIP)
	if err != nil {
		return nil, err
	}
	if opts.BeforeStart != nil {
		shown := make([]string, len(hosts))
		for i, host := range hosts {
			shown[i] = exec.Redactor().Redact(host)
		}
		opts.BeforeStart(shown)
	}

	transport := opts.Transport
	var owned *http.Transport
	if transport == nil {
		owned = newSharedTransport(opts.VUs, dial)
		transport = owned
	} else if dial != nil {
		// The private-address guard must see every connection's real
		// destination: install it on the caller's transport and turn off
		// its proxy, or refuse a transport it cannot guard.
		t, ok := transport.(*http.Transport)
		if !ok || t.DialContext != nil || t.DialTLSContext != nil {
			return nil, verr.New(verr.CodeConfig,
				"load: a custom Transport with its own dialer cannot be guarded; pass --allow-remote or leave Transport nil")
		}
		t.DialContext = dial
		t.Proxy = nil
	}
	if owned != nil {
		defer owned.CloseIdleConnections()
	}
	// OAuth token requests use the runner's transports, which carry the same
	// dial guard (runner.Options.DialContext) and a step's mTLS config.

	rep := &Report{
		SchemaVersion:   "1",
		Project:         exec.ProjectName(),
		Environment:     exec.EnvironmentName(),
		StartedAt:       time.Now(),
		VUs:             opts.VUs,
		RampUpMs:        opts.RampUp.Milliseconds(),
		ThinkMs:         opts.Think.Milliseconds(),
		ConfiguredIters: opts.Iterations,
		Targets:         hosts,
		red:             exec.Redactor(),
	}
	if opts.Duration > 0 {
		rep.ConfiguredMs = opts.Duration.Milliseconds()
	}

	setups := exec.RunSetups(ctx)
	for _, s := range setups {
		step := SetupStep{Suite: s.Suite, Name: s.Result.Name, Status: string(s.Result.Status)}
		if s.Result.Status == spi.StatusFailed || s.Result.Status == spi.StatusError {
			if !rep.SetupFailed {
				rep.SetupErrorCode = setupErrorCode(s.Result)
			}
			rep.SetupFailed = true
			step.Error = setupError(exec, s.Result)
		}
		if s.Result.Status == spi.StatusAborted || ctx.Err() != nil {
			rep.Interrupted = true
		}
		rep.Setup = append(rep.Setup, step)
	}
	// Teardown runs once after the load, and also after a failed setup
	// (cleanup of what setup did); no virtual user starts after a failure.
	teardown := func() {
		for _, s := range exec.RunTeardowns(ctx) {
			step := SetupStep{Suite: s.Suite, Name: s.Result.Name, Status: string(s.Result.Status)}
			if s.Result.Status == spi.StatusFailed || s.Result.Status == spi.StatusError {
				rep.TeardownFailed = true
				step.Error = setupError(exec, s.Result)
			}
			rep.Teardown = append(rep.Teardown, step)
		}
	}
	if rep.SetupFailed || rep.Interrupted {
		teardown()
		return finish(rep, nil, nil, thresholds, opts, time.Now()), nil
	}

	cases := exec.Cases()
	baseline := exec.RunScoped()
	rec := newRecorder(exec.Redactor())
	var iterations atomic.Int64
	var active atomic.Int32

	vuStart := time.Now()
	var deadline time.Time
	if opts.Duration > 0 {
		deadline = vuStart.Add(opts.Duration)
	}

	liveStop := make(chan struct{})
	var liveWG sync.WaitGroup
	if opts.Live != nil || opts.OnLive != nil {
		every := opts.LiveEvery
		if every <= 0 {
			every = time.Second
		}
		emit := func(s LiveStats) {
			if opts.Live != nil {
				fmt.Fprintf(opts.Live, "\r%s", FormatLive(s.VUs, s.RPS, s.P95Ms, s.Errors))
			}
			if opts.OnLive != nil {
				opts.OnLive(s)
			}
		}
		liveWG.Add(1)
		go func() {
			defer liveWG.Done()
			runLive(liveStop, emit, every, &active, rec, vuStart)
		}()
	}

	vuCtx, vuCancel := context.WithCancel(ctx)
	defer vuCancel()
	var refuseOnce sync.Once
	var refused error
	refuse := func(msg string) {
		refuseOnce.Do(func() {
			refused = configRefusal(msg)
			vuCancel()
		})
	}

	var wg sync.WaitGroup
	for i := 0; i < opts.VUs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runVU(vuCtx, exec, cases, baseline, transport, rec, &iterations, &active, vuConfig{
				index:      i,
				vus:        opts.VUs,
				iterations: opts.Iterations,
				deadline:   deadline,
				ramp:       opts.RampUp,
				think:      opts.Think,
				duration:   opts.Duration > 0,
			}, refuse)
		}(i)
	}
	wg.Wait()
	vuEnd := time.Now()
	close(liveStop)
	liveWG.Wait()
	if opts.Live != nil {
		fmt.Fprintln(opts.Live)
	}
	if refused != nil && ctx.Err() == nil {
		teardown()
		return nil, refused
	}

	if ctx.Err() != nil {
		rep.Interrupted = true
	}
	teardown()
	rep.Iterations = iterations.Load()
	return finish(rep, rec, &vuWindow{vuStart, vuEnd}, thresholds, opts, vuEnd), nil
}

type vuWindow struct {
	start time.Time
	end   time.Time
}

type vuConfig struct {
	index      int
	vus        int
	iterations int
	deadline   time.Time
	ramp       time.Duration
	think      time.Duration
	duration   bool
}

func runVU(ctx context.Context, exec *runner.CaseExec, cases []runner.CaseBinding, baseline map[string]string, transport http.RoundTripper, rec *recorder, iterations *atomic.Int64, active *atomic.Int32, cfg vuConfig, refuse func(string)) {
	if d := rampDelay(cfg.index, cfg.vus, cfg.ramp); d > 0 {
		if sleepBounded(ctx, d, cfg.deadline, cfg.duration) != nil {
			return
		}
	}
	if cfg.duration && !time.Now().Before(cfg.deadline) {
		return
	}
	active.Add(1)
	defer active.Add(-1)

	vuRun := copyMap(baseline)
	for n := 0; ; n++ {
		if ctx.Err() != nil {
			return
		}
		if !cfg.duration && n >= cfg.iterations {
			return
		}
		if cfg.duration && !time.Now().Before(cfg.deadline) {
			return
		}
		b := cases[n%len(cases)]
		resolver := exec.NewResolver(b.Suite, b.Case, vuRun)
		results, err := exec.RunCase(ctx, runner.CaseRequest{
			Suite:     b.Suite,
			Case:      b.Case,
			Resolver:  resolver,
			Transport: transport,
			OnRunVar: func(name, value string) {
				vuRun[name] = value
			},
		})
		if err != nil {
			rec.record(spi.StepResult{
				Name:   b.Case.Name,
				Status: spi.StatusError,
				Error:  &spi.ErrorInfo{Code: string(verr.CodeInternal), Message: err.Error()},
			})
		}
		for _, cr := range results {
			for _, st := range cr.Steps {
				rec.record(st)
				if msg := refusalText(stepError(st)); msg != "" {
					refuse(msg)
					return
				}
			}
		}
		iterations.Add(1)
		more := false
		if cfg.duration {
			more = time.Now().Before(cfg.deadline) && ctx.Err() == nil
		} else {
			more = n+1 < cfg.iterations && ctx.Err() == nil
		}
		if more {
			if sleepBounded(ctx, cfg.think, cfg.deadline, cfg.duration) != nil {
				return
			}
		}
	}
}

func stepError(st spi.StepResult) string {
	if st.Error == nil {
		return ""
	}
	return st.Error.Message
}

func finish(rep *Report, rec *recorder, window *vuWindow, thresholds []Threshold, opts Options, finished time.Time) *Report {
	rep.FinishedAt = finished
	elapsed := time.Duration(0)
	if window != nil {
		elapsed = window.end.Sub(window.start)
	}
	rep.DurationMs = elapsed.Milliseconds()
	if rec != nil {
		rep.Summary, rep.Steps = rec.snapshot(elapsed)
	} else {
		rep.Summary.StatusCodes = []StatusCount{}
		rep.Steps = []Metrics{}
	}
	if rep.Summary.StatusCodes == nil {
		rep.Summary.StatusCodes = []StatusCount{}
	}
	if rep.Summary.Errors == nil {
		rep.Summary.Errors = []string{}
	}
	if rep.Steps == nil {
		rep.Steps = []Metrics{}
	}
	for i := range rep.Steps {
		if rep.Steps[i].Errors == nil {
			rep.Steps[i].Errors = []string{}
		}
	}
	if rep.Targets == nil {
		rep.Targets = []string{}
	}
	if rep.Thresholds == nil {
		rep.Thresholds = evaluate(thresholds, rep.Summary, rep.Steps)
	}
	if opts.MaxErrorRate != nil {
		actual := rep.Summary.ErrorRate
		er := &ErrorRateResult{Max: *opts.MaxErrorRate, Actual: actual, Passed: actual <= *opts.MaxErrorRate}
		if rep.Summary.Requests == 0 {
			er.Passed = false
			er.Message = "no requests ran"
		}
		rep.ErrorRate = er
	}
	rep.Passed = !rep.Interrupted && !rep.SetupFailed && !rep.TeardownFailed
	for _, th := range rep.Thresholds {
		if !th.Passed {
			rep.Passed = false
		}
	}
	if rep.ErrorRate != nil && !rep.ErrorRate.Passed {
		rep.Passed = false
	}
	redactReport(rep)
	return rep
}

func redactReport(rep *Report) {
	if rep == nil || rep.red == nil {
		return
	}
	red := rep.red
	rep.Project = red.Redact(rep.Project)
	rep.Environment = red.Redact(rep.Environment)
	for i := range rep.Targets {
		rep.Targets[i] = red.Redact(rep.Targets[i])
	}
	for _, hooks := range [][]SetupStep{rep.Setup, rep.Teardown} {
		for i := range hooks {
			hooks[i].Suite = red.Redact(hooks[i].Suite)
			hooks[i].Name = red.Redact(hooks[i].Name)
			hooks[i].Error = red.Redact(hooks[i].Error)
		}
	}
	for i := range rep.Steps {
		rep.Steps[i].Name = red.Redact(rep.Steps[i].Name)
	}
	for i := range rep.Thresholds {
		rep.Thresholds[i].Expr = red.Redact(rep.Thresholds[i].Expr)
		rep.Thresholds[i].Step = red.Redact(rep.Thresholds[i].Step)
		rep.Thresholds[i].Message = red.Redact(rep.Thresholds[i].Message)
	}
}

func evaluate(ths []Threshold, summary Metrics, steps []Metrics) []ThresholdResult {
	byName := map[string]Metrics{}
	for _, s := range steps {
		byName[s.Name] = s
	}
	out := make([]ThresholdResult, 0, len(ths))
	for _, th := range ths {
		m := summary
		res := ThresholdResult{Expr: th.Expr, Step: th.Step, Metric: th.Metric, LimitMs: millis(th.LimitMs)}
		if th.Step != "" {
			var ok bool
			m, ok = byName[th.Step]
			if !ok || m.Requests == 0 {
				res.Message = fmt.Sprintf("step %q did not run", th.Step)
				out = append(out, res)
				continue
			}
		} else if m.Requests == 0 {
			res.Message = "no requests ran"
			out = append(out, res)
			continue
		}
		actual := latencyValue(m.Latency, th.Metric)
		res.ActualMs = millis(actual)
		res.LimitMs = millis(th.LimitMs)
		if th.Op == "<=" {
			res.Passed = actual <= th.LimitMs
		} else {
			res.Passed = actual < th.LimitMs
		}
		if !res.Passed {
			res.Message = fmt.Sprintf("%s failed: %s=%.2fms limit %s %.2fms", th.Expr, th.Metric, actual, th.Op, th.LimitMs)
		}
		out = append(out, res)
	}
	return out
}

// setupErrorCode is the verr code of a failed hook step: the step's error
// code, or assertion_failed when an assertion failed.
func setupErrorCode(cr spi.CaseResult) string {
	for _, st := range cr.Steps {
		if st.Error != nil && st.Error.Code != "" {
			return st.Error.Code
		}
	}
	return string(verr.CodeAssertion)
}

func setupError(exec *runner.CaseExec, cr spi.CaseResult) string {
	red := exec.Redactor()
	for _, st := range cr.Steps {
		if st.Error != nil && st.Error.Message != "" {
			return red.Redact(st.Error.Message)
		}
		for _, a := range st.Assertions {
			if !a.Passed && a.Message != "" {
				return red.Redact(a.Message)
			}
		}
	}
	return string(cr.Status)
}

func validateOptions(opts Options) error {
	if opts.VUs < 1 {
		return verr.Newf(verr.CodeConfig, "vus must be at least 1, got %d", opts.VUs)
	}
	if opts.VUs > MaxVUs {
		return verr.Newf(verr.CodeConfig, "vus %d exceeds the cap of %d", opts.VUs, MaxVUs)
	}
	if opts.Iterations < 0 {
		return verr.Newf(verr.CodeConfig, "iterations must be >= 0, got %d", opts.Iterations)
	}
	if opts.Duration < 0 || opts.RampUp < 0 || opts.Think < 0 || opts.Timeout < 0 {
		return verr.New(verr.CodeConfig, "duration, ramp-up, think, and timeout must be >= 0")
	}
	hasIter := opts.Iterations > 0
	hasDur := opts.Duration > 0
	if hasIter == hasDur {
		return verr.New(verr.CodeConfig, "set exactly one of iterations or duration")
	}
	if opts.MaxErrorRate != nil && (*opts.MaxErrorRate < 0 || *opts.MaxErrorRate > 1) {
		return verr.New(verr.CodeConfig, "max error rate must be from 0 to 1")
	}
	return nil
}

func parseThresholds(exprs []string) ([]Threshold, error) {
	out := make([]Threshold, 0, len(exprs))
	for _, e := range exprs {
		th, err := ParseThreshold(e)
		if err != nil {
			return nil, err
		}
		out = append(out, th)
	}
	return out, nil
}

func newSharedTransport(vus int, dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	if vus < 2 {
		vus = 2
	}
	return newPooledTransport(vus*2, vus, dial)
}

func newPooledTransport(maxIdle, perHost int, dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdle,
		MaxIdleConnsPerHost:   perHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableKeepAlives:     false,
	}
	if dial != nil {
		// With the private-address guard active, never send requests to an
		// HTTP(S)_PROXY: the guard would only see the proxy's address, and the
		// proxy could reach any public host (a redirect or captured host).
		t.DialContext = dial
		t.Proxy = nil
	}
	return t
}

func rampDelay(index, vus int, ramp time.Duration) time.Duration {
	if vus <= 1 || ramp <= 0 || index <= 0 {
		return 0
	}
	return time.Duration(int64(ramp) * int64(index) / int64(vus))
}

// sleepBounded waits for d, but not past deadline when limited is set, and
// not past ctx cancellation.
func sleepBounded(ctx context.Context, d time.Duration, deadline time.Time, limited bool) error {
	if d <= 0 {
		return nil
	}
	if limited {
		remain := time.Until(deadline)
		if remain <= 0 {
			return nil
		}
		if d > remain {
			d = remain
		}
	}
	return sleepCtx(ctx, d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func runLive(stop <-chan struct{}, emit func(LiveStats), every time.Duration, active *atomic.Int32, rec *recorder, started time.Time) {
	t := time.NewTicker(every)
	defer t.Stop()
	var prevReq int64
	prev := started
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			summary, _ := rec.snapshot(now.Sub(started))
			dt := now.Sub(prev).Seconds()
			rps := 0.0
			if dt > 0 {
				rps = float64(summary.Requests-prevReq) / dt
			}
			prevReq = summary.Requests
			prev = now
			emit(LiveStats{
				VUs:       int(active.Load()),
				RPS:       rps,
				P95Ms:     float64(summary.Latency.P95Ms),
				Errors:    summary.failures(),
				Requests:  summary.Requests,
				ElapsedMs: now.Sub(started).Milliseconds(),
			})
		}
	}
}

// prepare builds the runner state a load run (or a target preview) uses.
// dial nil keeps the default dialer (AllowRemote, or a preview that sends
// nothing).
func prepare(reg *registry.Registry, project *model.Project, opts Options, dial func(context.Context, string, string) (net.Conn, error)) (*runner.CaseExec, error) {
	return runner.New(reg).Prepare(project, runner.Options{
		AuthSessions:       opts.AuthSessions,
		Environment:        opts.Environment,
		Redactor:           opts.Redactor,
		ExtraVars:          opts.ExtraVars,
		Suites:             opts.Suites,
		Cases:              opts.Cases,
		FollowRedirects:    opts.FollowRedirects,
		Timeout:            opts.Timeout,
		AllowExternalFiles: opts.AllowExternalFiles,
		DialContext:        dial,
		LeanCaptures:       true,
	})
}
