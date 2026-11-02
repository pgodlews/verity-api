// watch.go is `verity run --watch`.
//
// The runner is unchanged. Each iteration loads the project (which records
// the files it read and the feed/upload files it references), runs, and
// writes reporters. fsnotify watches the parent directory of each of those
// files, not the file itself: vim and VS Code save by writing a temp file
// and renaming it into place, which drops a watch that was attached to the
// old inode. Events for other names in those directories (editor swap files,
// report output) are ignored. A short trailing debounce collapses the burst
// of events one save produces.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/watchfs"
)

// watchDebounce is how long a burst of filesystem events must stay quiet
// before a re-run. Editors emit several events for one save. The value lives
// in core/watchfs so the desktop app uses the same window.
const watchDebounce = watchfs.Debounce

// watchConfig is one `verity run --watch` invocation. Env files are loaded
// by cmdRun before this starts; they are not part of the watch set.
type watchConfig struct {
	ProjectPath        string
	Env                string
	JUnitOut           string
	JSONOut            string
	HTMLOut            string
	Parallel           int
	Timeout            time.Duration
	JSONStdout         bool
	Quiet              bool
	FollowRedirects    bool
	PluginsFile        string
	UntilStep          int
	Bail               bool
	AllowExternalFiles bool
	Vars               map[string]string
	Suites             []string
	Cases              []string
	// TTY enables the ANSI clear before each re-run. cmdWatch sets it from
	// stdout; tests leave it false.
	TTY bool
	// DeltaOut receives the one-line delta. Nil uses stdout, or stderr when
	// JSON is on stdout so a machine reader still sees one JSON document per run.
	DeltaOut io.Writer
	// Debounce overrides watchDebounce when > 0. Tests use the default.
	Debounce time.Duration
	// Iter, when non-nil, receives the exit code of each finished iteration
	// after directory watches are updated and before the next wait. Nil in
	// the CLI. The send also selects on ctx so a cancelled run cannot block
	// here.
	Iter chan int
}

func cmdWatch(cfg watchConfig) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg.TTY = stdoutIsTTY()
	return watchLoop(ctx, cfg)
}

// watchLoop runs the project, then again each time a source file changes,
// until ctx is cancelled. The returned code is the last finished run. A run
// interrupted by ctx keeps the previous finished code; if the first run is
// the one interrupted, its code is returned.
func watchLoop(ctx context.Context, cfg watchConfig) int {
	debounce := cfg.Debounce
	if debounce <= 0 {
		debounce = watchDebounce
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watch: %v\n", err)
		return exitInternal
	}
	defer watcher.Close()

	var (
		lastCode = exitOK
		haveRun  bool
		prev     *spi.RunResult
		files    = []string{absPath(cfg.ProjectPath)}
		active   = map[string]bool{}
	)

	for {
		if ctx.Err() != nil {
			return lastCode
		}
		code, result, loaded := runOnce(ctx, cfg)
		if ctx.Err() != nil {
			if !haveRun {
				return code
			}
			return lastCode
		}
		haveRun = true
		lastCode = code
		if loaded != nil {
			files = loaded
		}
		ensureWatches(watcher, watchDirs(files), active)
		if result != nil && prev != nil {
			fmt.Fprintln(cfg.deltaWriter(), formatDelta(prev, result))
		}
		if result != nil {
			prev = result
		}
		if ctx.Err() != nil {
			return lastCode
		}
		fmt.Fprintf(os.Stderr, "watch: watching %d files\n", len(files))
		if cfg.Iter != nil {
			select {
			case <-ctx.Done():
				return lastCode
			case cfg.Iter <- lastCode:
			}
		}
		if err := waitForSourceChange(ctx, watcher, files, debounce); err != nil {
			return lastCode
		}
		if cfg.TTY {
			clearScreen(os.Stdout)
		}
	}
}

// runOnce loads, validates, and runs the project once. files is nil when the
// project could not be loaded; a validation failure still returns SourceFiles
// so watch can retry when the user fixes the file. Reporter paths are
// overwritten on every successful run.
func runOnce(ctx context.Context, cfg watchConfig) (code int, result *spi.RunResult, files []string) {
	project, err := model.Load(cfg.ProjectPath)
	if err != nil {
		return failJSON(cfg.JSONStdout, exitConfig, "load", err), nil, nil
	}
	files = project.SourceFiles
	if verrs := project.Validate(); len(verrs) > 0 {
		if cfg.JSONStdout {
			emitJSON(map[string]any{"ok": false, "errors": verrs})
		} else {
			fmt.Fprintln(os.Stderr, "validation failed:")
			for _, e := range verrs {
				fmt.Fprintf(os.Stderr, "  - %s\n", e.Error())
			}
		}
		return exitConfig, nil, files
	}

	warnLiteralSecrets(project)
	reg := bootstrap.Default()
	pluginsPath := cfg.PluginsFile
	if pluginsPath == "" {
		auto := filepath.Join(project.SourceDir, ".verity", "plugins.json")
		if _, statErr := os.Stat(auto); statErr == nil {
			pluginsPath = auto
		}
	}
	if pluginsPath != "" {
		mgr, perr := plugin.LoadFile(ctx, reg, pluginsPath)
		if perr != nil {
			return failJSON(cfg.JSONStdout, exitConfig, "plugins", perr), nil, files
		}
		defer mgr.Close()
	}

	result, err = runner.New(reg).Run(ctx, project, runner.Options{
		Environment:        cfg.Env,
		Parallel:           cfg.Parallel,
		Timeout:            cfg.Timeout,
		ExtraVars:          cfg.Vars,
		FollowRedirects:    cfg.FollowRedirects,
		Suites:             cfg.Suites,
		Cases:              cfg.Cases,
		UntilStep:          cfg.UntilStep,
		Bail:               cfg.Bail,
		AllowExternalFiles: cfg.AllowExternalFiles,
	})
	if err != nil {
		return failJSON(cfg.JSONStdout, exitConfig, "run", err), nil, files
	}

	code = exitCodeOf(result)
	if cfg.JSONStdout {
		if rc := writeReporter(reg, "json", result, "-"); rc != exitOK {
			return rc, result, files
		}
	} else if !cfg.Quiet {
		if rc := writeReporter(reg, "console", result, "-"); rc != exitOK {
			return rc, result, files
		}
	}
	if cfg.JUnitOut != "" {
		if rc := writeReporter(reg, "junit", result, cfg.JUnitOut); rc != exitOK {
			return rc, result, files
		}
	}
	if cfg.JSONOut != "" {
		if rc := writeReporter(reg, "json", result, cfg.JSONOut); rc != exitOK {
			return rc, result, files
		}
	}
	if cfg.HTMLOut != "" {
		if rc := writeReporter(reg, "html", result, cfg.HTMLOut); rc != exitOK {
			return rc, result, files
		}
	}
	return code, result, files
}

func exitCodeOf(result *spi.RunResult) int {
	if result.Summary.Aborted {
		return exitInterrupted
	}
	switch result.Summary.Status {
	case spi.StatusPassed, spi.StatusSkipped:
		return exitOK
	default:
		return exitTestFailed
	}
}

func (cfg watchConfig) deltaWriter() io.Writer {
	if cfg.DeltaOut != nil {
		return cfg.DeltaOut
	}
	if cfg.JSONStdout {
		return os.Stderr
	}
	return os.Stdout
}

// The watch set, ancestor handling, debounce, and event filter live in
// core/watchfs. These wrappers keep the CLI names the watch tests call.

func ensureWatches(w *fsnotify.Watcher, dirs []string, active map[string]bool) {
	watchfs.EnsureWatches(w, dirs, active)
}

func existingAncestor(dir string) string { return watchfs.ExistingAncestor(dir) }

func watchDirs(files []string) []string { return watchfs.WatchDirs(files) }

func waitForSourceChange(ctx context.Context, w *fsnotify.Watcher, files []string, debounce time.Duration) error {
	set := make(map[string]struct{}, len(files))
	for _, f := range files {
		set[filepath.Clean(f)] = struct{}{}
	}
	_, err := watchfs.WaitForSourceChange(ctx, w, set, debounce)
	return err
}

func pushDebounce(deadline, eventAt time.Time, d time.Duration) time.Time {
	return watchfs.PushDebounce(deadline, eventAt, d)
}

func debounceFired(deadline, now time.Time) bool {
	return watchfs.DebounceFired(deadline, now)
}

func eventTriggers(files map[string]struct{}, name string, op fsnotify.Op) bool {
	return watchfs.EventTriggers(files, name, op)
}

func absPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// clearScreen sends the ANSI sequence that clears the terminal and homes the
// cursor. Callers use it only when stdout is a TTY.
func clearScreen(w io.Writer) {
	_, _ = io.WriteString(w, "\033[2J\033[H")
}

func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// formatDelta is the one-line comparison of two runs, keyed by suite, case
// (feed iteration included), and step.
func formatDelta(prev, next *spi.RunResult) string {
	newly, fixed, unchanged := countDelta(stepStatuses(prev), stepStatuses(next))
	return fmt.Sprintf("%d newly failing, %d fixed, %d unchanged", newly, fixed, unchanged)
}

// countDelta compares step outcomes.
//
// A step is newly failing when it is failed or error now and was not before
// (including a step that did not exist). It is fixed when it was failed or
// error and is now passed, skipped, or gone. failed and error compare as the
// same failing outcome. Steps present in both runs with the same status are
// unchanged. Moves between passed and skipped, and added or removed steps
// that are not failing, are not counted.
func countDelta(prev, next map[string]spi.Status) (newly, fixed, unchanged int) {
	seen := make(map[string]struct{}, len(prev)+len(next))
	for k := range prev {
		seen[k] = struct{}{}
	}
	for k := range next {
		seen[k] = struct{}{}
	}
	for k := range seen {
		ps, pok := prev[k]
		ns, nok := next[k]
		switch {
		case pok && nok && ps == ns:
			unchanged++
		case pok && nok && failingStatus(ps) && failingStatus(ns):
			unchanged++
		case nok && failingStatus(ns) && (!pok || !failingStatus(ps)):
			newly++
		case pok && failingStatus(ps) && (!nok || !failingStatus(ns)):
			fixed++
		}
	}
	return newly, fixed, unchanged
}

func failingStatus(s spi.Status) bool {
	return s == spi.StatusFailed || s == spi.StatusError
}

// stepStatuses keys each step as suite/case/step. A data-feed iteration is
// part of the case name (suite/case#N/step) so rows do not collapse. A
// repeated step name in one case gets a #2, #3, … suffix.
func stepStatuses(r *spi.RunResult) map[string]spi.Status {
	out := map[string]spi.Status{}
	if r == nil {
		return out
	}
	for _, suite := range r.Suites {
		for _, c := range suite.Cases {
			caseName := c.Name
			if c.Iteration >= 0 {
				caseName = fmt.Sprintf("%s#%d", c.Name, c.Iteration)
			}
			seen := map[string]int{}
			for _, st := range c.Steps {
				seen[st.Name]++
				key := suite.Name + "/" + caseName + "/" + st.Name
				if seen[st.Name] > 1 {
					key = fmt.Sprintf("%s#%d", key, seen[st.Name])
				}
				out[key] = st.Status
			}
		}
	}
	return out
}
