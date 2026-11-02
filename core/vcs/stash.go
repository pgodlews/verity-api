package vcs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/verr"
)

// Stash identifies an entry by its full object id. Index is display-only.
type Stash struct {
	ID      string `json:"id"`
	Index   int    `json:"index"`
	Branch  string `json:"branch"`
	Message string `json:"message"`
	Date    string `json:"date"`
}

// Stashes uses NUL fields and record terminators, independent of locale,
// date config, quoting, and the user's stash list format.
func Stashes(ctx context.Context, root string) ([]Stash, error) {
	out, _, err := runGit(ctx, root, gitOpt{readOnly: true, terminalPrompt: true},
		"stash", "list", "--no-show-signature", "-z", "--format=%H%x00%gd%x00%gs%x00%cI")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	if fields[len(fields)-1] != "" || (len(fields)-1)%4 != 0 {
		return nil, fmt.Errorf("invalid git stash list output")
	}
	list := make([]Stash, 0, (len(fields)-1)/4)
	for i := 0; i+3 < len(fields); i += 4 {
		if !fullObjectID(fields[i]) {
			return nil, fmt.Errorf("invalid git stash object id")
		}
		subject := fields[i+2]
		branch, message, found := strings.Cut(subject, ": ")
		if found && (strings.HasPrefix(subject, "WIP on ") || strings.HasPrefix(subject, "On ")) {
			branch = strings.TrimPrefix(strings.TrimPrefix(branch, "WIP on "), "On ")
		} else {
			branch, message = "", subject
		}
		list = append(list, Stash{ID: fields[i], Index: i / 4,
			Branch: redactGit(branch), Message: redactGit(message), Date: fields[i+3]})
	}
	return list, nil
}

func fullObjectID(id string) bool {
	return (len(id) == 40 || len(id) == 64) && isBlobID(id)
}

// FindStash refuses stale ids even when the object is still in the object
// database. A selector or an abbreviated id is never accepted from callers.
func FindStash(ctx context.Context, root, id string) (*Stash, error) {
	if !fullObjectID(id) {
		return nil, verr.New(verr.CodeValidation, "a full stash object id is required")
	}
	list, err := Stashes(ctx, root)
	if err != nil {
		return nil, err
	}
	for _, entry := range list {
		if entry.ID == id {
			return &entry, nil
		}
	}
	return nil, verr.New(verr.CodeConflict, "this stash is no longer in the list; refresh stashes and try again")
}

// StashPush saves tracked changes, plus non-ignored untracked files with -u.
// An empty paths slice means the whole repository. Paths are literal and may
// contain newlines, spaces, dashes, or pathspec syntax.
func StashPush(ctx context.Context, root string, paths []string, untracked bool, message string) (err error) {
	if strings.ContainsRune(message, 0) {
		return verr.New(verr.CodeValidation, "stash message contains a NUL byte")
	}
	selected := map[string]bool{}
	for _, p := range paths {
		if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
			return verr.New(verr.CodeValidation, "stash paths must be files inside the repository")
		}
		selected[p] = true
	}
	// Read the full status: a path-limited status hides rename sources and
	// unrelated staged entries that Git would otherwise copy into the stash.
	out, _, err := runGit(ctx, root, gitOpt{readOnly: true, terminalPrompt: true},
		"status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	st, err := parseStatus(out)
	if err != nil {
		return err
	}
	chosen := func(p string) bool {
		if len(paths) == 0 {
			return true
		}
		for name := range selected {
			if p == name || strings.HasPrefix(p, name+"/") {
				return true
			}
		}
		return false
	}
	for _, e := range st.Entries {
		if e.RenamedFrom != "" && (chosen(e.Path) || chosen(e.RenamedFrom)) {
			selected[e.Path], selected[e.RenamedFrom] = true, true
		}
	}
	found := false
	var protected, deleted []string
	for _, e := range st.Entries {
		if e.Conflicted {
			return verr.New(verr.CodeConflict, "resolve the existing conflicts before stashing")
		}
		if chosen(e.Path) {
			if !e.Submodule && (!e.Untracked || (untracked && !nestedRepository(root, e.Path))) {
				found = true
			}
			if len(paths) > 0 {
				if e.Index == "D" {
					deleted = append(deleted, e.Path)
				} else if e.Index == "R" {
					deleted = append(deleted, e.RenamedFrom)
				}
			}
		} else if e.Index != "." && e.Index != "" && !e.Untracked {
			protected = append(protected, e.Path)
			if e.RenamedFrom != "" {
				protected = append(protected, e.RenamedFrom)
			}
		}
	}
	if !found {
		return verr.New(verr.CodeValidation, "nothing to stash in the chosen files")
	}
	// Unstage unrelated entries so neither the stash's index nor worktree
	// parent contains them. Selected deletions must exist in the index for
	// stash's pathspec matching. Only restore those deletions on failure.
	temporary := append(append([]string{}, protected...), deleted...)
	if len(temporary) > 0 {
		snapshot, snapErr := SnapshotIndex(ctx, root, temporary)
		if snapErr != nil {
			return snapErr
		}
		succeeded := false
		defer func() {
			restore := temporary
			if succeeded {
				restore = protected
			}
			if len(restore) > 0 {
				keep := map[string]bool{}
				for _, p := range restore {
					keep[p] = true
				}
				var entries []IndexEntry
				for _, e := range snapshot {
					if keep[e.Path] {
						entries = append(entries, e)
					}
				}
				err = errors.Join(err, RestoreIndex(ctx, root, restore, entries))
			}
		}()
		args, pathErr := withPaths([]string{"restore", "--staged", "--"}, temporary)
		if pathErr != nil {
			return pathErr
		}
		if _, err = run(ctx, root, nil, args...); err != nil {
			return err
		}
		err = pushStash(ctx, root, paths, selected, untracked, message)
		succeeded = err == nil
		return err
	}
	return pushStash(ctx, root, paths, selected, untracked, message)
}

// nestedRepository excludes untracked repositories, which stash -u ignores
// (otherwise Git can create an empty stash and leave the directory behind).
func nestedRepository(root, name string) bool {
	for dir := strings.TrimSuffix(name, "/"); dir != "." && dir != ""; dir = path.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(dir), ".git")); err == nil {
			return true
		}
	}
	return false
}

func pushStash(ctx context.Context, root string, paths []string, selected map[string]bool, untracked bool, message string) error {
	args := []string{"stash", "push"}
	if untracked {
		args = append(args, "-u")
	}
	if message != "" {
		args = append(args, "-m", message)
	}
	args = append(args, "--")
	if len(paths) > 0 {
		var names []string
		for p := range selected {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			args = append(args, ":(literal)"+p)
		}
	}
	// Do not relay stdout: it echoes the user's message and file names.
	text, err := run(ctx, root, nil, args...)
	if missingIdentity(err) {
		return &IdentityError{}
	}
	if err == nil && strings.Contains(strings.ToLower(text), "no local changes to save") {
		return verr.New(verr.CodeValidation, "nothing to stash in the chosen files")
	}
	return err
}

// StashApply applies by immutable id. Pop is apply followed by a fresh
// membership check and drop; an unsuccessful apply never drops anything.
func StashApply(ctx context.Context, root, id string, pop bool) (*PullOutcome, error) {
	before, err := ConflictedFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(before) > 0 {
		return nil, verr.New(verr.CodeConflict, "resolve the existing conflicts before applying a stash")
	}
	if _, err := FindStash(ctx, root, id); err != nil {
		return nil, err
	}
	_, applyErr := run(ctx, root, nil, "stash", "apply", "--", id)
	if applyErr != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, applyErr
		}
		conflicts, err := ConflictedFiles(ctx, root)
		if err != nil {
			return nil, err
		}
		if len(conflicts) > 0 {
			return &PullOutcome{Status: "conflict", Conflicted: conflicts, Output: "Stash apply left conflicts. Resolve them in your editor. The stash was kept."}, nil
		}
		return nil, applyErr
	}
	if pop {
		if err := StashDrop(ctx, root, id); err != nil {
			return nil, err
		}
	}
	return &PullOutcome{Status: "applied"}, nil
}

// StashDrop takes an id, never a position from the UI. Git itself only accepts
// a reflog selector for drop, so resolve that selector afresh immediately
// before invoking it. Workspace serializes our writes with the repo lock.
func StashDrop(ctx context.Context, root, id string) error {
	entry, err := FindStash(ctx, root, id)
	if err != nil {
		return err
	}
	_, err = run(ctx, root, nil, "stash", "drop", "--", "stash@{"+strconv.Itoa(entry.Index)+"}")
	return err
}

// StashFiles lists both the tracked diff and the optional untracked tree.
// No patch, external diff driver, or textconv output is sent to the UI.
func StashFiles(ctx context.Context, root, id string) ([]string, bool, error) {
	if _, err := FindStash(ctx, root, id); err != nil {
		return nil, false, err
	}
	opt := gitOpt{readOnly: true, terminalPrompt: true}
	out, _, err := runGit(ctx, root, opt, "diff-tree", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-r", "-z", id+"^1", id, "--")
	if err != nil {
		return nil, false, err
	}
	files := splitNULPaths(out)
	parents, _, err := runGit(ctx, root, opt, "rev-list", "--parents", "-n", "1", id, "--")
	if err != nil {
		return nil, false, err
	}
	hasUntracked := len(strings.Fields(string(parents))) == 4
	if hasUntracked {
		out, _, err = runGit(ctx, root, opt, "ls-tree", "--name-only", "-r", "-z", id+"^3", "--")
		if err != nil {
			return nil, false, err
		}
		files = append(files, splitNULPaths(out)...)
	}
	return files, hasUntracked, nil
}
