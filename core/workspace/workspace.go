// Package workspace is the service layer behind Verity's GUI. The desktop
// app (gui/app.go) and `verity gui-call` (the browser dev bridge) both call
// these functions, so the two front doors behave identically. Everything here
// is a thin composition of core packages: no protocol, auth or assertion
// logic lives in this package.
package workspace

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Catalog returns the registered extension types (mirrors `verity list`).
func Catalog() registry.Catalog {
	return bootstrap.Default().Catalog()
}

// ValidateResult is returned by Validate.
type ValidateResult struct {
	OK      bool     `json:"ok"`
	Project string   `json:"project"`
	Errors  []string `json:"errors"`
}

// Validate loads and validates a project file (mirrors `verity validate`).
// environment selects the same environment as a run. sessions carries desktop
// OAuth sign-ins; nil is the CLI and dev bridge.
func Validate(ctx context.Context, path, environment string, sessions *OAuthSessions) ValidateResult {
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := model.Load(path)
	if err != nil {
		return ValidateResult{OK: false, Errors: []string{err.Error()}}
	}
	opt, err := graphqlOptionsFor(p, path, environment, nil, sessions)
	if err != nil {
		return ValidateResult{OK: false, Project: p.Name, Errors: []string{err.Error()}}
	}
	verrs := p.Validate()
	verrs = append(verrs, graphql.ValidateProject(ctx, p, opt)...)
	out := ValidateResult{OK: len(verrs) == 0, Project: p.Name, Errors: []string{}}
	for _, e := range verrs {
		out.Errors = append(out.Errors, e.Error())
	}
	return out
}

// RunOptions are the run parameters a frontend supplies.
type RunOptions struct {
	Path        string            `json:"path"`
	Environment string            `json:"environment"`
	Parallel    int               `json:"parallel"`
	Vars        map[string]string `json:"vars"`
	// Suites / Cases restrict the run (exact names or globs); UntilStep > 0
	// runs only the first N steps of each case. See runner.Options.
	Suites    []string `json:"suites,omitempty"`
	Cases     []string `json:"cases,omitempty"`
	UntilStep int      `json:"untilStep,omitempty"`
	// TimeoutMs is the per-attempt HTTP timeout for steps without their own
	// timeoutMs (0 = the runner's 30 s default), like the CLI's --timeout.
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// NoRedirects turns off following HTTP redirects. The zero value follows
	// them, matching the CLI's --follow-redirects default.
	NoRedirects bool `json:"noRedirects,omitempty"`
}

// Run loads, validates and executes a project, returning the redacted
// result. Plugins listed in <project-dir>/.verity/plugins.json are loaded as
// the CLI does. It does not stream progress; use RunStream for that.
func Run(ctx context.Context, opts RunOptions) (*spi.RunResult, error) {
	return run(ctx, opts, nil, nil)
}

// RunStream is Run with a progress callback. onEvent may be nil. It is
// invoked from the runner's case goroutines and must be concurrency-safe.
// The events carry the same redacted step results as the returned report.
func RunStream(ctx context.Context, opts RunOptions, onEvent func(spi.RunEvent)) (*spi.RunResult, error) {
	return run(ctx, opts, nil, onEvent)
}

// run is Run with desktop OAuth sessions (nil outside the desktop app) and
// an optional progress callback.
func run(ctx context.Context, opts RunOptions, sessions *OAuthSessions, onEvent func(spi.RunEvent)) (*spi.RunResult, error) {
	p, err := model.Load(opts.Path)
	if err != nil {
		return nil, err
	}
	if verrs := p.Validate(); len(verrs) > 0 {
		return nil, verr.Wrap(verr.CodeValidation, verrs[0], "project validation failed")
	}

	reg := bootstrap.Default()
	auto := filepath.Join(p.SourceDir, ".verity", "plugins.json")
	if _, statErr := os.Stat(auto); statErr == nil {
		mgr, perr := plugin.LoadFile(ctx, reg, auto)
		if perr != nil {
			return nil, perr
		}
		defer mgr.Close()
	}

	tokens, err := sessions.forRun(p, opts)
	if err != nil {
		return nil, err
	}
	return runner.New(reg).Run(ctx, p, runner.Options{
		AuthSessions: tokens,
		Environment:  opts.Environment,
		Parallel:     opts.Parallel,
		ExtraVars:    opts.Vars,
		Suites:       opts.Suites,
		Cases:        opts.Cases,
		UntilStep:    opts.UntilStep,
		// Clamp to a day so a bad value cannot overflow the Duration.
		Timeout:         time.Duration(min(max(opts.TimeoutMs, 0), 86_400_000)) * time.Millisecond,
		FollowRedirects: !opts.NoRedirects,
		OnEvent:         onEvent,
	})
}

// Session runs one project at a time. A second Run while one is in flight
// returns a busy error. Cancel cancels the in-flight run's context; it is a
// no-op when nothing is running. The desktop app uses a Session so Cancel
// and the one-run rule live next to the runner. Run and RunStream stay
// stateless for the CLI dev bridge.
type Session struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

// Run starts opts on this session. oauth (the desktop app's sign-ins) and
// onEvent may be nil.
func (s *Session) Run(ctx context.Context, opts RunOptions, oauth *OAuthSessions, onEvent func(spi.RunEvent)) (*spi.RunResult, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil, verr.New(verr.CodeBusy, "a run is already in progress")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
	}()
	return run(runCtx, opts, oauth, onEvent)
}

// Cancel cancels the in-flight run, if any.
func (s *Session) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
