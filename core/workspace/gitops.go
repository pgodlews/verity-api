package workspace

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

// GitProgress is the desktop "git:progress" event. Op is fetch, pull, or
// push. Line is a short status line: "started", "finished", or one redacted
// line from git --progress.
type GitProgress struct {
	Op   string `json:"op"`
	Line string `json:"line"`
}

// GitOps runs one desktop git operation at a time. A second Run while one
// is in flight returns a busy error. Cancel cancels the in-flight context.
type GitOps struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Run calls fn with a cancellable context. A second call while fn is still
// running returns verr.CodeBusy.
func (g *GitOps) Run(ctx context.Context, fn func(context.Context) error) error {
	if g == nil {
		return fn(ctx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	g.mu.Lock()
	if g.cancel != nil {
		g.mu.Unlock()
		return verr.New(verr.CodeBusy, "a git operation is already in progress")
	}
	runCtx, cancel := context.WithCancel(ctx)
	g.cancel = cancel
	g.mu.Unlock()
	defer func() {
		cancel()
		g.mu.Lock()
		g.cancel = nil
		g.mu.Unlock()
	}()
	return fn(runCtx)
}

// Cancel cancels the in-flight Run, if any.
func (g *GitOps) Cancel() {
	if g == nil {
		return
	}
	g.mu.Lock()
	cancel := g.cancel
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CommitRequest is a desktop commit. Path is the project file. Paths are
// the files to stage, relative to the project directory (the same paths
// GitStatus reports). Message is required.
type CommitRequest struct {
	Path    string   `json:"path"`
	Message string   `json:"message"`
	Paths   []string `json:"paths"`
}

// CommitResult is a commit that was recorded. Output is git's summary.
type CommitResult struct {
	Output string `json:"output,omitempty"`
}

// Commit stages exactly the requested paths, scans that index the way
// `verity scan-secrets --staged` does, and only then commits those blobs.
// A finding or a staged dotenv file refuses the commit even when no hook
// is installed. Hooks still run, and their output is returned on failure.
// A scan refusal, a hook failure, or a cancel restores the index entries
// from before the stage, including a different blob of the same path and
// unmerged stages. Verity does not pass --no-verify and does not set the
// author identity.
func Commit(ctx context.Context, req CommitRequest) (*CommitResult, error) {
	if strings.TrimSpace(req.Message) == "" {
		return nil, verr.New(verr.CodeValidation, "commit message is required")
	}
	if len(req.Paths) == 0 {
		return nil, verr.New(verr.CodeValidation, "select at least one file to commit")
	}
	root, rels, err := repoPaths(req.Path, req.Paths)
	if err != nil {
		return nil, err
	}
	unlock := lockProject(req.Path)
	defer unlock()
	if err := vcs.CheckIdentity(ctx, root); err != nil {
		return nil, gitErr(err)
	}
	before, err := vcs.SnapshotIndex(ctx, root, rels)
	if err != nil {
		return nil, gitErr(err)
	}
	// RestoreIndex detaches cancellation, and retries while index.lock is held.
	restore := func() error {
		return vcs.RestoreIndex(ctx, root, rels, before)
	}
	if err := vcs.Stage(ctx, root, rels); err != nil {
		return nil, withIndexRestore(err, restore())
	}
	scan, err := ScanCommit(req.Path, rels)
	if err != nil {
		return nil, withIndexRestore(err, restore())
	}
	if !scan.Clean() {
		if rerr := restore(); rerr != nil {
			return nil, verr.Wrap(verr.CodeValidation, rerr, "commit refused, and restoring the index failed")
		}
		return nil, secretsRefusal(scan)
	}
	text, err := vcs.Commit(ctx, root, req.Message, rels)
	if err != nil {
		// The commit object exists; rolling the index back would hide it.
		if vcs.IsIndexSync(err) {
			return nil, gitErr(err)
		}
		return nil, withIndexRestore(err, restore())
	}
	return &CommitResult{Output: text}, nil
}

func withIndexRestore(err, rerr error) error {
	if rerr == nil {
		if _, ok := verr.As(err); ok {
			return err
		}
		return gitErr(err)
	}
	code := verr.CodeGit
	msg := "restoring the index failed"
	if err != nil {
		if ve, ok := verr.As(err); ok {
			code = ve.Code
		}
		text := err.Error()
		if _, ok := verr.As(err); !ok {
			text = gitText(err)
		}
		if text != "" {
			msg = text + "\nrestoring the index failed"
		}
	}
	return verr.Wrap(code, rerr, msg)
}

// PullRequest asks for a pull. Mode is "ff-only" (the default) or "merge".
// BeforeRewrite runs immediately before git pull, which rewrites files.
// It is not part of the JSON API. Nil is fine.
type PullRequest struct {
	Path          string `json:"path"`
	Mode          string `json:"mode"`
	BeforeRewrite func() `json:"-"`
}

// PullResult is a pull. Status is updated, up-to-date, diverged, or conflict.
// Diverged means the GUI can offer a merge. Conflicted paths are relative to
// the project directory. Output is git's text.
type PullResult struct {
	Status     string   `json:"status"`
	Conflicted []string `json:"conflicted,omitempty"`
	Output     string   `json:"output,omitempty"`
}

// Pull fast-forwards, or merges when Mode is "merge". Divergence and
// conflicts are statuses on the result. onLine may be nil.
func Pull(ctx context.Context, req PullRequest, onLine func(string)) (*PullResult, error) {
	unlock := lockProject(req.Path)
	defer unlock()
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	mode := req.Mode
	if mode == "" {
		mode = vcs.PullFFOnly
	}
	if req.BeforeRewrite != nil {
		req.BeforeRewrite()
	}
	out, err := vcs.Pull(ctx, root, mode, onLine)
	if err != nil {
		return nil, gitErr(err)
	}
	return &PullResult{
		Status:     out.Status,
		Conflicted: relPaths(req.Path, root, out.Conflicted),
		Output:     out.Output,
	}, nil
}

// FetchResult is a completed fetch. Output is git's text.
type FetchResult struct {
	Output string `json:"output,omitempty"`
}

// Fetch fetches the current upstream remote only. onLine may be nil.
func Fetch(ctx context.Context, projectPath string, onLine func(string)) (*FetchResult, error) {
	unlock := lockProject(projectPath)
	defer unlock()
	root, err := repoRootOf(projectPath)
	if err != nil {
		return nil, err
	}
	text, err := vcs.Fetch(ctx, root, onLine)
	if err != nil {
		return nil, gitErr(err)
	}
	return &FetchResult{Output: text}, nil
}

// PushResult is a push. Status is pushed or no-upstream. no-upstream does
// not send anything; Remote and Branch are what a confirmed
// PushUpstream would use. Remotes lists configured remote names.
type PushResult struct {
	Status  string   `json:"status"`
	Remote  string   `json:"remote,omitempty"`
	Branch  string   `json:"branch,omitempty"`
	Remotes []string `json:"remotes,omitempty"`
	Output  string   `json:"output,omitempty"`
}

// Push pushes to the upstream. It never force-pushes. A missing upstream
// is status no-upstream. onLine may be nil.
func Push(ctx context.Context, projectPath string, onLine func(string)) (*PushResult, error) {
	unlock := lockProject(projectPath)
	defer unlock()
	root, err := repoRootOf(projectPath)
	if err != nil {
		return nil, err
	}
	out, err := vcs.Push(ctx, root, onLine)
	if err != nil {
		return nil, gitErr(err)
	}
	return pushResult(out), nil
}

// UpstreamRequest is a confirmed `git push -u`. Remote is a remote name,
// not a URL. Branch is the local branch.
type UpstreamRequest struct {
	Path   string `json:"path"`
	Remote string `json:"remote"`
	Branch string `json:"branch"`
}

// PushUpstream runs `git push -u` after the user has confirmed. It never
// force-pushes. onLine may be nil.
func PushUpstream(ctx context.Context, req UpstreamRequest, onLine func(string)) (*PushResult, error) {
	unlock := lockProject(req.Path)
	defer unlock()
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	text, err := vcs.PushSetUpstream(ctx, root, req.Remote, req.Branch, onLine)
	if err != nil {
		return nil, gitErr(err)
	}
	return &PushResult{Status: "pushed", Remote: req.Remote, Branch: req.Branch, Output: text}, nil
}

// BranchInfo is one local branch for the branch picker.
type BranchInfo struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Gone     bool   `json:"gone,omitempty"`
}

// Branches lists local branches and their upstream tracking.
func Branches(ctx context.Context, projectPath string) ([]BranchInfo, error) {
	root, err := repoRootOf(projectPath)
	if err != nil {
		return nil, err
	}
	list, err := vcs.Branches(ctx, root)
	if err != nil {
		return nil, gitErr(err)
	}
	out := make([]BranchInfo, len(list))
	for i, b := range list {
		out[i] = BranchInfo{Name: b.Name, Current: b.Current, Upstream: b.Upstream, Ahead: b.Ahead, Behind: b.Behind, Gone: b.Gone}
	}
	return out, nil
}

// SwitchRequest checks out a local branch. Force is the user's "switch
// anyway" after a dirty refusal. Git may still refuse if it would overwrite
// a file; that error is returned. BeforeRewrite runs only when a checkout
// is about to start, not when the dirty check stops it. It is not part of
// the JSON API.
type SwitchRequest struct {
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	Force         bool   `json:"force"`
	BeforeRewrite func() `json:"-"`
}

// CreateBranchRequest creates a branch. SwitchTo checks it out. Force is
// "switch anyway" when SwitchTo is set and project files are dirty.
// BeforeRewrite runs only when that checkout is about to start.
type CreateBranchRequest struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	SwitchTo      bool   `json:"switchTo"`
	Force         bool   `json:"force"`
	BeforeRewrite func() `json:"-"`
}

// BranchWriteResult is a switch or branch creation. Status is switched,
// created, or dirty. Dirty lists project files with uncommitted changes
// and does not change HEAD.
type BranchWriteResult struct {
	Status string   `json:"status"`
	Files  []string `json:"files,omitempty"`
	Output string   `json:"output,omitempty"`
}

// Switch checks out a branch. Uncommitted changes to project files refuse
// the switch (status dirty) unless Force is set.
func Switch(ctx context.Context, req SwitchRequest) (*BranchWriteResult, error) {
	unlock := lockProject(req.Path)
	defer unlock()
	if !req.Force {
		if res, err := dirtyRefusal(req.Path); res != nil || err != nil {
			return res, err
		}
	}
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	if req.BeforeRewrite != nil {
		req.BeforeRewrite()
	}
	text, err := vcs.Switch(ctx, root, req.Branch)
	if err != nil {
		return nil, gitErr(err)
	}
	return &BranchWriteResult{Status: "switched", Output: text}, nil
}

// CreateBranch creates a branch, and checks it out when SwitchTo is set.
// A checkout is refused the same way as Switch when project files are dirty.
func CreateBranch(ctx context.Context, req CreateBranchRequest) (*BranchWriteResult, error) {
	unlock := lockProject(req.Path)
	defer unlock()
	if req.SwitchTo && !req.Force {
		if res, err := dirtyRefusal(req.Path); res != nil || err != nil {
			return res, err
		}
	}
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	if req.SwitchTo && req.BeforeRewrite != nil {
		req.BeforeRewrite()
	}
	text, err := vcs.CreateBranch(ctx, root, req.Name, req.SwitchTo)
	if err != nil {
		return nil, gitErr(err)
	}
	status := "created"
	if req.SwitchTo {
		status = "switched"
	}
	return &BranchWriteResult{Status: status, Output: text}, nil
}

// MergeAbort runs `git merge --abort`. before runs immediately before git
// when it is non-nil, so a desktop watcher can stop treating the rewrite as
// a self-write. Nil is fine.
func MergeAbort(ctx context.Context, projectPath string, before func()) (string, error) {
	unlock := lockProject(projectPath)
	defer unlock()
	root, err := repoRootOf(projectPath)
	if err != nil {
		return "", err
	}
	if before != nil {
		before()
	}
	text, err := vcs.MergeAbort(ctx, root)
	if err != nil {
		return "", gitErr(err)
	}
	return text, nil
}

// Conflicted lists unmerged paths relative to the project directory.
func Conflicted(ctx context.Context, projectPath string) ([]string, error) {
	root, err := repoRootOf(projectPath)
	if err != nil {
		return nil, err
	}
	files, err := vcs.ConflictedFiles(ctx, root)
	if err != nil {
		return nil, gitErr(err)
	}
	return relPaths(projectPath, root, files), nil
}

// FileURL is a file URL for a path inside the work tree, relative to the
// project directory. The desktop app opens it with the system handler.
func FileURL(projectPath, rel string) (string, error) {
	root, rels, err := repoPaths(projectPath, []string{rel})
	if err != nil {
		return "", err
	}
	abs := filepath.Clean(filepath.Join(root, filepath.FromSlash(rels[0])))
	path := filepath.ToSlash(abs)
	// A drive letter is part of the URL path, not a URL authority.
	if volume := filepath.VolumeName(abs); volume != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String(), nil
}

func dirtyRefusal(projectPath string) (*BranchWriteResult, error) {
	files, err := dirtyProjectFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	return &BranchWriteResult{Status: "dirty", Files: files}, nil
}

func dirtyProjectFiles(projectPath string) ([]string, error) {
	info, err := GitStatus(projectPath)
	if err != nil {
		return nil, err
	}
	if info == nil || !info.Available {
		return nil, verr.New(verr.CodeConfig, "git was not found on PATH; install Git to use source control")
	}
	if !info.Repo {
		return nil, verr.New(verr.CodeConfig, "this project is not in a git repository")
	}
	var out []string
	for _, f := range info.Files {
		if f.ProjectFile {
			out = append(out, f.Path)
		}
	}
	return out, nil
}

func repoRootOf(projectPath string) (string, error) {
	_, dir, err := projectDir(projectPath)
	if err != nil {
		return "", err
	}
	if !vcs.Available() {
		return "", verr.New(verr.CodeConfig, "git was not found on PATH; install Git to use source control")
	}
	root, err := vcs.RepoRoot(dir)
	if err != nil {
		return "", gitErr(err)
	}
	return root, nil
}

func repoPaths(projectPath string, paths []string) (root string, rels []string, err error) {
	root, err = repoRootOf(projectPath)
	if err != nil {
		return "", nil, err
	}
	_, dir, err := projectDir(projectPath)
	if err != nil {
		return "", nil, err
	}
	seen := map[string]struct{}{}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "\n\r\x00") {
			return "", nil, verr.Newf(verr.CodeValidation, "invalid path %q", p)
		}
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(dir, filepath.FromSlash(p))
		}
		rel, rerr := repoRelative(root, full)
		if rerr != nil {
			return "", nil, rerr
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		rels = append(rels, rel)
	}
	if len(rels) == 0 {
		return "", nil, verr.New(verr.CodeValidation, "select at least one file to commit")
	}
	return root, rels, nil
}

func relPaths(projectPath, root string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	_, dir, err := projectDir(projectPath)
	if err != nil {
		return append([]string{}, paths...)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs := filepath.Join(root, filepath.FromSlash(p))
		out = append(out, relToProject(dir, abs))
	}
	return out
}

func pushResult(out *vcs.PushOutcome) *PushResult {
	if out == nil {
		return &PushResult{}
	}
	return &PushResult{
		Status:  out.Status,
		Remote:  out.Remote,
		Branch:  out.Branch,
		Remotes: out.Remotes,
		Output:  out.Output,
	}
}

func gitErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := verr.As(err); ok {
		return err
	}
	if vcs.IsIdentity(err) {
		return verr.New(verr.CodeConfig, err.Error())
	}
	if vcs.IsEmptyMessage(err) {
		return verr.New(verr.CodeValidation, err.Error())
	}
	if vcs.IsNoUpstream(err) {
		return verr.New(verr.CodeGit, err.Error())
	}
	if errors.Is(err, vcs.ErrUnavailable) {
		return verr.New(verr.CodeConfig, "git was not found on PATH; install Git to use source control")
	}
	if vcs.IsNotRepository(err) {
		return verr.New(verr.CodeConfig, "this project is not in a git repository")
	}
	if errors.Is(err, context.Canceled) {
		return verr.New(verr.CodeGit, "Git operation cancelled.")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return verr.New(verr.CodeGit, "Git operation timed out.")
	}
	return verr.New(verr.CodeGit, gitText(err))
}

func gitText(err error) string {
	if err == nil {
		return ""
	}
	var pending *vcs.MergePendingError
	if errors.As(err, &pending) {
		return pending.Error()
	}
	var ce *vcs.CommandError
	if errors.As(err, &ce) {
		msg := strings.TrimSpace(ce.Stdout)
		errText := strings.TrimSpace(ce.Stderr)
		switch {
		case msg != "" && errText != "":
			return msg + "\n" + errText
		case msg != "":
			return msg
		case errText != "":
			return errText
		}
	}
	return err.Error()
}
