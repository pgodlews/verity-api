package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/load"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/verr"
)

// LoadOptions are the load-run parameters a frontend supplies.
// Set Iterations or DurationMs, not both.
type LoadOptions struct {
	Path        string            `json:"path"`
	Environment string            `json:"environment"`
	Vars        map[string]string `json:"vars,omitempty"`
	Suites      []string          `json:"suites,omitempty"`
	Cases       []string          `json:"cases,omitempty"`
	VUs         int               `json:"vus"`
	Iterations  int               `json:"iterations,omitempty"`
	DurationMs  int64             `json:"durationMs,omitempty"`
	RampUpMs    int64             `json:"rampUpMs,omitempty"`
	ThinkMs     int64             `json:"thinkMs,omitempty"`
	Thresholds  []string          `json:"thresholds,omitempty"`
	// MaxErrorRate is "" (no check) or a rate string accepted by
	// load.ParseErrorRate ("1%" or "0.01").
	MaxErrorRate string `json:"maxErrorRate,omitempty"`
	AllowRemote  bool   `json:"allowRemote,omitempty"`
	TimeoutMs    int    `json:"timeoutMs,omitempty"`
	NoRedirects  bool   `json:"noRedirects,omitempty"`
	// LiveEveryMs overrides the 1 s live-stats interval when > 0.
	LiveEveryMs int `json:"liveEveryMs,omitempty"`
}

// SaveLoadReportRequest asks for a load report to be written to Out as JSON.
type SaveLoadReportRequest struct {
	Path      string       `json:"path,omitempty"`
	Out       string       `json:"out"`
	Overwrite bool         `json:"overwrite,omitempty"`
	Report    *load.Report `json:"report"`
}

// Load loads, validates, and executes a project load test, returning the
// redacted report. It does not stream live stats; use LoadStream or
// Session.Load for that.
func Load(ctx context.Context, opts LoadOptions) (*load.Report, error) {
	return runLoad(ctx, opts, nil, nil)
}

// LoadStream is Load with a live-stats callback. onLive may be nil.
func LoadStream(ctx context.Context, opts LoadOptions, onLive func(load.LiveStats)) (*load.Report, error) {
	return runLoad(ctx, opts, nil, onLive)
}

// Load starts a load run on this session. A second Run or Load while one is
// in flight returns a busy error. Cancel stops the in-flight load test and
// returns the partial report.
func (s *Session) Load(ctx context.Context, opts LoadOptions, oauth *OAuthSessions, onLive func(load.LiveStats)) (*load.Report, error) {
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
	return runLoad(runCtx, opts, oauth, onLive)
}

// LoadTargets resolves the target hosts for the selected suites, cases, and
// environment and reports which are outside the private allow-list without
// sending any traffic.
func LoadTargets(ctx context.Context, opts LoadOptions, sessions *OAuthSessions) (*load.TargetReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	lopts, err := toLoadOptions(p, opts, sessions, nil)
	if err != nil {
		return nil, err
	}
	return load.CheckTargets(reg, p, lopts)
}

// SaveLoadReport writes req.Report as indented JSON using load.WriteJSON.
// Relative paths are resolved next to req.Path when set. It refuses to
// overwrite an existing file unless req.Overwrite is true.
func SaveLoadReport(req SaveLoadReportRequest) (string, error) {
	if req.Report == nil {
		return "", verr.New(verr.CodeConfig, "load report is required")
	}
	out := strings.TrimSpace(req.Out)
	if out == "" {
		return "", verr.New(verr.CodeConfig, "output path is required")
	}
	if !filepath.IsAbs(out) && strings.TrimSpace(req.Path) != "" {
		out = filepath.Join(filepath.Dir(req.Path), out)
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return "", verr.Wrap(verr.CodeConfig, err, "resolve output path")
	}
	if _, err := os.Stat(out); err == nil && !req.Overwrite {
		return "", verr.Newf(verr.CodeConfig, "%s already exists", out)
	}
	var buf bytes.Buffer
	if err := load.WriteJSON(&buf, req.Report); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "marshal load report")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "create report directory")
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "write load report")
	}
	return out, nil
}

func runLoad(ctx context.Context, opts LoadOptions, sessions *OAuthSessions, onLive func(load.LiveStats)) (*load.Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
	lopts, err := toLoadOptions(p, opts, sessions, onLive)
	if err != nil {
		return nil, err
	}
	return load.Run(ctx, reg, p, lopts)
}

func toLoadOptions(p *model.Project, opts LoadOptions, sessions *OAuthSessions, onLive func(load.LiveStats)) (load.Options, error) {
	var maxRate *float64
	if raw := strings.TrimSpace(opts.MaxErrorRate); raw != "" {
		r, err := load.ParseErrorRate(raw)
		if err != nil {
			return load.Options{}, err
		}
		maxRate = &r
	}
	tokens, err := sessions.forRun(p, RunOptions{
		Path:        opts.Path,
		Environment: opts.Environment,
		Vars:        opts.Vars,
	})
	if err != nil {
		return load.Options{}, err
	}
	every := time.Second
	if opts.LiveEveryMs > 0 {
		every = clampMs(int64(opts.LiveEveryMs))
	}
	return load.Options{
		AuthSessions:    tokens,
		Environment:     opts.Environment,
		ExtraVars:       opts.Vars,
		Suites:          opts.Suites,
		Cases:           opts.Cases,
		VUs:             opts.VUs,
		Iterations:      opts.Iterations,
		Duration:        clampMs(opts.DurationMs),
		RampUp:          clampMs(opts.RampUpMs),
		Think:           clampMs(opts.ThinkMs),
		Thresholds:      opts.Thresholds,
		MaxErrorRate:    maxRate,
		AllowRemote:     opts.AllowRemote,
		FollowRedirects: !opts.NoRedirects,
		Timeout:         clampMs(int64(opts.TimeoutMs)),
		OnLive:          onLive,
		LiveEvery:       every,
	}, nil
}

// clampMs converts milliseconds to a time.Duration, preserving negative
// values so load.Run rejects them and capping positive values at 24 hours so
// a large number cannot overflow int64 nanoseconds.
func clampMs(ms int64) time.Duration {
	if ms < 0 {
		return -time.Millisecond
	}
	const dayMs = 86_400_000
	if ms > dayMs {
		ms = dayMs
	}
	return time.Duration(ms) * time.Millisecond
}
