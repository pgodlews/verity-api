// Package vcs talks to a Git working tree by executing the installed git CLI.
//
// Every command is `git --no-pager -C <dir> -c core.quotepath=false
// -c core.fsmonitor=false -c log.showSignature=false …`, with `--` before
// a path argument, and GIT_OPTIONAL_LOCKS=0 so a status poll does not take the index lock, quote
// non-ASCII names, or run a repository fsmonitor hook. Reads (status, show,
// index) and writes (write.go) share that invocation, so a -c flag added to
// configArgs applies to both. Parsed commands set LC_ALL to C.UTF-8 when
// that locale exists, and to C otherwise, so diagnostics stay in English
// and non-ASCII paths are not recoded. core.quotepath stays false, and
// path lists use -z. Reads also set GIT_NO_LAZY_FETCH=1 so cat-file does
// not fetch from a promisor remote.
//
// Clean, smudge, and process filters defined in repository-local config
// (git config --local and --worktree) are overridden to empty on every
// command except `git add` and `git commit`. Global and system filters,
// including a git-lfs the user installed for themselves, are left alone:
// blanking those makes every LFS file look modified. The driver name is
// the text between `filter.` and the last dot, so filter.foo.bar.clean
// blanks foo.bar. A name that cannot be passed safely as -c fails the
// command; the filter is not left enabled. Listing the names does not run
// them. add keeps the user's filters so a staged blob is what the user
// would record. Commit then records those index blobs, not a second
// worktree hash. Pull, switch, and merge do not run smudge; a checkout of
// an LFS-tracked file leaves the pointer text until the user runs their
// own smudge. Write commands set GIT_TERMINAL_PROMPT=0 and take a context.
// Cancel signals Git (SIGINT on Unix, CTRL_C_EVENT in a private hidden
// console on Windows), then kills it after a short wait, so Git can remove
// index.lock. The kill timer is stopped when the process exits. On
// Windows, os.Interrupt is not a successful cancel: Go's Signal does not
// reach the process group.
//
// If GIT_SSH_COMMAND and core.sshCommand are both unset, writes and reads
// set GIT_SSH_COMMAND to `ssh -o BatchMode=yes` so an unknown host key or a
// passphrase fails instead of prompting. StrictHostKeyChecking=accept-new
// is never set. Credential helpers stay as the user configured them. Verity
// never stores git credentials and never force-pushes.
//
// Callers treat "not a repository" as a typed result, not a failure of the
// project. Git's own text is returned as-is, except credentials: URL
// userinfo (https://user:token@host), a token in a remote URL query string,
// and an echoed http.extraHeader.
package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

// ErrUnavailable means the git executable is not on PATH.
var ErrUnavailable = errors.New("git is not on PATH")

// NotRepositoryError means dir is not inside a work tree. Callers show
// "not a git repository" and continue; the project itself is fine.
type NotRepositoryError struct {
	Dir string
}

func (e *NotRepositoryError) Error() string {
	return e.Dir + " is not a git repository"
}

// IsNotRepository reports whether err means the directory is not a work tree.
func IsNotRepository(err error) bool {
	var n *NotRepositoryError
	return errors.As(err, &n)
}

// RevisionError means rev does not name a commit (a repository with no
// commits yet, or a missing ref). A missing file in a real commit is
// fs.ErrNotExist instead.
type RevisionError struct {
	Rev string
}

func (e *RevisionError) Error() string {
	return "git revision " + e.Rev + " does not exist"
}

// IsRevision reports whether err is a missing revision.
func IsRevision(err error) bool {
	var r *RevisionError
	return errors.As(err, &r)
}

// CommandError is a git invocation that failed. Stdout and Stderr are git's
// own text with URL userinfo masked. Args is the argv after the git binary.
type CommandError struct {
	Args   []string
	Stdout string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if out := strings.TrimSpace(e.Stdout); out != "" {
		if msg != "" {
			msg = out + "\n" + msg
		} else {
			msg = out
		}
	}
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	return "git " + strings.Join(e.Args, " ") + ": " + msg
}

func (e *CommandError) Unwrap() error { return e.Err }

// Available reports whether the git executable is on PATH.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// RepoRoot returns the working tree root that contains dir.
// A directory that is not a repository returns *NotRepositoryError.
func RepoRoot(dir string) (string, error) {
	if !Available() {
		return "", ErrUnavailable
	}
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if notRepository(err) {
			return "", &NotRepositoryError{Dir: dir}
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GitPath resolves a path inside the git directory (HEAD, index, hooks).
// `git rev-parse --git-path` honours worktrees and core.hooksPath. The
// argument is a git-dir path, so it is not placed after `--` (that would
// end option parsing and drop --git-path). The result is absolute.
func GitPath(dir, name string) (string, error) {
	if !Available() {
		return "", ErrUnavailable
	}
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\n\r\x00") {
		return "", fmt.Errorf("invalid git path %q", name)
	}
	out, err := git(dir, "rev-parse", "--git-path", name)
	if err != nil {
		if notRepository(err) {
			return "", &NotRepositoryError{Dir: dir}
		}
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("git rev-parse --git-path %s returned an empty path", name)
	}
	if !filepath.IsAbs(p) {
		// git prints a path relative to the directory given to -C.
		p = filepath.Join(dir, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p), nil
	}
	return filepath.Clean(abs), nil
}

// SymbolicFullName resolves name (HEAD, @{u}) to a full ref such as
// refs/heads/main. ok is false when name is not a symbolic ref (detached
// HEAD) or has no upstream. A directory that is not a repository returns
// *NotRepositoryError.
func SymbolicFullName(dir, name string) (ref string, ok bool, err error) {
	if !Available() {
		return "", false, ErrUnavailable
	}
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\n\r\x00") {
		return "", false, fmt.Errorf("invalid git revision %q", name)
	}
	out, err := git(dir, "rev-parse", "--symbolic-full-name", name)
	if err != nil {
		if notRepository(err) {
			return "", false, &NotRepositoryError{Dir: dir}
		}
		// Detached HEAD and a branch with no upstream land here. Callers
		// that are only choosing extra files to watch treat that as absence.
		return "", false, nil
	}
	ref = strings.TrimSpace(string(out))
	if ref == "" || ref == "HEAD" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\n\r\x00") {
		return "", false, nil
	}
	return ref, true, nil
}

// gitCancelGrace is how long a cancelled git is given to handle SIGINT and
// remove index.lock before the process group is killed.
const gitCancelGrace = 2 * time.Second

// configArgs is the prefix of every git invocation, reads and writes alike,
// so a shared -c flag cannot be missed by a second command builder.
// core.fsmonitor is a hook path from the repository config; status, commit,
// and checkout would execute it, so it is forced off. core.sshCommand and
// credential.helper stay as the user configured them: SSH is their program,
// and a credential helper may run, but Verity never stores what it returns.
// Commit still runs git's own hooks (pre-commit, commit-msg, and the rest).
// The user's gitconfig is still read. Name-only diffs do not run
// diff.external or textconv; stash diff-tree also passes --no-ext-diff
// and --no-textconv explicitly. Blob reads use `git cat-file blob` on the
// id from ls-tree or ls-files.
func configArgs(dir string) []string {
	return []string{"-C", dir, "-c", "core.quotepath=false", "-c", "core.fsmonitor=false", "-c", "log.showSignature=false"}
}

// gitOpt is the per-call variation on the shared runner.
type gitOpt struct {
	// backgroundRead avoids allocating a Windows console for synchronous
	// metadata reads. Only pure reads with context.Background set this.
	backgroundRead bool
	// terminalPrompt sets GIT_TERMINAL_PROMPT=0 so a missing credential
	// fails instead of reading the terminal. Write operations set it.
	terminalPrompt bool
	onLine         func(string)
	// skipFilters leaves clean/smudge/process alone. add and commit set it
	// so the user's filters still run. The config lookup that lists filter
	// names sets it too, so that lookup does not call itself.
	skipFilters bool
	// readOnly sets GIT_NO_LAZY_FETCH=1. fetch, pull, and push leave it unset.
	readOnly bool
	// mergeNoEdit sets GIT_MERGE_AUTOEDIT=no. Pull sets it.
	mergeNoEdit bool
	// skipSSH skips the BatchMode default. The core.sshCommand lookup sets
	// it so that lookup does not call itself.
	skipSSH bool
	stdin   []byte
	// extraConfig is appended as -c key=value before the subcommand.
	// Pull sets fetch.followTags=false: git pull on some releases does not
	// accept --no-follow-tags, while the config has the same effect.
	extraConfig []string
	// indexFile sets GIT_INDEX_FILE so a commit can use a temporary index
	// instead of the work tree. Empty means git's default index.
	indexFile string
}

// git runs a read (`status`, `rev-parse`, `ls-files`, `cat-file`, …).
// GIT_OPTIONAL_LOCKS=0 is set so optional index updates do not run.
// Ambient GIT_DIR / GIT_WORK_TREE / GIT_INDEX_FILE are dropped so -C is
// the directory that is inspected. Successful stdout is returned raw:
// callers read blobs and porcelain from it. Failures carry redacted text.
func git(dir string, args ...string) ([]byte, error) {
	out, _, err := runGit(context.Background(), dir, gitOpt{readOnly: true, backgroundRead: true}, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// runGit is the shared runner. stdout is raw process output. stderr is
// redacted. onLine receives each stderr line already redacted (git --progress
// updates a line with CR; each update is delivered). A cancelled context
// sends SIGINT to git's process group; WaitDelay then sends SIGKILL.
func runGit(ctx context.Context, dir string, opt gitOpt, args ...string) ([]byte, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	overrides, err := filterOverrideArgs(dir, opt)
	if err != nil {
		return nil, "", err
	}
	full := append(configArgs(dir), overrides...)
	for _, kv := range opt.extraConfig {
		full = append(full, "-c", kv)
	}
	full = append(full, "--no-pager")
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = gitEnv(dir, opt)
	cmd.WaitDelay = gitCancelGrace
	cmd.Cancel = func() error { return cancelGit(cmd) }
	ownProcessGroup(cmd, opt.backgroundRead)
	if len(opt.stdin) > 0 {
		cmd.Stdin = bytes.NewReader(opt.stdin)
	}
	sink := &lineSink{fn: opt.onLine}
	cmd.Stderr = sink
	out, err := commandOutput(cmd)
	sink.flush()
	stderr := redactGit(sink.String())
	if err != nil {
		if ctx.Err() != nil {
			return out, stderr, ctx.Err()
		}
		return out, stderr, &CommandError{
			Args:   full,
			Stdout: redactGit(string(out)),
			Stderr: stderr,
			Err:    err,
		}
	}
	return out, stderr, nil
}

func gitEnv(dir string, opt gitOpt) []string {
	src := os.Environ()
	out := make([]string, 0, len(src)+6)
	for _, e := range src {
		key, _, _ := strings.Cut(e, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX", "GIT_OPTIONAL_LOCKS", "LC_ALL":
			continue
		case "GIT_TERMINAL_PROMPT":
			if opt.terminalPrompt {
				continue
			}
		case "GIT_MERGE_AUTOEDIT":
			if opt.mergeNoEdit {
				continue
			}
		case "GIT_NO_LAZY_FETCH":
			if opt.readOnly {
				continue
			}
		}
		out = append(out, e)
	}
	out = append(out, "GIT_OPTIONAL_LOCKS=0", "LC_ALL="+gitLocale())
	if opt.indexFile != "" {
		out = append(out, "GIT_INDEX_FILE="+opt.indexFile)
	}
	if opt.terminalPrompt {
		out = append(out, "GIT_TERMINAL_PROMPT=0")
	}
	if opt.readOnly {
		out = append(out, "GIT_NO_LAZY_FETCH=1")
	}
	if opt.mergeNoEdit {
		out = append(out, "GIT_MERGE_AUTOEDIT=no")
	}
	if !opt.skipSSH {
		out = ensureBatchSSH(dir, out)
	}
	return out
}

// ensureBatchSSH sets a non-interactive ssh when the user has not chosen a
// command. An empty GIT_SSH_COMMAND counts as unset. accept-new is never
// added: an unknown host key fails.
func ensureBatchSSH(dir string, env []string) []string {
	if envValue(env, "GIT_SSH_COMMAND") != "" {
		return env
	}
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			continue
		}
		filtered = append(filtered, e)
	}
	if strings.TrimSpace(configuredSSH(dir)) != "" {
		return filtered
	}
	return append(filtered, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(e, prefix))
		}
	}
	return ""
}

func configuredSSH(dir string) string {
	stdout, _, err := runGit(context.Background(), dir, gitOpt{skipFilters: true, skipSSH: true, readOnly: true, backgroundRead: true}, "config", "--get", "core.sshCommand")
	if err != nil {
		return ""
	}
	return string(stdout)
}

// filterOverrideArgs disables clean, smudge, and process for filters defined
// in the repository (--local and --worktree), plus custom merge drivers.
// skipFilters (add, commit, and this function's config lookup) returns nothing.
// Global and system filters are not blanked. An unusable name, or a lookup that printed keys
// and then failed, is an error: the command does not run with that filter
// still enabled. Merge drivers use only a fixed Git text merge command.
func filterOverrideArgs(dir string, opt gitOpt) ([]string, error) {
	if opt.skipFilters {
		return nil, nil
	}
	names, err := filterNames(dir)
	if err != nil {
		return nil, err
	}
	merges, err := mergeDriverNames(dir)
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, len(names)*6+len(merges)*2)
	for _, name := range names {
		args = append(args,
			"-c", "filter."+name+".clean=",
			"-c", "filter."+name+".smudge=",
			"-c", "filter."+name+".process=",
		)
	}
	for _, name := range merges {
		args = append(args, "-c", "merge."+name+".driver=git merge-file -L ours -L base -L theirs -- %A %O %B")
	}
	return args, nil
}

// mergeDriverNames uses the same subsection validation as filter drivers.
// An empty driver is treated as an empty executable on Git 2.43, so use
// Git's own merge-file with fixed arguments to retain text conflict markers.
// No part of the repository's driver command is retained.
func mergeDriverNames(dir string) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, scope := range []string{"--local", "--worktree"} {
		out, _, err := runGit(context.Background(), dir, gitOpt{skipFilters: true, skipSSH: true, readOnly: true, backgroundRead: true},
			"config", scope, "--name-only", "--get-regexp", "^merge\\..*\\.driver$")
		if err != nil {
			if len(out) > 0 {
				return nil, fmt.Errorf("reading %s merge drivers: %w", scope, err)
			}
			continue
		}
		// Normalize only the namespace, retaining the untrusted subsection
		// verbatim for parseFilterDrivers to reject unsafe names.
		var keys []string
		for _, key := range strings.Split(string(out), "\n") {
			keys = append(keys, strings.Replace(key, "merge.", "filter.", 1))
		}
		parsed, err := parseFilterDrivers(strings.Join(keys, "\n"))
		if err != nil {
			return nil, err
		}
		for _, name := range parsed {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

func filterNames(dir string) ([]string, error) {
	local, err := filterNamesInScope(dir, "--local")
	if err != nil {
		return nil, err
	}
	work, err := filterNamesInScope(dir, "--worktree")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	names := make([]string, 0, len(local)+len(work))
	for _, name := range append(local, work...) {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func filterNamesInScope(dir, scope string) ([]string, error) {
	stdout, _, err := runGit(context.Background(), dir, gitOpt{skipFilters: true, skipSSH: true, readOnly: true, backgroundRead: true}, "config", scope, "--name-only", "--get-regexp", "^filter\\.")
	if err != nil {
		// git config exits 1 when nothing matches. A failure that still
		// printed keys is partial: do not continue with those filters on.
		if strings.TrimSpace(string(stdout)) != "" {
			return nil, fmt.Errorf("reading %s filter names: %w", scope, err)
		}
		return nil, nil
	}
	return parseFilterDrivers(string(stdout))
}

// filterDriver returns the subsection of a filter.<name>.<var> key.
// The name is everything between the "filter." prefix and the last dot,
// so filter.foo.bar.clean is the driver foo.bar.
func filterDriver(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "filter.")
	if !ok || rest == "" {
		return "", false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return "", false
	}
	name := rest[:i]
	if name == "" {
		return "", false
	}
	return name, true
}

func parseFilterDrivers(stdout string) ([]string, error) {
	seen := map[string]struct{}{}
	var names []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		name, ok := filterDriver(line)
		if !ok || !safeFilterName(name) {
			return nil, fmt.Errorf("refusing to run git while filter %q cannot be disabled", line)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// safeFilterName reports whether name can be placed in one -c argument
// (filter.<name>.clean=) without git splitting the key on '=' or treating
// a control character as a line break. Spaces and dots are allowed.
func safeFilterName(name string) bool {
	if name == "" || len(name) > 256 || strings.TrimSpace(name) == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c == 0x7f || c == '=' {
			return false
		}
	}
	return true
}

// pickGitLocale chooses the locale git should run under. C.UTF-8 keeps
// non-ASCII paths as UTF-8; C is the fallback when that locale is absent.
// windows is true when the process is Windows even if `locale` is missing:
// Git for Windows ships C.UTF-8, and the C locale there is a legacy code page.
func pickGitLocale(available func(string) bool, windows bool) string {
	if available != nil && (available("C.UTF-8") || available("C.utf8")) {
		return "C.UTF-8"
	}
	if windows {
		return "C.UTF-8"
	}
	return "C"
}

var (
	localeOnce sync.Once
	localeSet  map[string]struct{}
)

func localeAvailable(name string) bool {
	localeOnce.Do(func() {
		localeSet = map[string]struct{}{}
		out, err := exec.Command("locale", "-a").Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				localeSet[line] = struct{}{}
			}
		}
	})
	_, ok := localeSet[name]
	return ok
}

func gitLocale() string {
	return pickGitLocale(localeAvailable, runtime.GOOS == "windows")
}

// cancelSucceeded reports whether a cancel attempt actually signalled git.
// os.Interrupt does not signal a Windows group, and CTRL_BREAK does not run
// Git for Windows' SIGINT cleanup. Neither counts as graceful cancellation.
func cancelSucceeded(method string, err error) bool {
	if method == "os.Interrupt" || method == "ctrl-break" {
		return false
	}
	return err == nil
}

// fallbackToPidKill reports whether a failed process-group signal may be
// retried against the pid itself. A missing group (the process has exited)
// or a process that has already been reaped must not be killed by pid:
// the number may have been reused.
func fallbackToPidKill(groupMissing, processExited bool) bool {
	if processExited || groupMissing {
		return false
	}
	return true
}

// killTimer ties the post-grace kill to the git process. releaseGit marks
// it done when Wait returns so the timer cannot signal a reused pid.
type killTimer struct {
	mu    sync.Mutex
	done  bool
	timer *time.Timer
}

var killTimers sync.Map

func beginKillTimer(cmd *exec.Cmd, fn func()) {
	st := &killTimer{}
	st.timer = time.AfterFunc(gitCancelGrace, func() {
		st.mu.Lock()
		done := st.done
		st.mu.Unlock()
		if done {
			return
		}
		fn()
	})
	killTimers.Store(cmd, st)
}

func gitExited(cmd *exec.Cmd) bool {
	v, ok := killTimers.Load(cmd)
	if !ok {
		return true
	}
	st := v.(*killTimer)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.done
}

func releaseGit(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	v, ok := killTimers.LoadAndDelete(cmd)
	if ok {
		st := v.(*killTimer)
		st.mu.Lock()
		st.done = true
		timer := st.timer
		st.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
	}
	releaseGitExtra(cmd)
}

// redactGit masks credentials in text git printed (progress lines and
// errors). File blobs go through git() and are not passed here.
func redactGit(s string) string {
	return secrets.RedactGitOutput(s)
}

// lineSink captures stderr and reports complete lines. Git progress uses CR.
type lineSink struct {
	mu  sync.Mutex
	buf []byte
	all bytes.Buffer
	fn  func(string)
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.all.Write(p)
	s.buf = append(s.buf, p...)
	var lines []string
	for {
		i := indexBreak(s.buf)
		if i < 0 {
			break
		}
		lines = append(lines, string(s.buf[:i]))
		s.buf = append([]byte(nil), s.buf[i+1:]...)
	}
	fn := s.fn
	s.mu.Unlock()
	emitLines(fn, lines)
	return len(p), nil
}

func (s *lineSink) flush() {
	s.mu.Lock()
	rest := string(s.buf)
	s.buf = nil
	fn := s.fn
	s.mu.Unlock()
	if strings.TrimSpace(rest) == "" {
		return
	}
	emitLines(fn, []string{rest})
}

func (s *lineSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all.String()
}

func emitLines(fn func(string), lines []string) {
	if fn == nil {
		return
	}
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		fn(redactGit(line))
	}
}

func indexBreak(b []byte) int {
	for i, c := range b {
		if c == '\n' || c == '\r' {
			return i
		}
	}
	return -1
}

func exitCode(err error) int {
	var ce *CommandError
	if errors.As(err, &ce) {
		err = ce.Err
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func notRepository(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	return strings.Contains(strings.ToLower(ce.Stderr), "not a git repository")
}

func badRevision(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	msg := strings.ToLower(ce.Stderr)
	return strings.Contains(msg, "not a valid object name") ||
		strings.Contains(msg, "bad revision") ||
		strings.Contains(msg, "ambiguous argument") ||
		strings.Contains(msg, "needed a single revision")
}
