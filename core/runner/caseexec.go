package runner

import (
	"context"
	"net/http"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// CaseBinding is one selected case and the suite that owns it. Pointers refer
// to the filtered project held by the CaseExec; they stay valid for the
// exec's lifetime.
type CaseBinding struct {
	Suite *model.Suite
	Case  *model.TestCase
}

// CaseRequest is one execution of a case. The caller owns Resolver and must
// not share it across goroutines. Run-scoped transfers update that resolver
// and, when set, OnRunVar — they are not written to the shared baseline, so
// one virtual user cannot see another's captures. Transport is reused for
// HTTP steps that have no TLS config of their own.
type CaseRequest struct {
	Suite     *model.Suite
	Case      *model.TestCase
	Resolver  *secrets.Resolver
	OnRunVar  func(name, value string)
	Transport http.RoundTripper
}

// CaseExec runs individual cases of a prepared project. Load testing uses it
// so each virtual user executes through Runner (assertions, transfers, auth,
// redaction) without a second implementation of the step loop.
type CaseExec struct {
	e       *execContext
	project *model.Project
	cases   []CaseBinding
}

// Prepare checks filters, selects the environment, and builds the shared run
// state (redactor, auth cache, base variables). It does not execute a step.
// The project must contain at least one selected case.
func (r *Runner) Prepare(project *model.Project, opts Options) (*CaseExec, error) {
	ex, project, err := r.prepare(project, opts)
	if err != nil {
		return nil, err
	}
	x := &CaseExec{e: ex, project: project}
	for i := range project.Suites {
		s := &project.Suites[i]
		for j := range s.Cases {
			x.cases = append(x.cases, CaseBinding{Suite: s, Case: &s.Cases[j]})
		}
	}
	if len(x.cases) == 0 {
		return nil, verr.New(verr.CodeConfig, "no cases to run")
	}
	return x, nil
}

// Redactor is the run redactor. Every load-test report must pass through it.
func (x *CaseExec) Redactor() *secrets.Redactor { return x.e.red }

// ProjectName is the project name recorded on reports.
func (x *CaseExec) ProjectName() string { return x.project.Name }

// EnvironmentName is the environment selected for this exec.
func (x *CaseExec) EnvironmentName() string { return x.e.env.Name }

// Project is the filtered project. Callers walk it to discover request URLs
// before any traffic is sent.
func (x *CaseExec) Project() *model.Project { return x.project }

// Cases returns the selected cases in project order.
func (x *CaseExec) Cases() []CaseBinding {
	out := make([]CaseBinding, len(x.cases))
	copy(out, x.cases)
	return out
}

// RunScoped copies the run-scoped captures recorded so far. After RunSetups
// this is the baseline every virtual user starts from.
func (x *CaseExec) RunScoped() map[string]string { return x.e.runVarsSnapshot() }

// Protocol returns the registered protocol for a step's type name.
func (x *CaseExec) Protocol(name string) (spi.Protocol, error) {
	return x.e.runner.reg.Protocol(name)
}

// SetHTTPTransport installs the transport auth providers use for their own
// HTTP calls (OAuth2 token requests). It must be set before any step runs.
func (x *CaseExec) SetHTTPTransport(rt http.RoundTripper) {
	x.e.authResolver.SetHTTPTransport(rt)
}

// CloseIdleTransports closes idle connections on private TLS transports
// cached for this exec.
func (x *CaseExec) CloseIdleTransports() {
	x.e.tlsCache.close()
}

// FeedRows returns the data rows for a case, using the same loader as
// RunCase. A case with no feed returns one nil row; an empty feed returns
// no rows (RunCase reports that case as skipped). A feed that fails to load
// returns a config error.
func (x *CaseExec) FeedRows(c *model.TestCase) ([]map[string]string, error) {
	if c == nil || c.Feed == nil {
		return []map[string]string{nil}, nil
	}
	rows, fed, errCase := x.e.feedRows(c)
	if errCase != nil && errCase.Status == spi.StatusSkipped {
		return []map[string]string{}, nil
	}
	if errCase != nil {
		msg := "load feed"
		if len(errCase.Steps) > 0 && errCase.Steps[0].Error != nil && errCase.Steps[0].Error.Message != "" {
			msg = errCase.Steps[0].Error.Message
		}
		return nil, verr.New(verr.CodeConfig, msg)
	}
	if !fed {
		return []map[string]string{nil}, nil
	}
	return rows, nil
}

// InterpolateRequest resolves raw the way RunCase interpolates a request
// field: environment, run captures, suite, case, feed row, CLI vars, then
// step vars. Unknown variables are left in place. Captures that only exist
// after a transfer are not visible.
func (x *CaseExec) InterpolateRequest(suite *model.Suite, c *model.TestCase, step *model.Step, row map[string]string, raw string) (string, error) {
	r, err := x.requestResolver(suite, c, step, row)
	if err != nil {
		return "", err
	}
	out, err := r.Interpolate(raw, false)
	x.e.red.AddAll(r.SecretValues())
	if err != nil {
		return "", verr.Wrap(verr.CodeConfig, err, "interpolate request target")
	}
	return out, nil
}

func (x *CaseExec) requestResolver(suite *model.Suite, c *model.TestCase, step *model.Step, row map[string]string) (*secrets.Resolver, error) {
	var suiteVars, caseVars map[string]string
	if suite != nil {
		suiteVars = suite.Vars
	}
	if c != nil {
		caseVars = c.Vars
	}
	r := x.e.newResolver(mergeVars(x.e.baseVars, x.e.runVarsSnapshot(), suiteVars, caseVars, row, x.e.opts.ExtraVars))
	defer func() { x.e.red.AddAll(r.SecretValues()) }()
	if step == nil {
		return r, nil
	}
	for k, v := range step.Vars {
		iv, err := r.Interpolate(v, false)
		if err != nil {
			return nil, verr.Wrap(verr.CodeConfig, err, "interpolate step var "+k)
		}
		r.Set(k, iv)
	}
	return r, nil
}

// InterpolateStep resolves ${...} in s with the same precedence a step run
// uses, minus captures that only exist after a transfer and minus a feed
// row. Unknown variables are left in place.
func (x *CaseExec) InterpolateStep(suite *model.Suite, c *model.TestCase, step *model.Step, s string) (string, error) {
	var suiteVars, caseVars, stepVars map[string]string
	if suite != nil {
		suiteVars = suite.Vars
	}
	if c != nil {
		caseVars = c.Vars
	}
	if step != nil {
		stepVars = step.Vars
	}
	r := x.e.newResolver(mergeVars(x.e.baseVars, x.e.runVarsSnapshot(), suiteVars, caseVars, stepVars, x.e.opts.ExtraVars))
	defer func() { x.e.red.AddAll(r.SecretValues()) }()
	return r.Interpolate(s, false)
}

// NewResolver builds the variable scope for one case execution:
// environment < run-scoped captures < suite < case < CLI overrides.
// runVars is the caller's copy of the run-scoped baseline (and any captures
// that virtual user has made since). The map is copied.
func (x *CaseExec) NewResolver(suite *model.Suite, c *model.TestCase, runVars map[string]string) *secrets.Resolver {
	return x.e.newResolver(mergeVars(x.e.baseVars, runVars, suite.Vars, c.Vars, x.e.opts.ExtraVars))
}

// SetupResult is one setup-hook step. Suite is the suite that owns the hook.
type SetupResult struct {
	Suite  string
	Result spi.CaseResult
}

// RunSetups executes each selected suite's setup hook once, in project order,
// and records scope:run transfers on the shared baseline. Teardown is not
// run. A cancelled context stops the walk; hooks that never started are
// reported as aborted.
func (x *CaseExec) RunSetups(ctx context.Context) []SetupResult {
	var out []SetupResult
	seen := map[*model.Suite]bool{}
	for _, b := range x.cases {
		if seen[b.Suite] || b.Suite.Setup == nil {
			seen[b.Suite] = true
			continue
		}
		seen[b.Suite] = true
		for i := range b.Suite.Setup.Steps {
			name := "setup:" + b.Suite.Setup.Name
			step := b.Suite.Setup.Steps[i]
			var cr spi.CaseResult
			if ctx.Err() != nil {
				cr = x.e.abortedHook(b.Suite.Name, name, step)
			} else {
				cr = x.e.runHookCase(ctx, b.Suite, name, step)
			}
			out = append(out, SetupResult{Suite: b.Suite.Name, Result: cr})
		}
	}
	return out
}

// RunCase executes one case. A data feed expands to one result per row, each
// starting from the resolver's variables plus that row; run-scoped transfers
// from an earlier row are visible to the next row and to OnRunVar. Without a
// feed the caller's resolver is used directly, so step vars and transfers
// mutate it.
func (x *CaseExec) RunCase(ctx context.Context, req CaseRequest) ([]spi.CaseResult, error) {
	if req.Suite == nil || req.Case == nil {
		return nil, verr.New(verr.CodeConfig, "case run requires a suite and a case")
	}
	if req.Resolver == nil {
		return nil, verr.New(verr.CodeConfig, "case run requires a resolver")
	}
	rows, fed, errCase := x.e.feedRows(req.Case)
	if errCase != nil {
		return []spi.CaseResult{*errCase}, nil
	}
	opts := stepOpts{transport: req.Transport, onRunVar: req.OnRunVar}
	if !fed {
		return []spi.CaseResult{x.e.runCaseIteration(ctx, req.Suite, req.Case, nil, -1, opts, req.Resolver)}, nil
	}

	// Snapshot once so feed rows do not see each other's case-scoped
	// transfers. Run-scoped captures still accumulate in overlay.
	base := mergeVars(req.Resolver.Vars)
	overlay := map[string]string{}
	opts.onRunVar = func(name, value string) {
		overlay[name] = value
		req.Resolver.Set(name, value)
		if req.OnRunVar != nil {
			req.OnRunVar(name, value)
		}
	}
	var out []spi.CaseResult
	for ri, row := range rows {
		if ctx.Err() != nil {
			out = append(out, x.e.abortedCase(req.Suite.Name, req.Case, ri))
			continue
		}
		resolver := x.e.newResolver(mergeVars(base, overlay, row))
		out = append(out, x.e.runCaseIteration(ctx, req.Suite, req.Case, nil, ri, opts, resolver))
	}
	return out, nil
}

// feedRows returns the data rows for a case. fed is false when the case runs
// once without a feed. On failure, errCase is a single error result.
func (e *execContext) feedRows(c *model.TestCase) (rows []map[string]string, fed bool, errCase *spi.CaseResult) {
	rows = []map[string]string{nil}
	if c.Feed == nil {
		return rows, false, nil
	}
	ds, err := e.runner.reg.DataSource(c.Feed.Type)
	if err != nil {
		return nil, false, &spi.CaseResult{
			Name:      c.Name,
			Status:    spi.StatusError,
			Iteration: -1,
			Steps: []spi.StepResult{{
				Name:   "feed",
				Status: spi.StatusError,
				Error:  e.errInfo(verr.Wrap(verr.CodeConfig, err, "resolve feed type "+c.Feed.Type)),
			}},
		}
	}
	feedConfig := make(map[string]any, len(c.Feed.Config)+1)
	for k, v := range c.Feed.Config {
		feedConfig[k] = v
	}
	feedConfig[fsutil.AllowExternalKey] = e.opts.AllowExternalFiles
	fetched, ferr := ds.Rows(e.project.SourceDir, feedConfig, c.Feed.Inline, c.Feed.Path)
	if ferr != nil {
		return nil, false, &spi.CaseResult{
			Name:      c.Name,
			Status:    spi.StatusError,
			Iteration: -1,
			Steps: []spi.StepResult{{
				Name:   "feed",
				Status: spi.StatusError,
				Error:  e.errInfo(ferr),
			}},
		}
	}
	if len(fetched) == 0 {
		// Running the case un-fed would send literal ${column} placeholders.
		where := c.Feed.Path
		if where == "" {
			where = "inline rows"
		}
		return nil, false, &spi.CaseResult{
			Name:      c.Name,
			Status:    spi.StatusSkipped,
			Iteration: -1,
			Steps: []spi.StepResult{{
				Name:   "feed",
				Status: spi.StatusSkipped,
				Error: e.errInfo(verr.Newf(verr.CodeConfig,
					"%s feed (%s) produced no rows; case skipped", c.Feed.Type, where)),
			}},
		}
	}
	return fetched, true, nil
}

// newResolver builds a variable resolver with the run's file policy:
// ${secret:file:...} is confined to the project directory unless
// --allow-external-files is set.
func (e *execContext) newResolver(vars map[string]string) *secrets.Resolver {
	return secrets.NewResolver(vars).WithFiles(e.project.SourceDir, e.opts.AllowExternalFiles)
}
