package workspace

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

const (
	changeAdded    = "added"
	changeRemoved  = "removed"
	changeModified = "modified"
	changeMoved    = "moved"
)

// GitFile is one path `git status` reports, relative to the project directory.
// ProjectFile is true for the project file and its fragments.
type GitFile struct {
	Path        string `json:"path"`
	State       string `json:"state"`
	ProjectFile bool   `json:"projectFile"`
	RenamedFrom string `json:"renamedFrom,omitempty"`
}

// StepRef identifies a request that differs from HEAD. State is
// added, removed, modified, or moved. The collection tree marks added and
// modified (and moved) requests; removed requests appear only in the changes.
type StepRef struct {
	Suite string `json:"suite"`
	Case  string `json:"case"`
	Name  string `json:"name"`
	Index int    `json:"index"`
	State string `json:"state"`
}

// GitInfo is the read-only git snapshot for a project. Available is false
// when git is not installed. Repo is false when the project is not inside a
// work tree. Neither of those is an error. Ahead and Behind are from the
// local upstream ref; Tracking is false when the branch has no upstream.
type GitInfo struct {
	Available    bool      `json:"available"`
	Repo         bool      `json:"repo"`
	Root         string    `json:"root,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	Detached     bool      `json:"detached"`
	Short        string    `json:"short,omitempty"`
	Ahead        int       `json:"ahead"`
	Behind       int       `json:"behind"`
	Tracking     bool      `json:"tracking"`
	Upstream     string    `json:"upstream,omitempty"`
	UpstreamGone bool      `json:"upstreamGone,omitempty"`
	Merging      bool      `json:"merging"`
	Rebasing     bool      `json:"rebasing"`
	Files        []GitFile `json:"files"`
	ChangedSteps []StepRef `json:"changedSteps"`
}

// PartChange is one scalar (or one header, query, or variable key) that
// differs. Before and After are display text; a value secretscan would flag
// is "***REDACTED***".
type PartChange struct {
	Part   string `json:"part"`
	Key    string `json:"key,omitempty"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// StepChange is one request compared with HEAD. For moved, Suite/Case/Index
// are the working-tree location and From* is where it was at HEAD. Parts is
// set for modified, and for moved when the request also changed.
type StepChange struct {
	Suite     string       `json:"suite"`
	Case      string       `json:"case"`
	Name      string       `json:"name"`
	Index     int          `json:"index"`
	State     string       `json:"state"`
	FromSuite string       `json:"fromSuite,omitempty"`
	FromCase  string       `json:"fromCase,omitempty"`
	FromName  string       `json:"fromName,omitempty"`
	FromIndex int          `json:"fromIndex,omitempty"`
	Parts     []PartChange `json:"parts,omitempty"`
}

// EnvChange is an environment added, removed, or edited since HEAD.
type EnvChange struct {
	Name  string       `json:"name"`
	State string       `json:"state"`
	Parts []PartChange `json:"parts,omitempty"`
}

// AuthChange is a named auth definition added, removed, or edited since HEAD.
type AuthChange struct {
	Name  string       `json:"name"`
	State string       `json:"state"`
	Parts []PartChange `json:"parts,omitempty"`
}

// ScopeChange is a suite or case added, removed, or edited since HEAD.
// Suite is set for a case; Name is the suite or case name. Parts is set
// when a suite or case that exists on both sides changed vars, auth, feed,
// continueOnError, or parallelism.
type ScopeChange struct {
	Suite string       `json:"suite,omitempty"`
	Name  string       `json:"name"`
	State string       `json:"state"`
	Parts []PartChange `json:"parts,omitempty"`
}

// ProjectChanges is the semantic diff of the working tree against HEAD.
// File states come from git status; the step, environment, and auth entries
// are computed by loading both sides, not from `git diff` text. OtherFiles
// counts status entries that are not the project or one of its fragments.
type ProjectChanges struct {
	Available    bool          `json:"available"`
	Repo         bool          `json:"repo"`
	Steps        []StepChange  `json:"steps"`
	Environments []EnvChange   `json:"environments"`
	Auth         []AuthChange  `json:"auth"`
	Suites       []ScopeChange `json:"suites"`
	Cases        []ScopeChange `json:"cases"`
	// Meta is project name, description, and schemaVersion. A single part
	// named "project" means the loaded projects differ and no specific field
	// was reported.
	Meta       []PartChange `json:"meta,omitempty"`
	OtherFiles int          `json:"otherFiles"`
}

// ProjectChanged is the desktop "project:changed" event. Files are absolute
// paths that changed on disk, excluding a file this app just saved. Git is
// true when HEAD or the index changed (a branch switch or a commit outside
// Verity).
type ProjectChanged struct {
	Files []string `json:"files"`
	Git   bool     `json:"git"`
}

// GitStatus reports branch, ahead/behind, and which project requests differ
// from HEAD. A missing git executable or a directory that is not a repository
// is a normal result. A git failure that is not "not a repository" is returned.
func GitStatus(projectPath string) (*GitInfo, error) {
	info := blankGitInfo()
	if !vcs.Available() {
		return info, nil
	}
	info.Available = true
	abs, dir, err := projectDir(projectPath)
	if err != nil {
		return nil, err
	}
	st, err := vcs.Status(dir)
	if errors.Is(err, vcs.ErrUnavailable) {
		info.Available = false
		return info, nil
	}
	if vcs.IsNotRepository(err) {
		return info, nil
	}
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "git status")
	}
	fillGitInfo(info, abs, st)
	if ch, derr := diffAgainstHEAD(abs, st); derr == nil {
		info.ChangedSteps = ch.stepRefs()
	}
	return info, nil
}

// GitChanges is the semantic diff shown in the Changes view. It does not run
// `git diff`. A project that has never been committed is reported as added.
func GitChanges(projectPath string) (*ProjectChanges, error) {
	if !vcs.Available() {
		return blankChanges(false, false), nil
	}
	abs, dir, err := projectDir(projectPath)
	if err != nil {
		return nil, err
	}
	st, err := vcs.Status(dir)
	if errors.Is(err, vcs.ErrUnavailable) {
		return blankChanges(false, false), nil
	}
	if vcs.IsNotRepository(err) {
		return blankChanges(true, false), nil
	}
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "git status")
	}
	return diffAgainstHEAD(abs, st)
}

// WatchPaths lists the files the desktop watcher should follow: everything
// model.Load records, plus the worktree's HEAD, index, current branch ref,
// upstream ref, stash ref/reflog, and packed-refs (via `git rev-parse --git-path`, so linked
// worktrees resolve). loaded is false when the project does not load; sources
// is then just the project path. Callers keep the previous source set in
// that case (see RetainSources). Git metadata is omitted when git is absent
// or the directory is not a repository.
func WatchPaths(projectPath string) (sources, gitMeta []string, loaded bool, err error) {
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, nil, false, verr.Wrap(verr.CodeConfig, err, "resolve project path")
	}
	if p, lerr := model.Load(abs); lerr == nil && len(p.SourceFiles) > 0 {
		sources = append([]string{}, p.SourceFiles...)
		loaded = true
	} else {
		sources = []string{abs}
	}
	if !vcs.Available() {
		return sources, nil, loaded, nil
	}
	root, rerr := vcs.RepoRoot(filepath.Dir(abs))
	if rerr != nil {
		return sources, nil, loaded, nil
	}
	gitMeta = gitWatchPaths(root)
	return sources, gitMeta, loaded, nil
}

// RetainSources keeps the last successfully loaded source set when a reload
// fails. The project root is always included so fixing that file is noticed.
// A successful load replaces the set.
func RetainSources(previous, next []string, root string, loaded bool) []string {
	if loaded {
		return cleanUnique(next)
	}
	return cleanUnique(append(append([]string{}, previous...), root))
}

func cleanUnique(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		c := filepath.Clean(p)
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func gitWatchPaths(root string) []string {
	var out []string
	add := func(p string, err error) {
		if err != nil || p == "" {
			return
		}
		out = append(out, p)
	}
	for _, name := range []string{"HEAD", "index", "refs/stash", "logs/refs/stash"} {
		add(vcs.GitPath(root, name))
	}
	// A symbolic branch ref changes on reset/commit without rewriting HEAD.
	// @{u} is the upstream ref; it moves on fetch. Both are absent when
	// detached or when the branch has no upstream.
	for _, rev := range []string{"HEAD", "@{u}"} {
		name, ok, err := vcs.SymbolicFullName(root, rev)
		if err != nil || !ok {
			continue
		}
		add(vcs.GitPath(root, name))
	}
	add(vcs.GitPath(root, "packed-refs"))
	return out
}

func projectDir(projectPath string) (abs, dir string, err error) {
	abs, err = filepath.Abs(projectPath)
	if err != nil {
		return "", "", verr.Wrap(verr.CodeConfig, err, "resolve project path")
	}
	return abs, filepath.Dir(abs), nil
}

func blankGitInfo() *GitInfo {
	return &GitInfo{Files: []GitFile{}, ChangedSteps: []StepRef{}}
}

func blankChanges(available, repo bool) *ProjectChanges {
	return &ProjectChanges{
		Available: available, Repo: repo,
		Steps: []StepChange{}, Environments: []EnvChange{}, Auth: []AuthChange{},
		Suites: []ScopeChange{}, Cases: []ScopeChange{},
	}
}

func fillGitInfo(info *GitInfo, projectPath string, st *vcs.Snapshot) {
	info.Repo = true
	info.Root = st.Root
	info.Branch = st.Branch
	info.Detached = st.Detached
	info.Short = st.Short
	info.Ahead = st.Ahead
	info.Behind = st.Behind
	info.Tracking = st.Tracking
	info.Upstream = st.Upstream
	info.UpstreamGone = st.UpstreamGone
	info.Merging = st.Merging
	info.Rebasing = st.Rebasing
	set := projectFileSet(projectPath)
	projectDir := filepath.Dir(projectPath)
	info.Files = make([]GitFile, 0, len(st.Entries))
	for _, e := range st.Entries {
		abs := filepath.Clean(filepath.Join(st.Root, filepath.FromSlash(e.Path)))
		_, isProject := set[abs]
		gf := GitFile{
			Path:        relToProject(projectDir, abs),
			State:       fileState(e),
			ProjectFile: isProject,
		}
		if e.RenamedFrom != "" {
			from := filepath.Clean(filepath.Join(st.Root, filepath.FromSlash(e.RenamedFrom)))
			gf.RenamedFrom = relToProject(projectDir, from)
		}
		info.Files = append(info.Files, gf)
	}
}

func fileState(e vcs.Entry) string {
	if e.Conflicted {
		return "conflicted"
	}
	if e.Untracked {
		return "untracked"
	}
	switch {
	case e.Index == "R" || e.Index == "C" || e.Worktree == "R" || e.Worktree == "C":
		return "renamed"
	case e.Index == "A" || e.Worktree == "A":
		return "added"
	case e.Index == "D" || e.Worktree == "D":
		return "deleted"
	default:
		return "modified"
	}
}

func relToProject(projectDir, abs string) string {
	rel, err := filepath.Rel(projectDir, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func projectFileSet(projectPath string) map[string]struct{} {
	set := map[string]struct{}{}
	files, err := ProjectFiles(projectPath)
	if err != nil {
		if abs, aerr := filepath.Abs(projectPath); aerr == nil {
			set[filepath.Clean(abs)] = struct{}{}
		}
		return set
	}
	for _, f := range files {
		set[filepath.Clean(f)] = struct{}{}
	}
	return set
}

func diffAgainstHEAD(projectPath string, st *vcs.Snapshot) (*ProjectChanges, error) {
	work, err := model.Load(projectPath)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "load project")
	}
	head, err := loadAtHEAD(projectPath, st.Root, st)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "read project at HEAD")
	}
	ch := diffProjects(head, work)
	ch.Available = true
	ch.Repo = true
	ch.OtherFiles = countOther(projectPath, st)
	noteUnparsedEdit(ch, projectPath, st)
	return ch, nil
}

// noteUnparsedEdit reports a project file git says is dirty when the decoded
// projects still match (a comment, or an equivalent include edit).
func noteUnparsedEdit(ch *ProjectChanges, projectPath string, st *vcs.Snapshot) {
	if ch == nil || hasReported(ch) || st == nil {
		return
	}
	set := projectFileSet(projectPath)
	for _, e := range st.Entries {
		if e.Untracked {
			continue
		}
		abs := filepath.Clean(filepath.Join(st.Root, filepath.FromSlash(e.Path)))
		if _, ok := set[abs]; ok {
			ch.Meta = append(ch.Meta, PartChange{Part: "file", After: "changed"})
			return
		}
	}
}

func countOther(projectPath string, st *vcs.Snapshot) int {
	if st == nil {
		return 0
	}
	set := projectFileSet(projectPath)
	n := 0
	for _, e := range st.Entries {
		abs := filepath.Clean(filepath.Join(st.Root, filepath.FromSlash(e.Path)))
		if _, ok := set[abs]; !ok {
			n++
		}
	}
	return n
}

// loadAtHEAD loads the project as of HEAD. The bytes come from
// `git cat-file` through WalkProject, including fragments. When
// status says the root (or a fragment) was renamed, the blob is read from
// RenamedFrom so a `git mv` is compared with the previous path instead of
// looking like a brand-new project. A project that is not in HEAD yet
// (never committed, or the file is new) is an empty project, so the working
// tree is entirely added.
func loadAtHEAD(projectPath, root string, st *vcs.Snapshot) (*model.Project, error) {
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, err
	}
	renames := renameSources(st)
	var parts []*model.Project
	err = WalkProject(abs, func(file string) ([]byte, error) {
		rel, rerr := repoRelative(root, file)
		if rerr != nil {
			return nil, rerr
		}
		if from, ok := renames[rel]; ok {
			rel = from
		}
		return vcs.ReadAt(root, "HEAD", rel)
	}, func(file, _ string, data []byte) error {
		p, derr := model.Decode(data, file)
		if derr != nil {
			return derr
		}
		parts = append(parts, p)
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || vcs.IsRevision(err) {
			return &model.Project{}, nil
		}
		return nil, err
	}
	return mergeProjects(parts), nil
}

func renameSources(st *vcs.Snapshot) map[string]string {
	if st == nil {
		return nil
	}
	out := map[string]string{}
	var deleted, arrived []string
	for _, e := range st.Entries {
		if e.Path != "" && e.RenamedFrom != "" {
			out[e.Path] = e.RenamedFrom
			continue
		}
		if e.Index == "D" || e.Worktree == "D" {
			deleted = append(deleted, e.Path)
		}
		if e.Index == "A" || e.Worktree == "A" {
			arrived = append(arrived, e.Path)
		}
	}
	// Git reports a directory move as delete+add when rename detection is
	// off or the similarity score is too low. Pair a unique basename so the
	// root project is still compared with its old blob.
	byDel := map[string][]string{}
	used := map[string]bool{}
	for _, src := range out {
		used[src] = true
	}
	for _, path := range deleted {
		if path == "" || used[path] {
			continue
		}
		base := filepath.Base(path)
		byDel[base] = append(byDel[base], path)
	}
	byArr := map[string][]string{}
	for _, path := range arrived {
		if path == "" {
			continue
		}
		if _, ok := out[path]; ok {
			continue
		}
		base := filepath.Base(path)
		byArr[base] = append(byArr[base], path)
	}
	for base, added := range byArr {
		removed := byDel[base]
		if len(added) == 1 && len(removed) == 1 {
			out[added[0]] = removed[0]
		}
	}
	return out
}

func mergeProjects(parts []*model.Project) *model.Project {
	if len(parts) == 0 {
		return &model.Project{}
	}
	base := *parts[0]
	base.Include = nil
	base.SourceFiles = nil
	for _, frag := range parts[1:] {
		if frag == nil {
			continue
		}
		base.Environments = append(base.Environments, frag.Environments...)
		base.Auth = append(base.Auth, frag.Auth...)
		base.Suites = append(base.Suites, frag.Suites...)
		base.Mocks = append(base.Mocks, frag.Mocks...)
		if base.Schema == nil {
			base.Schema = frag.Schema
		}
		if base.Name == "" {
			base.Name = frag.Name
		}
		if base.SchemaVersion == "" {
			base.SchemaVersion = frag.SchemaVersion
		}
	}
	return &base
}

func repoRelative(root, file string) (string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", err
	}
	top := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		top = resolved
	}
	// A stash can remove whole directories. Resolve the nearest existing
	// ancestor, then append the missing suffix (without following the file
	// itself, which may be a symlink tracked by Git).
	ancestor := filepath.Dir(abs)
	for {
		if resolved, err := filepath.EvalSymlinks(ancestor); err == nil {
			suffix, err := filepath.Rel(ancestor, abs)
			if err != nil {
				return "", err
			}
			abs = filepath.Join(resolved, suffix)
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", verr.New(verr.CodeConfig, "project file is outside the git repository")
	}
	return filepath.ToSlash(rel), nil
}

func (c *ProjectChanges) stepRefs() []StepRef {
	if c == nil || len(c.Steps) == 0 {
		return []StepRef{}
	}
	out := make([]StepRef, len(c.Steps))
	for i, s := range c.Steps {
		out[i] = StepRef{Suite: s.Suite, Case: s.Case, Name: s.Name, Index: s.Index, State: s.State}
	}
	return out
}
