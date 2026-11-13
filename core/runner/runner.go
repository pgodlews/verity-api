// Package runner executes a Verity project: it walks suites → cases → steps,
// resolves variables and auth across scopes, drives protocol plugins, evaluates
// assertions, performs property transfers and data-driven feeds, and produces a
// single structured spi.RunResult that every reporter consumes.
//
// Concurrency: cases within a suite run on a bounded goroutine pool sized by the
// effective parallelism. Steps within a case run sequentially because they may
// depend on each other via property transfers. Options.OnEvent is called from
// those case goroutines and must be safe for concurrent use.
package runner

import (
	"context"
	"net"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Options configures a run.
type Options struct {
	// Redactor, when provided, also receives secrets resolved by this run.
	Redactor *secrets.Redactor
	// AuthSessions supplies process-only GUI sessions by auth definition name.
	AuthSessions map[string]*auth.OAuthSession
	// Environment selects the project environment by name ("" = default).
	Environment string
	// Parallel sets the default per-suite case parallelism (>=1). 0/1 = serial.
	Parallel int
	// Timeout is the per-attempt HTTP timeout used when a step does not set
	// timeoutMs. 0 uses a 30s default. A step's timeoutMs replaces it for
	// every attempt of that step; each attempt gets a fresh budget.
	Timeout time.Duration
	// ExtraVars are CLI-supplied variable overrides. Precedence (lowest to
	// highest): environment < run-scoped captures from earlier cases < suite
	// vars < case vars < feed row < ExtraVars < step vars/property transfers
	// (see docs/project-format.md).
	ExtraVars map[string]string
	// FollowRedirects controls whether HTTP clients follow redirects.
	FollowRedirects bool
	// Suites and Cases restrict the run to matching suite / case names. Each
	// entry is an exact name or a path.Match glob ("smoke*"); empty means all.
	// Non-matching suites and cases are left out of the result entirely.
	Suites []string
	Cases  []string
	// UntilStep, when > 0, runs only the first UntilStep steps of each case.
	// Earlier steps still run, so their transfers feed the last one (this is
	// how the GUI "sends" a single request).
	UntilStep int
	// Bail stops scheduling new cases after the first case that fails or
	// errors. Cases already in flight run to completion. Cases that never
	// started are reported as skipped, in project order, so the result stays
	// complete. Hooks of a suite that had not started are skipped too;
	// teardown of a suite that did start still runs. Summary.Bailed is set
	// when a case trips this.
	Bail bool
	// AllowExternalFiles lets uploads, secret files, mTLS files, schema files,
	// and feeds be read from outside the project directory (off by default;
	// see core/fsutil).
	AllowExternalFiles bool
	// DialContext, when set, dials every HTTP connection this run opens
	// (HTTP(S)_PROXY is then ignored so the dialer sees each destination),
	// including private TLS transports. Nil keeps the transport default.
	// Load testing sets this so a redirect, a proxy, or a captured host
	// cannot leave the private-address allow-list.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
	// OnEvent, when non-nil, receives progress while the run is in flight.
	// Events are case_started (a case iteration begins), step_done (the
	// redacted StepResult that will appear in the final report), and
	// case_done. Nil for the CLI. The callback runs on the case goroutines,
	// so several calls may be in flight together; it must be concurrency-safe
	// and should return quickly. The Step pointer is a copy and may be retained.
	OnEvent func(spi.RunEvent)
	// LeanCaptures skips the request capture and keeps only the status and
	// duration of each response (no headers or body), so captures and their
	// redaction do not cost CPU per request. Load testing sets it; results
	// built with it are not meant for reports.
	LeanCaptures bool
}

// teardownTimeout is the grace budget for teardown of a suite that already
// started when the run has been cancelled. It is a fresh context so a
// cancelled run still gets to clean up. A run that was not cancelled runs
// teardown under the run context, with the usual step timeouts. It is a
// variable so tests can shorten it.
var teardownTimeout = 10 * time.Second

// matchName reports whether name matches any pattern (exact or glob). An
// empty pattern list matches everything.
func matchName(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == name {
			return true
		}
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// checkFilters rejects malformed globs and a negative UntilStep.
func checkFilters(opts Options) error {
	if opts.UntilStep < 0 {
		return verr.Newf(verr.CodeConfig, "until-step must be 0 (all steps) or positive, got %d", opts.UntilStep)
	}
	for _, p := range append(append([]string{}, opts.Suites...), opts.Cases...) {
		if _, err := path.Match(p, ""); err != nil {
			return verr.Newf(verr.CodeConfig, "invalid filter pattern %q: %v", p, err)
		}
	}
	return nil
}

// filterProject returns a shallow copy of project holding only the suites,
// cases and steps selected by opts. The input project is not modified.
func filterProject(project *model.Project, opts Options) *model.Project {
	if len(opts.Suites) == 0 && len(opts.Cases) == 0 && opts.UntilStep <= 0 {
		return project
	}
	out := *project
	out.Suites = nil
	for _, s := range project.Suites {
		if !matchName(opts.Suites, s.Name) {
			continue
		}
		var cases []model.TestCase
		for _, c := range s.Cases {
			if !matchName(opts.Cases, c.Name) {
				continue
			}
			if opts.UntilStep > 0 && opts.UntilStep < len(c.Steps) {
				c.Steps = c.Steps[:opts.UntilStep]
			}
			cases = append(cases, c)
		}
		if len(cases) > 0 {
			s.Cases = cases
			out.Suites = append(out.Suites, s)
		}
	}
	return &out
}

// Runner executes projects against a registry of extensions.
type Runner struct {
	reg *registry.Registry
}

// New returns a Runner backed by the given extension registry.
func New(reg *registry.Registry) *Runner {
	return &Runner{reg: reg}
}

// Run executes the project and returns the structured result. The returned
// error is non-nil only for setup failures (e.g. bad environment); assertion
// and transport failures are recorded in the result and reflected in
// result.Summary.Status.
func (r *Runner) Run(ctx context.Context, project *model.Project, opts Options) (*spi.RunResult, error) {
	runCtx, project, err := r.prepare(project, opts)
	if err != nil {
		return nil, err
	}
	defer runCtx.tlsCache.close()
	result := &spi.RunResult{
		Project:     project.Name,
		Environment: runCtx.env.Name,
		StartedAt:   time.Now(),
		Redactor:    runCtx.red,
	}

	for i := range project.Suites {
		if ctx.Err() != nil {
			// A suite that never started is still reported, in project order,
			// the same way --bail keeps skipped cases.
			result.Suites = append(result.Suites, runCtx.abortedSuite(&project.Suites[i]))
			continue
		}
		sr := runCtx.runSuite(ctx, &project.Suites[i])
		result.Suites = append(result.Suites, sr)
	}

	result.FinishedAt = time.Now()
	result.DurationMS = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	result.Summary = summarize(result)
	if runCtx.bailed.Load() {
		result.Summary.Bailed = true
	}
	if ctx.Err() != nil {
		result.Summary.Aborted = true
	}
	return result, nil
}

// prepare checks filters, selects the environment, and builds the shared
// execution state. It does not run any step. Both Run and CaseExec start here
// so a load test executes cases through the same variable, auth, and redaction
// path as a functional run.
func (r *Runner) prepare(project *model.Project, opts Options) (*execContext, *model.Project, error) {
	if err := checkFilters(opts); err != nil {
		return nil, nil, err
	}
	project = filterProject(project, opts)
	if (len(opts.Suites) > 0 || len(opts.Cases) > 0) && len(project.Suites) == 0 {
		// An empty selection is almost always a typo; never report it as a pass.
		return nil, nil, verr.New(verr.CodeNotFound, "no suite/case matches the --suite/--case filters")
	}
	env, err := project.SelectEnvironment(opts.Environment)
	if err != nil {
		return nil, nil, err
	}

	// Base variables: environment vars only (lowest precedence). CLI
	// ExtraVars are merged last, after suite/case/feed vars, in
	// runCaseIteration so they win regardless of scope — see the precedence
	// table on Options.ExtraVars.
	baseVars := map[string]string{}
	for k, v := range env.Variables {
		baseVars[k] = v
	}

	red := opts.Redactor
	if red == nil {
		red = secrets.NewRedactor()
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	runCtx := &execContext{
		runner:       r,
		project:      project,
		env:          env,
		baseVars:     baseVars,
		red:          red,
		opts:         opts,
		timeout:      timeout,
		authResolver: auth.NewResolver(r.reg, project, red, secrets.NewResolver(mergeVars(baseVars, opts.ExtraVars)).WithFiles(project.SourceDir, opts.AllowExternalFiles), project.SourceDir, auth.ResolverOptions{Sessions: opts.AuthSessions, AllowExternalFiles: opts.AllowExternalFiles}),
		tlsCache:     &transportCache{},
		gql:          graphql.NewCache(),
	}
	return runCtx, project, nil
}

// execContext carries shared run state through the suite/case/step walk.
type execContext struct {
	runner       *Runner
	project      *model.Project
	env          model.Environment
	baseVars     map[string]string
	red          *secrets.Redactor
	opts         Options
	timeout      time.Duration
	authResolver *auth.Resolver
	// tlsCache reuses private HTTP transports that carry a TLS config.
	tlsCache *transportCache
	// gql caches introspection results for this run, once per URL and auth identity.
	gql *graphql.Cache
	// bailed is set when Options.Bail is on and a case has failed or errored.
	// Parallel cases read it, so it is atomic. Do not copy execContext.
	bailed atomic.Bool
	// runVars holds values captured by transfers with scope "run". A case
	// sees the values captured before it started (see runCaseIteration).
	runVarsMu sync.RWMutex
	runVars   map[string]string
}

// setRunVar records a run-scoped capture for cases that start later.
func (e *execContext) setRunVar(name, value string) {
	e.runVarsMu.Lock()
	defer e.runVarsMu.Unlock()
	if e.runVars == nil {
		e.runVars = map[string]string{}
	}
	e.runVars[name] = value
}

// runVarsSnapshot copies the run-scoped captures made so far.
func (e *execContext) runVarsSnapshot() map[string]string {
	e.runVarsMu.RLock()
	defer e.runVarsMu.RUnlock()
	return mergeVars(e.runVars)
}

func (e *execContext) runSuite(ctx context.Context, suite *model.Suite) spi.SuiteResult {
	if ctx.Err() != nil {
		return e.abortedSuite(suite)
	}
	start := time.Now()
	sr := spi.SuiteResult{Name: suite.Name}
	// A suite reached only after bail tripped never started, so its hooks are
	// skipped. A suite that did start still runs teardown after a case trips
	// bail, as cleanup of work already done. After cancellation teardown uses a
	// short fresh context so a cancelled run does not skip that cleanup.
	suiteStarted := !e.bailTripped()

	// Setup hook (sequential, before cases). A failed setup step skips the
	// suite's cases (their preconditions are missing); teardown still runs.
	setupFailed := ""
	if suite.Setup != nil {
		hookVars := e.hookResolver(suite)
		for i := range suite.Setup.Steps {
			name := "setup:" + suite.Setup.Name
			step := suite.Setup.Steps[i]
			if ctx.Err() != nil {
				sr.Cases = append(sr.Cases, e.abortedHook(suite.Name, name, step))
				continue
			}
			if e.bailTripped() {
				sr.Cases = append(sr.Cases, e.skippedCase(suite.Name, name, -1))
				continue
			}
			cr := e.runHookCase(ctx, suite, name, step, hookVars)
			sr.Cases = append(sr.Cases, cr)
			e.noteBail(cr.Status)
			if setupFailed == "" && (cr.Status == spi.StatusFailed || cr.Status == spi.StatusError) {
				setupFailed = step.Name
			}
		}
	}

	parallel := suite.Parallel
	if parallel == 0 {
		parallel = e.opts.Parallel
	}
	if parallel < 1 {
		parallel = 1
	}

	perCase := make([][]spi.CaseResult, len(suite.Cases))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for ci := range suite.Cases {
		c := &suite.Cases[ci]
		if ctx.Err() != nil {
			perCase[ci] = []spi.CaseResult{e.abortedCase(suite.Name, c, -1)}
			continue
		}
		if e.bailTripped() {
			perCase[ci] = []spi.CaseResult{e.skippedCase(suite.Name, c.Name, -1)}
			continue
		}
		if setupFailed != "" {
			cr := e.skippedCase(suite.Name, c.Name, -1)
			cr.Steps = []spi.StepResult{{
				Name:   "setup",
				Status: spi.StatusSkipped,
				Error: e.errInfo(verr.Newf(verr.CodeSetupFailed,
					"suite setup step %q failed; case not run", setupFailed)),
			}}
			perCase[ci] = []spi.CaseResult{cr}
			continue
		}
		ci := ci
		wg.Add(1)
		sem <- struct{}{}
		// A peer may have failed, or the run may have been cancelled, while
		// this case waited for a slot. Recheck before launching.
		if ctx.Err() != nil || e.bailTripped() {
			if ctx.Err() != nil {
				perCase[ci] = []spi.CaseResult{e.abortedCase(suite.Name, c, -1)}
			} else {
				perCase[ci] = []spi.CaseResult{e.skippedCase(suite.Name, c.Name, -1)}
			}
			<-sem
			wg.Done()
			continue
		}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				perCase[ci] = []spi.CaseResult{e.abortedCase(suite.Name, c, -1)}
				return
			}
			if e.bailTripped() {
				perCase[ci] = []spi.CaseResult{e.skippedCase(suite.Name, c.Name, -1)}
				return
			}
			results := e.runCase(ctx, suite, c)
			perCase[ci] = results
			e.noteCaseBail(results)
		}()
	}
	wg.Wait()
	for _, crs := range perCase {
		sr.Cases = append(sr.Cases, crs...)
	}

	// Teardown hook (sequential, after cases). Steps run under the run
	// context while it is live, so --timeout, timeoutMs, and retries apply
	// and Ctrl-C interrupts them. Once the run is cancelled, a suite that
	// started gets one detached teardownTimeout grace context for its
	// remaining teardown steps so cancellation does not skip cleanup.
	if suite.Teardown != nil {
		var grace context.Context
		var cancelGrace context.CancelFunc = func() {}
		defer func() { cancelGrace() }()
		teardownCtx := func() context.Context {
			if ctx.Err() == nil {
				return ctx
			}
			if grace == nil {
				grace, cancelGrace = context.WithTimeout(context.WithoutCancel(ctx), teardownTimeout)
			}
			return grace
		}
		teardownVars := e.hookResolver(suite)
		for i := range suite.Teardown.Steps {
			name := "teardown:" + suite.Teardown.Name
			step := suite.Teardown.Steps[i]
			if !suiteStarted {
				if ctx.Err() != nil {
					sr.Cases = append(sr.Cases, e.abortedHook(suite.Name, name, step))
				} else {
					sr.Cases = append(sr.Cases, e.skippedCase(suite.Name, name, -1))
				}
				continue
			}
			cr := e.runHookCase(teardownCtx(), suite, name, step, teardownVars)
			sr.Cases = append(sr.Cases, cr)
			e.noteBail(cr.Status)
		}
	}

	sr.DurationMS = time.Since(start).Milliseconds()
	sr.Status = rollupCases(sr.Cases)
	return sr
}

// skippedCase is a case that was not executed because --bail tripped.
// It carries no steps, matching the historical bail report. iteration is -1
// except for a data-feed row that was not started.
func (e *execContext) skippedCase(suiteName, name string, iteration int) spi.CaseResult {
	cr := spi.CaseResult{Name: name, Status: spi.StatusSkipped, Iteration: iteration}
	e.emit(spi.RunEvent{
		Kind: spi.EventCaseDone, Suite: suiteName, Case: name,
		Iteration: iteration, Status: spi.StatusSkipped,
	})
	return cr
}

// abortedStepResult is a step that never started. An explicit skip stays
// skipped: the project asked for that, cancellation did not.
func abortedStepResult(step *model.Step) spi.StepResult {
	status := spi.StatusAborted
	if step.Skip {
		status = spi.StatusSkipped
	}
	return spi.StepResult{Name: step.Name, Protocol: step.Protocol, Status: status}
}

// abortedCase reports a case that never started, including each of its steps,
// and emits step_done / case_done so a live view fills in.
func (e *execContext) abortedCase(suiteName string, c *model.TestCase, iteration int) spi.CaseResult {
	steps := make([]spi.StepResult, len(c.Steps))
	for i := range c.Steps {
		steps[i] = abortedStepResult(&c.Steps[i])
		e.emitStep(suiteName, c.Name, iteration, i, steps[i])
	}
	cr := spi.CaseResult{
		Name: c.Name, Status: rollupSteps(steps), Iteration: iteration, Steps: steps,
	}
	if len(steps) == 0 {
		cr.Status = spi.StatusAborted
	}
	e.emit(spi.RunEvent{
		Kind: spi.EventCaseDone, Suite: suiteName, Case: c.Name,
		Iteration: iteration, Status: cr.Status,
	})
	return cr
}

func (e *execContext) abortedHook(suiteName, name string, step model.Step) spi.CaseResult {
	return e.abortedCase(suiteName, &model.TestCase{Name: name, Steps: []model.Step{step}}, -1)
}

// abortedSuite reports a suite that never started. Hooks are not executed.
func (e *execContext) abortedSuite(suite *model.Suite) spi.SuiteResult {
	sr := spi.SuiteResult{Name: suite.Name}
	if suite.Setup != nil {
		for i := range suite.Setup.Steps {
			name := "setup:" + suite.Setup.Name
			sr.Cases = append(sr.Cases, e.abortedHook(suite.Name, name, suite.Setup.Steps[i]))
		}
	}
	for i := range suite.Cases {
		sr.Cases = append(sr.Cases, e.abortedCase(suite.Name, &suite.Cases[i], -1))
	}
	if suite.Teardown != nil {
		for i := range suite.Teardown.Steps {
			name := "teardown:" + suite.Teardown.Name
			sr.Cases = append(sr.Cases, e.abortedHook(suite.Name, name, suite.Teardown.Steps[i]))
		}
	}
	sr.Status = rollupCases(sr.Cases)
	if len(sr.Cases) == 0 {
		sr.Status = spi.StatusAborted
	}
	return sr
}

func (e *execContext) emit(ev spi.RunEvent) {
	if e.opts.OnEvent == nil {
		return
	}
	e.opts.OnEvent(ev)
}

func (e *execContext) emitStep(suiteName, caseName string, iteration, index int, step spi.StepResult) {
	if e.opts.OnEvent == nil {
		return
	}
	cp := step
	e.opts.OnEvent(spi.RunEvent{
		Kind:      spi.EventStepDone,
		Suite:     suiteName,
		Case:      caseName,
		Iteration: iteration,
		StepIndex: index,
		Step:      &cp,
	})
}

func (e *execContext) bailTripped() bool {
	return e.opts.Bail && e.bailed.Load()
}

func (e *execContext) noteBail(status spi.Status) {
	if e.opts.Bail && (status == spi.StatusFailed || status == spi.StatusError) {
		e.bailed.Store(true)
	}
}

func (e *execContext) noteCaseBail(results []spi.CaseResult) {
	if !e.opts.Bail {
		return
	}
	for _, cr := range results {
		if cr.Status == spi.StatusFailed || cr.Status == spi.StatusError {
			e.bailed.Store(true)
			return
		}
	}
}

// runHookCase wraps a single hook step as a one-step case for reporting.
// runHookCase runs one setup/teardown step as its own one-step case result.
// resolver is shared by every step of the hook (see hookResolver), so a
// value one hook step captures is visible to the next.
func (e *execContext) runHookCase(ctx context.Context, suite *model.Suite, name string, step model.Step, resolver *secrets.Resolver) spi.CaseResult {
	c := &model.TestCase{Name: name, Steps: []model.Step{step}}
	return e.runCaseIteration(ctx, suite, c, nil, -1, stepOpts{}, resolver)
}

// hookResolver is the variable scope for one run of a suite hook:
// environment < run-scoped captures so far < suite < CLI overrides.
func (e *execContext) hookResolver(suite *model.Suite) *secrets.Resolver {
	return e.newResolver(mergeVars(e.baseVars, e.runVarsSnapshot(), suite.Vars, e.opts.ExtraVars))
}

// runCase executes a case, expanding data-driven feeds into multiple iterations.
func (e *execContext) runCase(ctx context.Context, suite *model.Suite, c *model.TestCase) []spi.CaseResult {
	if ctx.Err() != nil {
		return []spi.CaseResult{e.abortedCase(suite.Name, c, -1)}
	}
	rows, fed, errCase := e.feedRows(c)
	if errCase != nil {
		return []spi.CaseResult{*errCase}
	}
	iteration := -1
	if fed {
		iteration = 0
	}

	var out []spi.CaseResult
	for ri, row := range rows {
		it := iteration
		if it >= 0 {
			it = ri
		}
		// Each feed row is its own case result, so --bail and cancellation
		// stop between rows. Cancellation wins: the row never started.
		if ctx.Err() != nil {
			out = append(out, e.abortedCase(suite.Name, c, it))
			continue
		}
		if ri > 0 && e.bailTripped() {
			out = append(out, e.skippedCase(suite.Name, c.Name, it))
			continue
		}
		cr := e.runCaseIteration(ctx, suite, c, row, it, stepOpts{}, nil)
		e.noteBail(cr.Status)
		out = append(out, cr)
	}
	return out
}

func (e *execContext) runCaseIteration(ctx context.Context, suite *model.Suite, c *model.TestCase, row map[string]string, iteration int, opts stepOpts, resolver *secrets.Resolver) spi.CaseResult {
	start := time.Now()
	cr := spi.CaseResult{Name: c.Name, Iteration: iteration}

	// Build the variable scope: env < run-scoped captures < suite < case <
	// feed row < CLI --var. (Step-scoped vars and property transfers are
	// applied later, per-step, and take precedence over all of these — see
	// runStep.) A caller-supplied resolver (load testing) already has that
	// scope and is used as-is so each virtual user keeps its own captures.
	if resolver == nil {
		vars := mergeVars(e.baseVars, e.runVarsSnapshot(), suite.Vars, c.Vars, row, e.opts.ExtraVars)
		resolver = e.newResolver(vars)
	}

	continueOnError := true
	if c.ContinueOnError != nil {
		continueOnError = *c.ContinueOnError
	}

	e.emit(spi.RunEvent{
		Kind: spi.EventCaseStarted, Suite: suite.Name, Case: c.Name, Iteration: iteration,
	})

	for si := range c.Steps {
		step := &c.Steps[si]
		if ctx.Err() != nil {
			for ; si < len(c.Steps); si++ {
				step = &c.Steps[si]
				st := abortedStepResult(step)
				cr.Steps = append(cr.Steps, st)
				e.emitStep(suite.Name, c.Name, iteration, si, st)
			}
			break
		}
		if step.Skip {
			st := spi.StepResult{Name: step.Name, Protocol: step.Protocol, Status: spi.StatusSkipped}
			cr.Steps = append(cr.Steps, st)
			e.emitStep(suite.Name, c.Name, iteration, si, st)
			continue
		}
		stepRes := e.runStep(ctx, runScope{suite: suite, tcase: c, step: step},
			resolver, e.authResolver, opts)
		if stepRes.Error != nil {
			stepRes.Error.Message = e.red.Redact(stepRes.Error.Message)
		}
		cr.Steps = append(cr.Steps, stepRes)
		e.emitStep(suite.Name, c.Name, iteration, si, stepRes)
		if stepRes.Status != spi.StatusPassed && !continueOnError {
			// A step that died because the run was cancelled must not swallow
			// the steps that never started. A real failure still stops the case.
			if ctx.Err() != nil {
				continue
			}
			break
		}
	}

	cr.DurationMS = time.Since(start).Milliseconds()
	cr.Status = rollupSteps(cr.Steps)
	e.emit(spi.RunEvent{
		Kind: spi.EventCaseDone, Suite: suite.Name, Case: c.Name,
		Iteration: iteration, Status: cr.Status, DurationMS: cr.DurationMS,
	})
	return cr
}

// runScope bundles the model scopes for a single step execution.
type runScope struct {
	suite *model.Suite
	tcase *model.TestCase
	step  *model.Step
}

func mergeVars(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
