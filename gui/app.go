package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/load"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	_ "github.com/pgodlews/verity-api/core/secrets/oskeychain" // OS credential store for ${secret:keychain:...}
	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
	"github.com/pgodlews/verity-api/core/watchfs"
	"github.com/pgodlews/verity-api/core/workspace"
)

// App is the Wails-bound backend. Every method is a thin delegate to
// core/workspace — the same functions `verity gui-call` exposes to the
// browser dev bridge — so the GUI contains no protocol, auth, assertion or
// reporting logic of its own, and results reach the frontend already redacted.
type App struct {
	ctx        context.Context
	runs       workspace.Session
	sessions   workspace.OAuthSessions
	keychain   secrets.Keychain
	onLoadLive func(load.LiveStats)
	plugins    *pluginTrust

	mu          sync.Mutex
	watchCancel context.CancelFunc
	suppress    *watchfs.Suppress
	git         workspace.GitOps
}

// NewApp constructs the app.
func NewApp() *App {
	a := &App{suppress: watchfs.NewSuppress()}
	a.plugins = &pluginTrust{store: &plugin.TrustStore{Path: trustStorePath()}, confirm: a.confirmPlugins}
	a.runs.TrustPlugins = a.trustPlugins
	return a
}

// trustPlugins decides whether a project's .verity/plugins.json may run. An
// App built without NewApp refuses every project plugins file.
func (a *App) trustPlugins(fp plugin.Fingerprint) (bool, error) {
	if a.plugins == nil {
		return false, nil
	}
	return a.plugins.trust(fp)
}

// startup is called by Wails when the app launches.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown stops the project watcher when the window closes.
func (a *App) shutdown(context.Context) {
	a.UnwatchProject()
}

func (a *App) context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// Catalog returns the registered extension types (mirrors `verity list`).
func (a *App) Catalog() registry.Catalog {
	return workspace.Catalog()
}

// ValidateProject loads and validates a project file (mirrors `verity validate`).
// environment is the environment selected in the desktop app.
func (a *App) ValidateProject(path, environment string) workspace.ValidateResult {
	return workspace.Validate(a.context(), path, environment, &a.sessions)
}

// GraphQLTemplates lists operations that can be inserted into a graphql step.
func (a *App) GraphQLTemplates(req workspace.GraphQLTemplateRequest) ([]graphql.Template, error) {
	return workspace.GraphQLTemplates(a.context(), req, &a.sessions)
}

// InspectProject loads a project and reports its secret references,
// undefined variables and run-time captures.
func (a *App) InspectProject(path string) (*workspace.ProjectInfo, error) {
	return workspace.Inspect(path)
}

// RunProject executes a project, or part of it (suite/case filters and
// until-step power the request editor's Send). It returns the final
// redacted result and, while the run is in flight, emits "run:event"
// (spi.RunEvent) for the frontend. A second call while one run is in
// progress returns a busy error. CancelRun cancels the in-flight run.
func (a *App) RunProject(opts workspace.RunOptions) (*spi.RunResult, error) {
	return a.runs.Run(a.context(), opts, &a.sessions, func(ev spi.RunEvent) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "run:event", ev)
		}
	})
}

// CancelRun cancels the in-flight RunProject. It does nothing when no run
// is in progress.
func (a *App) CancelRun() {
	a.runs.Cancel()
}

// StartLoad executes a load test and returns the final redacted report. While
// virtual users are running it emits "load:live" (load.LiveStats) about once
// a second. A second call while a run or load test is in progress returns a
// busy error. CancelLoad cancels the in-flight load test.
func (a *App) StartLoad(opts workspace.LoadOptions) (*load.Report, error) {
	return a.runs.Load(a.context(), opts, &a.sessions, a.emitLoadLive)
}

// CancelLoad cancels the in-flight StartLoad. It does nothing when no load
// test is in progress.
func (a *App) CancelLoad() {
	a.runs.Cancel()
}

// LoadTargets resolves the target hosts for the selected suites, cases, and
// environment and reports any non-private targets without sending traffic.
func (a *App) LoadTargets(opts workspace.LoadOptions) (*load.TargetReport, error) {
	return workspace.LoadTargets(a.context(), opts, &a.sessions, a.trustPlugins)
}

// SaveLoadReport writes a load report JSON file to disk. When req.Out is
// empty in the desktop app, the native save-file dialog picks the destination
// ("" means the user cancelled).
func (a *App) SaveLoadReport(req workspace.SaveLoadReportRequest) (string, error) {
	if strings.TrimSpace(req.Out) == "" && a.ctx != nil {
		chosen, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
			Title:           "Save load report",
			DefaultFilename: "load-report.json",
			Filters: []runtime.FileFilter{
				{DisplayName: "JSON files (*.json)", Pattern: "*.json"},
				{DisplayName: "All files", Pattern: "*"},
			},
		})
		if err != nil || strings.TrimSpace(chosen) == "" {
			return "", err
		}
		req.Out = chosen
		req.Overwrite = true
	}
	return workspace.SaveLoadReport(req)
}

func (a *App) emitLoadLive(st load.LiveStats) {
	if a.onLoadLive != nil {
		a.onLoadLive(st)
	}
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "load:live", st)
}

// CheckStep lists the plaintext credentials in a step being edited, so the
// editor can mark those fields and offer ${env:NAME} instead. SaveStep
// refuses a step that has any.
func (a *App) CheckStep(step model.Step) ([]secretscan.Finding, error) {
	return workspace.CheckStep(step)
}

// KeychainSecretExists reports existence only; credentials never cross this binding.
func (a *App) KeychainSecretExists(name string) (bool, error) {
	return a.keychain.Exists(name)
}

// StoreKeychainSecret stores a credential without writing it into the project.
// The editor replaces its draft only after this call succeeds.
func (a *App) StoreKeychainSecret(name, value string) (string, error) {
	if err := a.keychain.Set(name, value); err != nil {
		return "", err
	}
	return "${secret:keychain:" + name + "}", nil
}

// SaveStep writes one edited step back to its YAML/JSON file.
// The desktop editor requires a step baseline or, when that step has no
// hash, the file baseline. workspace.SaveStep still accepts neither for
// CLI and dev-bridge callers that never recorded a hash. Skipping both
// checks would let a save overwrite a concurrent edit.
func (a *App) SaveStep(req workspace.SaveStepRequest) (*workspace.SaveStepResult, error) {
	if strings.TrimSpace(req.Baseline) == "" && strings.TrimSpace(req.FileBaseline) == "" {
		return nil, verr.New(verr.CodeValidation, "reload this request before saving; the editor has no baseline hash")
	}
	var marked []string
	req.BeforeWrite = a.noteWrite(&marked)
	res, err := workspace.SaveStep(req)
	if err != nil {
		a.unmark(marked)
	}
	a.finishWrite(marked)
	return res, err
}

// SaveEnvironment edits or creates an environment (comment-preserving).
// A missing baseline is rejected here. workspace.SaveEnvironment still
// accepts a nil baseline for CLI and dev-bridge callers.
func (a *App) SaveEnvironment(req workspace.SaveEnvironmentRequest) (*workspace.SaveStepResult, error) {
	if len(req.Baseline) == 0 {
		return nil, verr.New(verr.CodeValidation, "reload this environment before saving; the editor has no baseline hash")
	}
	var marked []string
	req.BeforeWrite = a.noteWrite(&marked)
	res, err := workspace.SaveEnvironment(req)
	if err != nil {
		a.unmark(marked)
	}
	a.finishWrite(marked)
	return res, err
}

// GitStatus is the read-only branch, ahead/behind, and per-request summary.
func (a *App) GitStatus(path string) (*workspace.GitInfo, error) {
	return workspace.GitStatus(path)
}

// GitChanges is the semantic diff of the working tree against HEAD.
func (a *App) GitChanges(path string) (*workspace.ProjectChanges, error) {
	return workspace.GitChanges(path)
}

// GitCommit stages the selected paths, scans the index for plaintext
// secrets, then commits with hooks enabled.
func (a *App) GitCommit(req workspace.CommitRequest) (*workspace.CommitResult, error) {
	var out *workspace.CommitResult
	err := a.git.Run(a.context(), func(ctx context.Context) error {
		var err error
		out, err = workspace.Commit(ctx, req)
		return err
	})
	return out, err
}

// GitPull fast-forwards, or merges when req.Mode is "merge".
func (a *App) GitPull(req workspace.PullRequest) (*workspace.PullResult, error) {
	req.BeforeRewrite = a.noteGitRewrite
	var out *workspace.PullResult
	err := a.gitRun("pull", func(ctx context.Context, line func(string)) error {
		var err error
		out, err = workspace.Pull(ctx, req, line)
		return err
	})
	return out, err
}

// GitFetch fetches the current upstream remote.
func (a *App) GitFetch(path string) (*workspace.FetchResult, error) {
	var out *workspace.FetchResult
	err := a.gitRun("fetch", func(ctx context.Context, line func(string)) error {
		var err error
		out, err = workspace.Fetch(ctx, path, line)
		return err
	})
	return out, err
}

// GitPush pushes to the upstream, or reports that one must be set.
func (a *App) GitPush(path string) (*workspace.PushResult, error) {
	var out *workspace.PushResult
	err := a.gitRun("push", func(ctx context.Context, line func(string)) error {
		var err error
		out, err = workspace.Push(ctx, path, line)
		return err
	})
	return out, err
}

// GitPushUpstream runs git push -u after the user confirms. It never force-pushes.
func (a *App) GitPushUpstream(req workspace.UpstreamRequest) (*workspace.PushResult, error) {
	var out *workspace.PushResult
	err := a.gitRun("push", func(ctx context.Context, line func(string)) error {
		var err error
		out, err = workspace.PushUpstream(ctx, req, line)
		return err
	})
	return out, err
}

// GitBranches lists local branches and their upstream tracking.
func (a *App) GitBranches(path string) ([]workspace.BranchInfo, error) {
	return workspace.Branches(a.context(), path)
}

// GitSwitch checks out a branch, refusing when project files are dirty
// unless the user chose to switch anyway.
func (a *App) GitSwitch(req workspace.SwitchRequest) (*workspace.BranchWriteResult, error) {
	req.BeforeRewrite = a.noteGitRewrite
	var out *workspace.BranchWriteResult
	err := a.git.Run(a.context(), func(ctx context.Context) error {
		var err error
		out, err = workspace.Switch(ctx, req)
		return err
	})
	return out, err
}

// GitCreateBranch creates a branch and optionally checks it out.
func (a *App) GitCreateBranch(req workspace.CreateBranchRequest) (*workspace.BranchWriteResult, error) {
	req.BeforeRewrite = a.noteGitRewrite
	var out *workspace.BranchWriteResult
	err := a.git.Run(a.context(), func(ctx context.Context) error {
		var err error
		out, err = workspace.CreateBranch(ctx, req)
		return err
	})
	return out, err
}

// GitMergeAbort aborts the in-progress merge.
func (a *App) GitMergeAbort(path string) (string, error) {
	var out string
	err := a.git.Run(a.context(), func(ctx context.Context) error {
		var err error
		out, err = workspace.MergeAbort(ctx, path, a.noteGitRewrite)
		return err
	})
	return out, err
}

// GitStashes lists stash ids and redacted metadata.
func (a *App) GitStashes(path string) ([]vcs.Stash, error) {
	return workspace.Stashes(a.context(), path)
}

// GitStash saves tracked changes and optionally untracked files.
func (a *App) GitStash(req workspace.StashRequest) (*workspace.PullResult, error) {
	req.BeforeRewrite = a.noteGitRewrite
	var out *workspace.PullResult
	err := a.gitRun("stash", func(ctx context.Context, line func(string)) error {
		var err error
		out, err = workspace.Stash(ctx, req)
		return err
	})
	return out, err
}

// GitStashAction applies, pops, or drops an immutable stash id.
func (a *App) GitStashAction(req workspace.StashActionRequest) (*workspace.PullResult, error) {
	req.BeforeRewrite = a.noteGitRewrite
	var out *workspace.PullResult
	err := a.gitRun("stash-"+req.Action, func(ctx context.Context, line func(string)) error {
		var err error
		out, err = workspace.StashAction(ctx, req)
		return err
	})
	return out, err
}

// GitStashChanges returns the same redacted semantic diff as GitChanges.
func (a *App) GitStashChanges(req workspace.StashActionRequest) (*workspace.ProjectChanges, error) {
	return workspace.StashChanges(a.context(), req)
}

// CancelGit cancels the in-flight fetch, pull, push, or other git operation.
func (a *App) CancelGit() {
	a.git.Cancel()
}

// OpenFile opens a work-tree file with the system handler. Conflicts are
// resolved in the user's editor, not in Verity.
func (a *App) OpenFile(projectPath, rel string) error {
	target, err := workspace.FileURL(projectPath, rel)
	if err != nil {
		return err
	}
	runtime.BrowserOpenURL(a.context(), target)
	return nil
}

func (a *App) gitRun(op string, fn func(ctx context.Context, line func(string)) error) error {
	return a.git.Run(a.context(), func(ctx context.Context) error {
		a.emitGit(op, "started")
		err := fn(ctx, func(line string) { a.emitGit(op, line) })
		if err == nil {
			a.emitGit(op, "finished")
		}
		return err
	})
}

func (a *App) emitGit(op, line string) {
	if a.ctx == nil || strings.TrimSpace(line) == "" {
		return
	}
	runtime.EventsEmit(a.ctx, "git:progress", workspace.GitProgress{Op: op, Line: line})
}

// WatchProject watches the project's source files plus the worktree HEAD and
// index, and emits "project:changed" after a short debounce. A file this app
// just saved is not emitted. Calling it again replaces the previous watch.
func (a *App) WatchProject(path string) error {
	if strings.TrimSpace(path) == "" {
		return verr.New(verr.CodeConfig, "project path is required")
	}
	a.UnwatchProject()
	w, err := watchfs.Start()
	if err != nil {
		return verr.Wrap(verr.CodeInternal, err, "watch project files")
	}
	ctx, cancel := context.WithCancel(a.context())
	a.mu.Lock()
	a.watchCancel = cancel
	a.mu.Unlock()
	go a.runWatch(ctx, w, path)
	return nil
}

// UnwatchProject stops the watcher started by WatchProject.
func (a *App) UnwatchProject() {
	a.mu.Lock()
	cancel := a.watchCancel
	a.watchCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// noteGitRewrite drops save hashes before a pull, checkout, or merge abort
// replaces working-tree files. Those bytes must not be classified as the
// earlier save, even when they match it.
func (a *App) noteGitRewrite() {
	if a.suppress != nil {
		a.suppress.Forget()
	}
}

// noteWrite records a content hash before the file is renamed into place.
func (a *App) noteWrite(marked *[]string) func(string, []byte) {
	return func(path string, data []byte) {
		if a.suppress == nil || path == "" {
			return
		}
		a.suppress.Begin(path)
		a.suppress.Mark(path, data)
		*marked = append(*marked, path)
	}
}

func (a *App) unmark(marked []string) {
	if a.suppress == nil {
		return
	}
	for _, f := range marked {
		a.suppress.Unmark(f)
	}
}

func (a *App) finishWrite(marked []string) {
	if a.suppress == nil {
		return
	}
	for _, f := range marked {
		a.suppress.End(f)
	}
}

func (a *App) runWatch(ctx context.Context, w *watchfs.Watcher, path string) {
	defer w.Close()
	root, _ := filepath.Abs(path)
	var lastSources []string
	for {
		if ctx.Err() != nil {
			return
		}
		sources, gitMeta, loaded, _ := workspace.WatchPaths(path)
		sources = workspace.RetainSources(lastSources, sources, root, loaded)
		if loaded {
			lastSources = sources
		}
		all := make([]string, 0, len(sources)+len(gitMeta))
		all = append(all, sources...)
		all = append(all, gitMeta...)
		w.Set(all)
		changed, err := w.Wait(ctx, watchfs.Debounce)
		if err != nil || ctx.Err() != nil {
			return
		}
		files, gitHit := classifyWatch(changed, gitMeta)
		emit, held := a.filterSaves(files)
		for len(held) > 0 {
			if a.suppress == nil {
				emit = append(emit, held...)
				break
			}
			if err := a.suppress.WaitIdle(ctx); err != nil {
				return
			}
			var again []string
			again, held = a.filterSaves(held)
			emit = append(emit, again...)
		}
		if len(emit) == 0 && !gitHit {
			continue
		}
		runtime.EventsEmit(a.context(), "project:changed", workspace.ProjectChanged{Files: emit, Git: gitHit})
	}
}

func (a *App) filterSaves(files []string) (emit, held []string) {
	for _, f := range files {
		if a.suppress == nil {
			emit = append(emit, f)
			continue
		}
		switch a.suppress.Decide(f) {
		case watchfs.Drop:
		case watchfs.Hold:
			held = append(held, f)
		default:
			emit = append(emit, f)
		}
	}
	return emit, held
}

func classifyWatch(changed, gitMeta []string) (files []string, git bool) {
	meta := map[string]struct{}{}
	for _, g := range gitMeta {
		meta[filepath.Clean(g)] = struct{}{}
	}
	for _, c := range changed {
		if _, ok := meta[filepath.Clean(c)]; ok {
			git = true
			continue
		}
		files = append(files, c)
	}
	return files, git
}

// ImportPreview converts an OpenAPI, WSDL, Postman, or SoapUI spec without saving it.
func (a *App) ImportPreview(kind, spec string, envs []string) (*workspace.ImportPreview, error) {
	return workspace.Preview(kind, spec, envs)
}

// ImportSave converts a spec and writes the project, returning its path.
func (a *App) ImportSave(req workspace.ImportSaveRequest) (string, error) {
	return workspace.Save(req)
}

// PickFile shows the native open-file dialog; "" means cancelled.
func (a *App) PickFile(title string) (string, error) {
	return runtime.OpenFileDialog(a.context(), runtime.OpenDialogOptions{
		Title: title,
		Filters: []runtime.FileFilter{
			{DisplayName: "Projects and specs (*.yaml, *.yml, *.json, *.wsdl, *.xml)", Pattern: "*.yaml;*.yml;*.json;*.wsdl;*.xml"},
			{DisplayName: "All files", Pattern: "*"},
		},
	})
}

// OAuthLogin opens the system browser; only status reaches the frontend.
func (a *App) OAuthLogin(req workspace.OAuthRequest) error {
	return a.sessions.OAuthLogin(a.context(), req, func(target string) error {
		runtime.BrowserOpenURL(a.context(), target)
		return nil
	})
}

func (a *App) OAuthLogout(req workspace.OAuthRequest) error {
	return a.sessions.OAuthLogout(req)
}

func (a *App) OAuthStatus(req workspace.OAuthRequest) (bool, error) {
	return a.sessions.OAuthStatus(req)
}
