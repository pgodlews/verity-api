package workspace

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

// StashRequest saves all tracked changes, or only Paths when nonempty.
// Paths use the same project-relative names as GitStatus. BeforeRewrite
// invalidates desktop self-save hashes, just as Pull does.
type StashRequest struct {
	Path             string   `json:"path"`
	Paths            []string `json:"paths"`
	IncludeUntracked bool     `json:"includeUntracked"`
	Message          string   `json:"message"`
	BeforeRewrite    func()   `json:"-"`
}

// StashActionRequest uses the id returned by Stashes; never a list position.
// Action is apply, pop, or drop. StashChanges ignores Action.
type StashActionRequest struct {
	Path          string `json:"path"`
	ID            string `json:"id"`
	Action        string `json:"action"`
	BeforeRewrite func() `json:"-"`
}

// Stashes returns redacted metadata, with immutable ids for later actions.
func Stashes(ctx context.Context, projectPath string) ([]vcs.Stash, error) {
	root, err := repoRootOf(projectPath)
	if err != nil {
		return nil, err
	}
	list, err := vcs.Stashes(ctx, root)
	if err != nil {
		return nil, gitErr(err)
	}
	for i := range list {
		list[i].Branch = redactStashText(list[i].Branch)
		list[i].Message = redactStashText(list[i].Message)
	}
	return list, nil
}

func redactStashText(value string) string {
	if secretscan.Sensitive("body", value, false) || (len(strings.Fields(value)) == 1 && secrets.LooksLikeSecret(value)) {
		return secrets.Mask
	}
	// Custom stash-store subjects can use a plain "api_key: value" pair,
	// which is neither a JSON body nor a form handled by the body scanner.
	if key, literal, ok := strings.Cut(value, ": "); ok && secretscan.Sensitive(strings.TrimSpace(key), literal, false) {
		return secrets.Mask
	}
	return value
}

// Stash saves changes and lets the normal watcher reload the project.
func Stash(ctx context.Context, req StashRequest) (*PullResult, error) {
	unlock := lockProject(req.Path)
	defer unlock()
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	_, dir, err := projectDir(req.Path)
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, p := range req.Paths {
		if p == "" || strings.ContainsRune(p, 0) {
			return nil, verr.New(verr.CodeValidation, "invalid stash path")
		}
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(dir, filepath.FromSlash(p))
		}
		rel, err := repoRelative(root, full)
		if err != nil {
			return nil, err
		}
		rels = append(rels, rel)
	}
	if req.BeforeRewrite != nil {
		req.BeforeRewrite()
	}
	if err := vcs.StashPush(ctx, root, rels, req.IncludeUntracked, req.Message); err != nil {
		return nil, gitErr(err)
	}
	return &PullResult{Status: "stashed"}, nil
}

// StashAction keeps conflicts for the editor. Pop only drops after a
// successful apply. The repository lock also serializes saves and commits.
func StashAction(ctx context.Context, req StashActionRequest) (*PullResult, error) {
	unlock := lockProject(req.Path)
	defer unlock()
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	if req.Action == "drop" {
		if err := vcs.StashDrop(ctx, root, req.ID); err != nil {
			return nil, gitErr(err)
		}
		return &PullResult{Status: "dropped"}, nil
	}
	if req.Action != "apply" && req.Action != "pop" {
		return nil, verr.New(verr.CodeValidation, "stash action must be apply, pop, or drop")
	}
	if req.BeforeRewrite != nil {
		req.BeforeRewrite()
	}
	out, err := vcs.StashApply(ctx, root, req.ID, req.Action == "pop")
	if err != nil {
		return nil, gitErr(err)
	}
	return &PullResult{Status: out.Status, Conflicted: relPaths(req.Path, root, out.Conflicted), Output: out.Output}, nil
}

// StashChanges compares the stash with its original base, using the existing
// semantic diff and secret scanner. Other paths are counted, never rendered
// as raw patches. Both sides (including fragments) come only from Git blobs.
func StashChanges(ctx context.Context, req StashActionRequest) (*ProjectChanges, error) {
	root, err := repoRootOf(req.Path)
	if err != nil {
		return nil, err
	}
	files, untracked, err := vcs.StashFiles(ctx, root, req.ID)
	if err != nil {
		return nil, gitErr(err)
	}
	before, bset, err := loadStashProject(req.Path, root, req.ID+"^1", "")
	if err != nil {
		return nil, err
	}
	extra := ""
	if untracked {
		extra = req.ID + "^3"
	}
	after, aset, err := loadStashProject(req.Path, root, req.ID, extra)
	if err != nil {
		return nil, err
	}
	ch := diffProjects(before, after)
	projectChanged := false
	seen := map[string]bool{}
	for _, p := range files {
		if seen[p] {
			continue
		}
		seen[p] = true
		if bset[p] || aset[p] {
			projectChanged = true
		} else {
			ch.OtherFiles++
		}
	}
	if projectChanged && !hasReported(ch) {
		ch.Meta = append(ch.Meta, PartChange{Part: "file", After: "changed"})
	}
	return ch, nil
}

func loadStashProject(projectPath, root, rev, extra string) (*model.Project, map[string]bool, error) {
	var parts []*model.Project
	set := map[string]bool{}
	err := WalkProject(projectPath, func(file string) ([]byte, error) {
		rel, err := repoRelative(root, file)
		if err != nil {
			return nil, err
		}
		data, err := vcs.ReadAt(root, rev, rel)
		if errors.Is(err, fs.ErrNotExist) && extra != "" {
			data, err = vcs.ReadAt(root, extra, rel)
		}
		if err == nil {
			set[rel] = true
		}
		return data, err
	}, func(file, _ string, data []byte) error {
		p, err := model.Decode(data, file)
		if err == nil {
			parts = append(parts, p)
		}
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return &model.Project{}, set, nil
	}
	if err != nil {
		// Decoder errors can quote the original secret-bearing source.
		return nil, nil, verr.New(verr.CodeConfig, "cannot decode the project in this stash revision")
	}
	p := mergeProjects(parts)
	p.SourceDir = filepath.Dir(projectPath)
	for _, file := range model.ReferencedFiles(p) {
		if rel, err := repoRelative(root, file); err == nil {
			set[rel] = true
		}
	}
	return p, set, nil
}
