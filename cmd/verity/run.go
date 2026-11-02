package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// varFlags collects repeated --var k=v flags.
type varFlags map[string]string

func (v varFlags) String() string { return "" }
func (v varFlags) Set(s string) error {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("--var must be key=value, got %q", s)
	}
	v[parts[0]] = parts[1]
	return nil
}

// listFlags collects a repeatable string flag.
type listFlags []string

func (l *listFlags) String() string     { return strings.Join(*l, ",") }
func (l *listFlags) Set(s string) error { *l = append(*l, s); return nil }

// commonRunFlags holds the flags shared by `verity run` and `verity load`.
type commonRunFlags struct {
	env             string
	timeout         time.Duration
	pluginsFile     string
	envFileOverride bool
	vars            varFlags
	suites          listFlags
	cases           listFlags
	envFiles        listFlags
}

func bindCommonRunFlags(fs *flag.FlagSet) *commonRunFlags {
	c := &commonRunFlags{vars: varFlags{}, timeout: 30 * time.Second}
	fs.StringVar(&c.env, "env", "", "environment name (default: project default)")
	fs.Func("timeout", "per-attempt HTTP timeout (default 30s; >= 0; a step timeoutMs overrides it)", func(raw string) error {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		if timeout < 0 {
			return fmt.Errorf("timeout must be >= 0")
		}
		c.timeout = timeout
		return nil
	})
	fs.StringVar(&c.pluginsFile, "plugins", "", "path to a plugins config file (default: <project-dir>/.verity/plugins.json if present)")
	fs.BoolVar(&c.envFileOverride, "env-file-override", false, "let --env-file values replace variables already set in the environment")
	fs.Var(c.vars, "var", "variable override key=value (repeatable)")
	fs.Var(&c.suites, "suite", "only run suites matching this name or glob (repeatable)")
	fs.Var(&c.cases, "case", "only run cases matching this name or glob (repeatable)")
	fs.Var(&c.envFiles, "env-file", "dotenv file to load before the run (repeatable; later files win)")
	return c
}

func (c *commonRunFlags) applyEnvFiles() error {
	return loadEnvFiles(c.envFiles, c.envFileOverride)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	common := bindCommonRunFlags(fs)
	var (
		junitOut      = fs.String("junit", "", "write JUnit XML to file")
		jsonOut       = fs.String("json", "", "write structured JSON result to file")
		htmlOut       = fs.String("html", "", "write HTML report to file")
		parallel      = fs.Int("parallel", 1, "max concurrent cases per suite")
		jsonStdout    = fs.Bool("json-stdout", false, "emit JSON result to stdout instead of console report")
		quiet         = fs.Bool("quiet", false, "suppress the console report on stdout")
		followRedir   = fs.Bool("follow-redirects", true, "follow HTTP redirects")
		untilStep     = fs.Int("until-step", 0, "run only the first N steps of each case (0 = all)")
		bail          = fs.Bool("bail", false, "stop scheduling new cases after the first failed or errored case")
		watch         = fs.Bool("watch", false, "re-run when the project, fragments, feeds, or upload files change")
		allowExternal = fs.Bool("allow-external-files", false, "let upload bodies read files outside the project directory (trusted projects only)")
	)
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "run: missing project file")
		return exitUsage
	}
	projectPath := pos[0]

	// Load dotenv before the project so ${env:NAME} and redaction see the
	// values. A bad line fails here and does not print the value.
	if err := common.applyEnvFiles(); err != nil {
		return failJSON(*jsonStdout, exitConfig, "env-file", err)
	}

	if *watch {
		return cmdWatch(watchConfig{
			ProjectPath:        projectPath,
			Env:                common.env,
			JUnitOut:           *junitOut,
			JSONOut:            *jsonOut,
			HTMLOut:            *htmlOut,
			Parallel:           *parallel,
			Timeout:            common.timeout,
			JSONStdout:         *jsonStdout,
			Quiet:              *quiet,
			FollowRedirects:    *followRedir,
			PluginsFile:        common.pluginsFile,
			UntilStep:          *untilStep,
			Bail:               *bail,
			AllowExternalFiles: *allowExternal,
			Vars:               map[string]string(common.vars),
			Suites:             []string(common.suites),
			Cases:              []string(common.cases),
		})
	}

	project, err := model.Load(projectPath)
	if err != nil {
		return failJSON(*jsonStdout, exitConfig, "load", err)
	}
	if verrs := project.Validate(); len(verrs) > 0 {
		if *jsonStdout {
			emitJSON(map[string]any{"ok": false, "errors": verrs})
		} else {
			fmt.Fprintln(os.Stderr, "validation failed:")
			for _, e := range verrs {
				fmt.Fprintf(os.Stderr, "  - %s\n", e.Error())
			}
		}
		return exitConfig
	}

	// Warn on literal-looking committed secrets (non-fatal).
	warnLiteralSecrets(project)

	reg := bootstrap.Default()

	// First SIGINT/SIGTERM cancels the run. The run finishes in-flight work,
	// writes every requested reporter, and exits exitInterrupted. A second
	// signal exits immediately, without waiting for reporters.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go watchInterrupt(sigs, cancel, done, os.Exit)

	// Load external plugins (opt-in, off the hot path): explicit --plugins file
	// or an auto-discovered .verity/plugins.json next to the project.
	pluginsPath := common.pluginsFile
	if pluginsPath == "" {
		auto := filepath.Join(project.SourceDir, ".verity", "plugins.json")
		if _, statErr := os.Stat(auto); statErr == nil {
			pluginsPath = auto
		}
	}
	if pluginsPath != "" {
		mgr, perr := plugin.LoadFile(ctx, reg, pluginsPath)
		if perr != nil {
			if ctx.Err() != nil {
				return exitInterrupted
			}
			return failJSON(*jsonStdout, exitConfig, "plugins", perr)
		}
		defer mgr.Close()
	}

	return finishRun(ctx, reg, project, runner.Options{
		Environment:        common.env,
		Parallel:           *parallel,
		Timeout:            common.timeout,
		ExtraVars:          common.vars,
		FollowRedirects:    *followRedir,
		Suites:             common.suites,
		Cases:              common.cases,
		UntilStep:          *untilStep,
		Bail:               *bail,
		AllowExternalFiles: *allowExternal,
	}, *jsonStdout, *quiet, *junitOut, *jsonOut, *htmlOut)
}

// finishRun executes project and writes every requested reporter, including
// when ctx is already cancelled or is cancelled mid-run. A cancelled run
// returns exitInterrupted after the reporters have been written.
func finishRun(ctx context.Context, reg *registry.Registry, project *model.Project, opts runner.Options, jsonStdout, quiet bool, junitOut, jsonOut, htmlOut string) int {
	result, err := runner.New(reg).Run(ctx, project, opts)
	if err != nil {
		if ctx.Err() != nil {
			return exitInterrupted
		}
		return failJSON(jsonStdout, exitConfig, "run", err)
	}

	// Console / JSON to stdout. Written even for a partial, cancelled run.
	if jsonStdout {
		writeReporter(reg, "json", result, "-")
	} else if !quiet {
		writeReporter(reg, "console", result, "-")
	}

	// File reporters.
	if junitOut != "" {
		if rc := writeReporter(reg, "junit", result, junitOut); rc != exitOK {
			return rc
		}
	}
	if jsonOut != "" {
		if rc := writeReporter(reg, "json", result, jsonOut); rc != exitOK {
			return rc
		}
	}
	if htmlOut != "" {
		if rc := writeReporter(reg, "html", result, htmlOut); rc != exitOK {
			return rc
		}
	}

	if result.Summary.Aborted {
		fmt.Fprintln(os.Stderr, "run interrupted; partial report written")
		return exitInterrupted
	}

	// Exit code reflects outcome.
	switch result.Summary.Status {
	case spi.StatusPassed, spi.StatusSkipped:
		return exitOK
	default:
		return exitTestFailed
	}
}

// watchInterrupt cancels on the first signal and exits on the second.
// done is closed when the command has finished; exit is os.Exit in production
// so a second Ctrl-C does not wait for reporters.
func watchInterrupt(sigs <-chan os.Signal, cancel func(), done <-chan struct{}, exit func(int)) {
	select {
	case <-sigs:
		cancel()
	case <-done:
		return
	}
	select {
	case <-sigs:
		exit(exitInterrupted)
	case <-done:
	}
}

// writeReporter resolves and runs a reporter, returning an exit code.
func writeReporter(reg interface {
	Reporter(string) (spi.Reporter, error)
}, typ string, result *spi.RunResult, out string) int {
	rep, err := reg.Reporter(typ)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reporter %s: %v\n", typ, err)
		return exitInternal
	}
	if err := rep.Report(result, out); err != nil {
		fmt.Fprintf(os.Stderr, "reporter %s write: %v\n", typ, err)
		return exitInternal
	}
	return exitOK
}

// warnLiteralSecrets prints warnings for credential-looking literals committed
// in the project (uses the same heuristic the validator exposes).
func warnLiteralSecrets(p *model.Project) {
	for _, a := range p.Auth {
		for k, v := range a.Config {
			if s, ok := v.(string); ok && secrets.LooksLikeSecret(s) {
				fmt.Fprintf(os.Stderr,
					"warning: auth %q field %q looks like a hard-coded secret; use ${env:VAR} instead\n",
					a.Name, k)
			}
		}
	}
}

func emitJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// failJSON emits a structured error (when json mode) and returns the exit code.
func failJSON(jsonMode bool, code int, phase string, err error) int {
	if jsonMode {
		emitJSON(map[string]any{"ok": false, "phase": phase, "error": err.Error()})
	} else {
		fmt.Fprintf(os.Stderr, "%s: %v\n", phase, err)
	}
	return code
}
