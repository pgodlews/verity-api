package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// PullFFOnly is the default pull: fast-forward or stop. PullMerge is a
	// merge (`--no-rebase`) the user chooses after a divergence.
	PullFFOnly = "ff-only"
	PullMerge  = "merge"
)

const identityHint = "Git does not have an author name and email for this repository. Set them with git config user.name and git config user.email, and add --global to use them for every repository. Verity does not set an identity for you."

// IdentityError means git has no author name or email. The message tells the
// user how to set it. Verity does not write git config.
type IdentityError struct{}

func (e *IdentityError) Error() string { return identityHint }

// IsIdentity reports whether err is a missing author identity.
func IsIdentity(err error) bool {
	var target *IdentityError
	return errors.As(err, &target)
}

// EmptyMessageError means Commit was asked to record a blank message.
type EmptyMessageError struct{}

func (e *EmptyMessageError) Error() string { return "commit message is required" }

// IsEmptyMessage reports whether err is a blank commit message.
func IsEmptyMessage(err error) bool {
	var target *EmptyMessageError
	return errors.As(err, &target)
}

// NoUpstreamError means the current branch does not track a remote branch.
type NoUpstreamError struct {
	Branch string
}

func (e *NoUpstreamError) Error() string {
	if e == nil || e.Branch == "" {
		return "the current branch has no upstream"
	}
	return "branch " + e.Branch + " has no upstream"
}

// IsNoUpstream reports whether err is a missing upstream.
func IsNoUpstream(err error) bool {
	var target *NoUpstreamError
	return errors.As(err, &target)
}

// PullOutcome is the result of a pull. Status is updated, up-to-date,
// diverged, or conflict. Diverged and conflict are results, not failures:
// the caller offers a merge or lists the conflicted files. Output is git's
// text with URL userinfo masked. Conflicted paths are repo-relative.
type PullOutcome struct {
	Status     string
	Conflicted []string
	Output     string
}

// PushOutcome is the result of a push. Status is pushed or no-upstream.
// When there is no upstream, Remote is the suggested remote name (origin
// when it exists) and Remotes lists every remote. Nothing is pushed until
// the caller confirms and calls PushSetUpstream. Output is git's text.
type PushOutcome struct {
	Status  string
	Remote  string
	Branch  string
	Remotes []string
	Output  string
}

// Branch is one local branch. Upstream is empty when it does not track.
type Branch struct {
	Name     string
	Upstream string
	Current  bool
	Ahead    int
	Behind   int
	// Gone is true when the upstream ref is configured but missing.
	Gone bool
}

// Stage runs `git add -- paths`. It never uses `add -A` or `add .`.
// paths are repo-relative. An empty list is an error.
func Stage(ctx context.Context, root string, paths []string) error {
	args, err := withPaths([]string{"add", "--"}, paths)
	if err != nil {
		return err
	}
	_, err = run(ctx, root, nil, args...)
	return err
}

// Unstage runs `git restore --staged -- paths`. paths are repo-relative.
func Unstage(ctx context.Context, root string, paths []string) error {
	args, err := withPaths([]string{"restore", "--staged", "--"}, paths)
	if err != nil {
		return err
	}
	_, err = run(ctx, root, nil, args...)
	return err
}

// CheckIdentity reports whether git can name an author. A missing identity
// is *IdentityError. Verity does not set user.name or user.email.
func CheckIdentity(ctx context.Context, root string) error {
	_, err := run(ctx, root, nil, "var", "GIT_AUTHOR_IDENT")
	if err == nil {
		return nil
	}
	if missingIdentity(err) {
		return &IdentityError{}
	}
	return err
}

// IndexSyncError means the commit object was written, but the real index
// could not be updated to the blobs that were recorded. The caller must
// not roll the index back to the pre-stage snapshot: HEAD already moved.
type IndexSyncError struct {
	Output string
	Err    error
}

func (e *IndexSyncError) Error() string {
	msg := "commit was recorded, but the index could not be updated"
	if e != nil && e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *IndexSyncError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsIndexSync reports whether the commit exists and only the index update failed.
func IsIndexSync(err error) bool {
	var target *IndexSyncError
	return errors.As(err, &target)
}

// Commit records the blobs already in the index for paths. It builds a
// temporary index from HEAD plus those entries and commits that, so a
// worktree edit after the scan cannot replace the scanned blob. Other
// staged paths stay in the real index and are not part of the commit.
// Hooks still run. An empty message is refused before git runs. A missing
// author identity is *IdentityError. On hook failure the returned error
// includes the hook's output and the real index is left for the caller
// to restore. add is the command that still runs clean filters; this
// commit does not hash the worktree again.
func Commit(ctx context.Context, root, message string, paths []string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", &EmptyMessageError{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := CheckIdentity(ctx, root); err != nil {
		return "", err
	}
	rels, err := cleanPaths(paths)
	if err != nil {
		return "", err
	}
	entries, err := SnapshotIndex(ctx, root, rels)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "verity-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	indexFile := filepath.Join(dir, "index")
	if err := seedCommitIndex(ctx, root, indexFile); err != nil {
		return "", err
	}
	if err := writeIndexEntries(ctx, root, indexFile, rels, entries); err != nil {
		return "", err
	}
	opt := writeOpt(nil, []string{"commit"})
	opt.indexFile = indexFile
	stdout, stderr, err := runGit(ctx, root, opt, "commit", "-m", strings.TrimSpace(message))
	text := joinText(redactGit(string(stdout)), stderr)
	if err != nil {
		return text, err
	}
	syncCtx := context.WithoutCancel(ctx)
	committed, rerr := snapshotIndex(syncCtx, root, indexFile, rels)
	if rerr != nil {
		return text, &IndexSyncError{Output: text, Err: rerr}
	}
	if rerr = RestoreIndex(syncCtx, root, rels, committed); rerr != nil {
		return text, &IndexSyncError{Output: text, Err: rerr}
	}
	return text, nil
}

func seedCommitIndex(ctx context.Context, root, indexFile string) error {
	opt := gitOpt{terminalPrompt: true, skipFilters: true, readOnly: true, indexFile: indexFile}
	_, _, err := runGit(ctx, root, opt, "read-tree", "HEAD")
	if err == nil {
		return nil
	}
	if badRevision(err) {
		_, _, err = runGit(ctx, root, opt, "read-tree", "--empty")
	}
	return err
}

func writeIndexEntries(ctx context.Context, root, indexFile string, paths []string, entries []IndexEntry) error {
	for _, p := range paths {
		if err := removeIndexPath(ctx, root, indexFile, p); err != nil {
			return err
		}
	}
	if len(entries) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString(e.Line)
		buf.WriteByte('\n')
	}
	opt := gitOpt{terminalPrompt: true, skipFilters: true, readOnly: true, indexFile: indexFile, stdin: buf.Bytes()}
	_, _, err := runGit(ctx, root, opt, "update-index", "--index-info")
	return err
}

// Fetch fetches the current upstream branch only. The refspec is explicit, so
// remote.<name>.fetch cannot move a local branch, and --no-tags ignores
// remote.<name>.tagOpt. The leading + updates the remote-tracking ref when
// it is not a fast-forward; it is not a force-push. A branch whose upstream
// ref is missing returns *NoUpstreamError. onLine may be nil.
func Fetch(ctx context.Context, root string, onLine func(string)) (string, error) {
	remote, _, upstream, err := tracking(ctx, root)
	if err != nil {
		return "", err
	}
	spec, err := fetchSpec(remote, upstream)
	if err != nil {
		return "", err
	}
	if onLine != nil {
		onLine("fetch " + remote)
	}
	// --refmap= ignores remote.<name>.fetch, so a configured
	// +refs/heads/*:refs/heads/* cannot move local branches.
	return run(ctx, root, onLine, "fetch", "--progress", "--no-tags", "--refmap=", remote, spec)
}

// Pull runs `git pull --no-rebase --no-edit` of the upstream branch and,
// unless mode is PullMerge, `--ff-only`. GIT_MERGE_AUTOEDIT=no is set so a
// merge does not open an editor. A fast-forward refusal ("Not possible to
// fast-forward") is status diverged. Network, authentication, and
// local-changes errors stay errors. A conflict is MERGE_HEAD appearing
// during this pull together with `git ls-files -u`. A merge that was
// already in progress is an error that still names the unmerged files.
// onLine may be nil.
func Pull(ctx context.Context, root, mode string, onLine func(string)) (*PullOutcome, error) {
	remote, _, upstream, err := tracking(ctx, root)
	if err != nil {
		return nil, err
	}
	if badRef(upstream) {
		return nil, fmt.Errorf("invalid upstream branch %q", upstream)
	}
	before, err := mergeHeadExists(root)
	if err != nil {
		return nil, err
	}
	args := []string{"pull", "--progress", "--no-rebase", "--no-edit", "--no-tags", "--refmap="}
	switch mode {
	case "", PullFFOnly:
		args = append(args, "--ff-only")
	case PullMerge:
	default:
		return nil, fmt.Errorf("unknown pull mode %q", mode)
	}
	spec, err := fetchSpec(remote, upstream)
	if err != nil {
		return nil, err
	}
	// A destination is required: --refmap= ignores remote.<name>.fetch, so a
	// source-only refspec would land only in FETCH_HEAD and leave @{upstream}
	// stale. The leading + force-updates the remote-tracking ref, not a
	// local branch.
	args = append(args, remote, spec)
	if onLine != nil {
		onLine("pull (" + defaultMode(mode) + ")")
	}
	text, err := run(ctx, root, onLine, args...)
	if err == nil {
		status := "updated"
		if upToDate(text) {
			status = "up-to-date"
		}
		return &PullOutcome{Status: status, Output: text}, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	after, _ := mergeHeadExists(root)
	files, _ := UnmergedPaths(ctx, root)
	out := text
	if strings.TrimSpace(out) == "" {
		out = combined(err)
	}
	if !before && after && len(files) > 0 {
		return &PullOutcome{Status: "conflict", Conflicted: files, Output: out}, nil
	}
	if before {
		return nil, &MergePendingError{Files: files, Err: err}
	}
	// Only git's fast-forward refusal is "diverged". A network, auth, or
	// "local changes would be overwritten" failure stays an error even
	// when both sides have commits.
	if fastForwardRefusal(out) || fastForwardRefusal(combined(err)) {
		return &PullOutcome{Status: "diverged", Output: out}, nil
	}
	return nil, err
}

func fastForwardRefusal(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "not possible to fast-forward")
}

// MergePendingError means pull was asked to start while a merge was already
// open. Files are the unmerged paths. Err is git's own failure.
type MergePendingError struct {
	Files []string
	Err   error
}

func (e *MergePendingError) Error() string {
	msg := ""
	if e != nil && e.Err != nil {
		msg = e.Err.Error()
	}
	if e != nil && len(e.Files) > 0 {
		if msg != "" {
			msg += "\n"
		}
		msg += "unmerged files:\n" + strings.Join(e.Files, "\n")
	}
	if msg == "" {
		return "a merge is already in progress"
	}
	return msg
}

func (e *MergePendingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Push pushes the current branch to its upstream. It never force-pushes.
// When the branch has no upstream, the outcome status is no-upstream and
// nothing is sent. onLine may be nil.
func Push(ctx context.Context, root string, onLine func(string)) (*PushOutcome, error) {
	remote, branch, upstream, err := tracking(ctx, root)
	if err != nil {
		if IsNoUpstream(err) {
			nu := &NoUpstreamError{}
			_ = errors.As(err, &nu)
			remotes, rerr := Remotes(ctx, root)
			if rerr != nil {
				return nil, rerr
			}
			return &PushOutcome{
				Status:  "no-upstream",
				Remote:  suggestRemote(remotes),
				Branch:  nu.Branch,
				Remotes: remotes,
			}, nil
		}
		return nil, err
	}
	if onLine != nil {
		onLine("push " + remote)
	}
	spec, err := pushSpec(branch, upstream)
	if err != nil {
		return nil, err
	}
	text, err := run(ctx, root, onLine, "push", "--progress", "--no-tags", "--no-follow-tags", remote, spec)
	if err != nil {
		return nil, err
	}
	return &PushOutcome{Status: "pushed", Remote: remote, Branch: branch, Output: text}, nil
}

// PushSetUpstream runs `git push -u <remote> refs/heads/<name>:refs/heads/<name>`
// after the user has confirmed. The refspec is fully qualified, so a branch
// whose name starts with '+' cannot be read as a force update. It never
// passes --force. remote is a configured remote name, not a URL.
func PushSetUpstream(ctx context.Context, root, remote, branch string, onLine func(string)) (string, error) {
	if !validRemote(remote) {
		return "", fmt.Errorf("invalid remote name %q", remote)
	}
	name, err := CheckBranch(ctx, root, branch)
	if err != nil {
		return "", err
	}
	spec, err := pushSpec(name, name)
	if err != nil {
		return "", err
	}
	if onLine != nil {
		onLine("push -u " + remote + " " + name)
	}
	return run(ctx, root, onLine, "push", "--progress", "--no-tags", "--no-follow-tags", "-u", remote, spec)
}

// Remotes lists configured remote names (`git remote`).
func Remotes(ctx context.Context, root string) ([]string, error) {
	text, err := run(ctx, root, nil, "remote")
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(text), nil
}

// Branches lists local branches with upstream tracking and ahead/behind.
func Branches(ctx context.Context, root string) ([]Branch, error) {
	// %00 is the for-each-ref NUL placeholder (git log's %xNN is not accepted).
	// Each record is still terminated by a newline.
	stdout, _, err := runGit(ctx, root, gitOpt{terminalPrompt: true}, "for-each-ref",
		"--format=%(refname:short)%00%(upstream:short)%00%(HEAD)%00%(upstream:track)",
		"refs/heads")
	if err != nil {
		return nil, err
	}
	text := string(stdout)
	if strings.TrimSpace(text) == "" {
		return []Branch{}, nil
	}
	var out []Branch
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) < 3 {
			return nil, fmt.Errorf("unrecognized branch list line")
		}
		b := Branch{
			Name:     parts[0],
			Upstream: parts[1],
			Current:  strings.TrimSpace(parts[2]) == "*",
		}
		if len(parts) > 3 {
			b.Ahead, b.Behind, b.Gone = parseTrack(parts[3])
		}
		out = append(out, b)
	}
	if out == nil {
		out = []Branch{}
	}
	return out, nil
}

// Switch runs `git switch -- <branch>`. It does not force, and it does not
// decide whether the working tree is too dirty to switch: the caller does.
func Switch(ctx context.Context, root, branch string) (string, error) {
	name, err := CheckBranch(ctx, root, branch)
	if err != nil {
		return "", err
	}
	return run(ctx, root, nil, "switch", "--", name)
}

// CreateBranch validates name with `git check-ref-format --branch` and
// creates it. switchTo checks it out (`git switch -c`); otherwise
// `git branch -- <name>` leaves HEAD where it is.
func CreateBranch(ctx context.Context, root, name string, switchTo bool) (string, error) {
	canon, err := CheckBranch(ctx, root, name)
	if err != nil {
		return "", err
	}
	if switchTo {
		return run(ctx, root, nil, "switch", "-c", canon)
	}
	return run(ctx, root, nil, "branch", "--", canon)
}

// CheckBranch validates a branch name. The returned name is git's canonical
// form. Names that start with '+' or '-' are rejected: '+' is a legal ref
// character that git would read as a force refspec, and '-' is an option.
func CheckBranch(ctx context.Context, root, name string) (string, error) {
	if badRef(name) {
		return "", fmt.Errorf("invalid branch name %q", name)
	}
	text, err := run(ctx, root, nil, "check-ref-format", "--branch", name)
	if err != nil {
		return "", err
	}
	canon := strings.TrimSpace(text)
	if badRef(canon) {
		return "", fmt.Errorf("invalid branch name %q", name)
	}
	return canon, nil
}

// MergeAbort runs `git merge --abort`, or `git rebase --abort` when a rebase
// is in progress. Verity does not continue a rebase it did not start.
func MergeAbort(ctx context.Context, root string) (string, error) {
	if rebaseInProgress(root) {
		return run(ctx, root, nil, "rebase", "--abort")
	}
	return run(ctx, root, nil, "merge", "--abort")
}

// ConflictedFiles lists repo-relative paths that are unmerged.
func ConflictedFiles(ctx context.Context, root string) ([]string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	st, err := Status(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range st.Entries {
		if e.Conflicted {
			out = append(out, e.Path)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// run executes a write (or a write-adjacent query) with the terminal prompt
// disabled. The returned text is redacted stdout and stderr. add and commit
// keep the user's clean/smudge filters. Every other command overrides them.
// Pull also sets GIT_MERGE_AUTOEDIT=no. fetch, pull, and push are not marked
// read-only, so they may contact the remote; other commands set
// GIT_NO_LAZY_FETCH=1.
func run(ctx context.Context, root string, onLine func(string), args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout, stderr, err := runGit(ctx, root, writeOpt(onLine, args), args...)
	text := joinText(redactGit(string(stdout)), stderr)
	return text, err
}

func writeOpt(onLine func(string), args []string) gitOpt {
	opt := gitOpt{terminalPrompt: true, onLine: onLine, readOnly: true}
	if len(args) == 0 {
		return opt
	}
	switch args[0] {
	case "add", "commit":
		opt.skipFilters = true
	case "fetch", "pull", "push":
		opt.readOnly = false
	}
	switch args[0] {
	case "pull":
		opt.mergeNoEdit = true
		// git fetch and git pull on some releases accept --no-tags but not
		// --no-follow-tags (push does). The config is that switch.
		opt.extraConfig = []string{"fetch.followTags=false"}
	case "stash":
		// Stash ref updates must not execute reference-transaction hooks.
		opt.extraConfig = []string{"core.hooksPath=" + os.DevNull}
	case "fetch":
		opt.extraConfig = []string{"fetch.followTags=false"}
	}
	return opt
}

func withPaths(prefix, paths []string) ([]string, error) {
	rels, err := cleanPaths(paths)
	if err != nil {
		return nil, err
	}
	out := append([]string{}, prefix...)
	for _, p := range rels {
		out = append(out, ":(literal)"+p)
	}
	return out, nil
}

func cleanPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths given")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || p == "." || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\n\r\x00") {
			return nil, fmt.Errorf("invalid git path %q", p)
		}
		if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") {
			return nil, fmt.Errorf("invalid git path %q", p)
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no paths given")
	}
	return out, nil
}

func tracking(ctx context.Context, root string) (remote, branch, upstream string, err error) {
	branch, err = currentBranch(ctx, root)
	if err != nil {
		return "", "", "", err
	}
	// branch.<name>.remote may itself contain a slash. The abbreviated
	// @{upstream} form is <remote>/<branch> and cannot be split safely.
	remote, ok, err := configGet(ctx, root, "branch."+branch+".remote")
	if err != nil {
		return "", branch, "", err
	}
	merge, mergeOK, merr := configGet(ctx, root, "branch."+branch+".merge")
	if merr != nil {
		return "", branch, "", merr
	}
	upstream, heads := strings.CutPrefix(merge, "refs/heads/")
	if !ok || !mergeOK || !heads || upstream == "" || !validRemote(remote) || badRef(upstream) {
		return "", branch, "", &NoUpstreamError{Branch: branch}
	}
	// A configured upstream whose remote-tracking ref is missing is the
	// "gone" case (or a tracking ref that was never fetched). Do not push
	// or fetch until the user confirms set-upstream. That also refuses to
	// recreate a deleted remote branch.
	ref := "refs/remotes/" + remote + "/" + upstream
	if _, err := run(ctx, root, nil, "rev-parse", "--verify", "--quiet", ref); err != nil {
		if upstreamMissing(err) || upstreamRefMissing(err) || exitCode(err) == 1 {
			return "", branch, "", &NoUpstreamError{Branch: branch}
		}
		return "", branch, "", err
	}
	return remote, branch, upstream, nil
}

func configGet(ctx context.Context, root, key string) (string, bool, error) {
	if key == "" || strings.HasPrefix(key, "-") || strings.ContainsAny(key, "\n\r\x00") {
		return "", false, fmt.Errorf("invalid config key")
	}
	text, err := run(ctx, root, nil, "config", "--get", key)
	if err != nil {
		if exitCode(err) == 1 {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(text), true, nil
}

func currentBranch(ctx context.Context, root string) (string, error) {
	text, err := run(ctx, root, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(text)
	if name == "" || name == "HEAD" {
		return "", fmt.Errorf("HEAD is detached")
	}
	return name, nil
}

func missingIdentity(err error) bool {
	msg := strings.ToLower(combined(err))
	return strings.Contains(msg, "author identity unknown") ||
		strings.Contains(msg, "empty ident") ||
		strings.Contains(msg, "unable to auto-detect email") ||
		strings.Contains(msg, "tell me who you are")
}

func upstreamMissing(err error) bool {
	msg := strings.ToLower(combined(err))
	return strings.Contains(msg, "no upstream") || strings.Contains(msg, "no tracking information")
}

func upstreamRefMissing(err error) bool {
	msg := strings.ToLower(combined(err))
	return strings.Contains(msg, "unknown revision") || strings.Contains(msg, "ambiguous argument")
}

func upToDate(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "already up to date") || strings.Contains(l, "already up-to-date")
}

func combined(err error) string {
	if err == nil {
		return ""
	}
	var ce *CommandError
	if errors.As(err, &ce) {
		return joinText(ce.Stdout, ce.Stderr)
	}
	return err.Error()
}

func joinText(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n" + stderr
	case stdout != "":
		return stdout
	default:
		return stderr
	}
}

func nonEmptyLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line != "" {
			out = append(out, line)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func suggestRemote(remotes []string) string {
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	return ""
}

func validRemote(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/':
		default:
			return false
		}
	}
	return true
}

func parseTrack(s string) (ahead, behind int, gone bool) {
	s = strings.Trim(s, "[] ")
	if s == "" {
		return 0, 0, false
	}
	if s == "gone" {
		return 0, 0, true
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		var n int
		switch {
		case strings.HasPrefix(part, "ahead "):
			fmt.Sscanf(part, "ahead %d", &n)
			ahead = n
		case strings.HasPrefix(part, "behind "):
			fmt.Sscanf(part, "behind %d", &n)
			behind = n
		}
	}
	return ahead, behind, false
}

func pushSpec(local, upstream string) (string, error) {
	if badRef(local) || badRef(upstream) {
		return "", fmt.Errorf("invalid branch name")
	}
	return "refs/heads/" + local + ":refs/heads/" + upstream, nil
}

func fetchSpec(remote, upstream string) (string, error) {
	if !validRemote(remote) || badRef(upstream) {
		return "", fmt.Errorf("invalid fetch refspec")
	}
	// The leading + force-updates the remote-tracking ref only.
	return "+refs/heads/" + upstream + ":refs/remotes/" + remote + "/" + upstream, nil
}

func badRef(name string) bool {
	return name == "" || strings.HasPrefix(name, "+") || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " :\t\n\r\x00")
}

func mergeHeadExists(root string) (bool, error) {
	return gitPathExists(root, "MERGE_HEAD", false)
}

func rebaseInProgress(root string) bool {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		ok, err := gitPathExists(root, name, true)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func gitPathExists(root, name string, wantDir bool) (bool, error) {
	p, err := GitPath(root, name)
	if err != nil {
		if IsNotRepository(err) || errors.Is(err, ErrUnavailable) {
			return false, err
		}
		return false, nil
	}
	st, err := os.Stat(p)
	if err != nil {
		return false, nil
	}
	if wantDir {
		return st.IsDir(), nil
	}
	return !st.IsDir(), nil
}

func defaultMode(mode string) string {
	if mode == "" {
		return PullFFOnly
	}
	return mode
}
