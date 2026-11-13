package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/load"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
)

func cmdLoad(args []string) int {
	red := secrets.NewRedactor()
	stderr := &loadRedactingWriter{out: os.Stderr, red: red}
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	common := bindCommonRunFlags(fs)
	var (
		vus         = fs.Int("vus", 1, "virtual users (max 1000; a warning is printed above 200)")
		duration    = fs.Duration("duration", 0, "how long to start new iterations (alternative to --iterations)")
		iterations  = fs.Int("iterations", 0, "case runs each virtual user performs (alternative to --duration)")
		ramp        = fs.Duration("ramp-up", 0, "spread virtual-user start across this duration")
		think       = fs.Duration("think", 0, "pause between case runs of one virtual user")
		maxError    = fs.String("max-error-rate", "", "fail when the failure rate exceeds this (1% or 0.01)")
		jsonOut     = fs.String("json", "", "write the load report JSON to this file")
		allowRemote = fs.Bool("allow-remote", false, "allow targets that are not loopback, RFC1918, or link-local")
		thresholds  listFlags
	)
	fs.Var(&thresholds, "threshold", "latency bound, e.g. p95<500ms or 'GET widget:p95<300ms' (repeatable)")
	fs.SetOutput(stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if *vus < 1 || *vus > load.MaxVUs {
		fmt.Fprintf(stderr, "load: --vus must be from 1 to %d\n", load.MaxVUs)
		return exitUsage
	}
	if *iterations < 0 || *duration < 0 || *ramp < 0 || *think < 0 {
		fmt.Fprintln(stderr, "load: iterations, duration, ramp-up, and think must be >= 0")
		return exitUsage
	}
	if (*iterations > 0) == (*duration > 0) {
		fmt.Fprintln(stderr, "load: set exactly one of --duration or --iterations")
		return exitUsage
	}
	for _, th := range thresholds {
		if _, err := load.ParseThreshold(th); err != nil {
			fmt.Fprintf(stderr, "load: %v\n", err)
			return exitUsage
		}
	}
	var maxRate *float64
	if *maxError != "" {
		rate, err := load.ParseErrorRate(*maxError)
		if err != nil {
			fmt.Fprintf(stderr, "load: %v\n", err)
			return exitUsage
		}
		maxRate = &rate
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "load: expected one project file")
		return exitUsage
	}
	if msg := load.VUWarning(*vus); msg != "" {
		fmt.Fprintln(stderr, msg)
	}

	if err := common.applyEnvFiles(); err != nil {
		return failJSON(false, exitConfig, "env-file", err)
	}

	project, err := model.Load(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "load: %v\n", err)
		return exitConfig
	}
	if verrs := project.Validate(); len(verrs) > 0 {
		fmt.Fprintln(stderr, "validation failed:")
		for _, e := range verrs {
			fmt.Fprintf(stderr, "  - %s\n", e.Error())
		}
		return exitConfig
	}
	warnLiteralSecrets(project)

	reg := bootstrap.Default()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go watchInterrupt(sigs, cancel, done, os.Exit)

	mgr, perr := plugin.LoadForProject(ctx, reg, project.SourceDir, common.pluginsFile, pluginTrust(common.trustPlugins))
	if perr != nil {
		if ctx.Err() != nil {
			return exitInterrupted
		}
		fmt.Fprintf(stderr, "plugins: %v\n", perr)
		return exitConfig
	}
	defer mgr.Close()
	mgr.SetRedactor(red)

	opts := load.Options{
		Redactor:        red,
		Environment:     common.env,
		ExtraVars:       common.vars,
		Suites:          common.suites,
		Cases:           common.cases,
		VUs:             *vus,
		Iterations:      *iterations,
		Duration:        *duration,
		RampUp:          *ramp,
		Think:           *think,
		Thresholds:      thresholds,
		MaxErrorRate:    maxRate,
		AllowRemote:     *allowRemote,
		FollowRedirects: true,
		Timeout:         common.timeout,
		BeforeStart: func(hosts []string) {
			fmt.Fprintln(stderr, load.FormatStart(hosts, *vus, *iterations, *duration, *ramp))
			if *allowRemote {
				fmt.Fprintln(stderr, "load: --allow-remote set; non-private targets are permitted")
			}
		},
	}
	if isTTY(os.Stdout) {
		opts.Live = os.Stdout
	}
	return executeLoad(ctx, reg, project, opts, *jsonOut, os.Stdout)
}

// executeLoad runs the load and writes the report. A cancelled context still
// writes jsonPath when a report was produced, then returns exitInterrupted.
func executeLoad(ctx context.Context, reg *registry.Registry, project *model.Project, opts load.Options, jsonPath string, console io.Writer) int {
	if opts.Redactor == nil {
		opts.Redactor = secrets.NewRedactor()
	}
	stderr := &loadRedactingWriter{out: os.Stderr, red: opts.Redactor}
	rep, err := load.Run(ctx, reg, project, opts)
	if err != nil {
		fmt.Fprintf(stderr, "load: %v\n", err)
		if ctx.Err() != nil {
			return exitInterrupted
		}
		return exitConfig
	}
	if jsonPath != "" {
		if err := writeLoadJSON(jsonPath, rep); err != nil {
			fmt.Fprintf(stderr, "json: %v\n", err)
			return exitInternal
		}
	}
	if console != nil {
		if err := load.WriteConsole(console, rep); err != nil {
			fmt.Fprintf(stderr, "console: %v\n", err)
			return exitInternal
		}
	}
	if rep.Interrupted || ctx.Err() != nil {
		fmt.Fprintln(stderr, "load interrupted; partial report written")
		return exitInterrupted
	}
	if !rep.Passed {
		for _, f := range rep.Failures() {
			fmt.Fprintf(stderr, "load: %s\n", f)
		}
		return exitTestFailed
	}
	return exitOK
}

func writeLoadJSON(path string, rep *load.Report) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return load.WriteJSON(f, rep)
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// fmt emits a complete diagnostic in one Write. Return the original byte
// count even when masking changes its length, as required by io.Writer.
type loadRedactingWriter struct {
	out io.Writer
	red *secrets.Redactor
}

func (w *loadRedactingWriter) Write(p []byte) (int, error) {
	masked := w.red.RedactBytes(p)
	n, err := w.out.Write(masked)
	if n < len(masked) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
