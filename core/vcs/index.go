package vcs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

// ReadIndex returns the staged blob for a repo-relative path (what
// `git commit` would record). A path that is not in the index is
// fs.ErrNotExist. A staged symlink is refused: its blob is the link text,
// not the file a scanner should read.
func ReadIndex(root, rel string) ([]byte, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	if err := validateRelPath(rel); err != nil {
		return nil, err
	}
	listed, err := git(root, "ls-files", "--stage", "-z", "--", ":(literal)"+rel)
	if err != nil {
		if notRepository(err) {
			return nil, &NotRepositoryError{Dir: root}
		}
		return nil, err
	}
	mode, oid, stage, ok := stage0(listed, rel)
	if !ok {
		return nil, fs.ErrNotExist
	}
	if stage != "0" {
		return nil, fmt.Errorf("%s is unmerged in the index", rel)
	}
	if mode == "120000" {
		return nil, fmt.Errorf("%s is a symlink in the index; refusing to read a symlinked project file", rel)
	}
	if !isBlobID(oid) {
		return nil, fmt.Errorf("git ls-files returned an unexpected object id")
	}
	data, err := git(root, "cat-file", "blob", oid)
	if err != nil {
		if notRepository(err) {
			return nil, &NotRepositoryError{Dir: root}
		}
		return nil, fs.ErrNotExist
	}
	return data, nil
}

// IndexEntry is one stage line from `git ls-files -s`. Line is the
// newline form `update-index --index-info` accepts.
type IndexEntry struct {
	Path string
	Line string
}

// SnapshotIndex records the index lines for paths. A path that is not staged
// is simply absent. The caller restores exactly these paths and no others.
func SnapshotIndex(ctx context.Context, root string, paths []string) ([]IndexEntry, error) {
	return snapshotIndex(ctx, root, "", paths)
}

func snapshotIndex(ctx context.Context, root, indexFile string, paths []string) ([]IndexEntry, error) {
	args, err := withPaths([]string{"ls-files", "-s", "-z", "--"}, paths)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stdout, _, err := runGit(ctx, root, gitOpt{readOnly: true, terminalPrompt: true, indexFile: indexFile}, args...)
	if err != nil {
		return nil, err
	}
	var out []IndexEntry
	for _, rec := range bytes.Split(stdout, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		line := string(rec)
		if strings.Contains(line, "\n") {
			return nil, fmt.Errorf("refusing to snapshot an index path that contains a newline")
		}
		_, path, ok := strings.Cut(line, "\t")
		if !ok || path == "" {
			return nil, fmt.Errorf("unrecognized ls-files record")
		}
		out = append(out, IndexEntry{Path: path, Line: line})
	}
	return out, nil
}

// RestoreIndex puts paths back to entries. Every snapshot path is removed
// first (including a stage-0 blob git add wrote over an unmerged path), then
// the saved --index-info lines are applied, so stages 1–3 come back and a
// refused stage 0 does not remain. Paths that were not in the snapshot are
// left alone. A cancelled context still runs: the caller is putting the
// index back after a failure. While a dying git holds index.lock the call
// retries until the grace period ends.
func RestoreIndex(ctx context.Context, root string, paths []string, entries []IndexEntry) error {
	rels, err := cleanPaths(paths)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, e := range entries {
		buf.WriteString(e.Line)
		buf.WriteByte('\n')
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)
	deadline := time.Now().Add(gitCancelGrace + 500*time.Millisecond)
	var last error
	for {
		last = restoreIndexOnce(ctx, root, rels, buf.Bytes())
		if last == nil || !indexLocked(last) || !time.Now().Before(deadline) {
			return last
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func restoreIndexOnce(ctx context.Context, root string, rels []string, info []byte) error {
	for _, p := range rels {
		if err := removeIndexPath(ctx, root, "", p); err != nil {
			return err
		}
	}
	if len(info) == 0 {
		return nil
	}
	_, _, err := runGit(ctx, root, gitOpt{terminalPrompt: true, skipFilters: true, stdin: info}, "update-index", "--index-info")
	return err
}

func indexLocked(err error) bool {
	return strings.Contains(strings.ToLower(combined(err)), "index.lock")
}

// removeIndexPath deletes every stage of path. update-index --force-remove
// leaves unmerged stages in place and ignores a :(literal) pathspec, so the
// removal is a mode-0 --index-info line. A path that is already absent is
// a successful no-op.
func removeIndexPath(ctx context.Context, root, indexFile, path string) error {
	line := "0 " + nullOID(ctx, root) + "\t" + path + "\n"
	opt := gitOpt{terminalPrompt: true, skipFilters: true, readOnly: true, indexFile: indexFile, stdin: []byte(line)}
	_, _, err := runGit(ctx, root, opt, "update-index", "--index-info")
	return err
}

func nullOID(ctx context.Context, root string) string {
	stdout, _, err := runGit(ctx, root, gitOpt{skipFilters: true, skipSSH: true, readOnly: true}, "rev-parse", "--show-object-format")
	if err == nil && strings.TrimSpace(string(stdout)) == "sha256" {
		return strings.Repeat("0", 64)
	}
	return strings.Repeat("0", 40)
}

// StagedOp is one cached change. From is set for a rename or copy.
type StagedOp struct {
	Code string
	Path string
	From string
}

// StagedOps lists cached changes with rename sources
// (`git diff --cached --name-status -z`).
func StagedOps(root string) ([]StagedOp, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	out, err := git(root, "diff", "--cached", "--name-status", "-z")
	if err != nil {
		if notRepository(err) {
			return nil, &NotRepositoryError{Dir: root}
		}
		return nil, err
	}
	recs := splitNULPaths(out)
	var ops []StagedOp
	for i := 0; i < len(recs); {
		status := recs[i]
		i++
		if status == "" {
			continue
		}
		code := status[:1]
		switch code {
		case "R", "C":
			if i+1 >= len(recs) {
				return nil, fmt.Errorf("git diff rename record is missing a path")
			}
			ops = append(ops, StagedOp{Code: code, From: recs[i], Path: recs[i+1]})
			i += 2
		default:
			if i >= len(recs) {
				return nil, fmt.Errorf("git diff record is missing a path")
			}
			ops = append(ops, StagedOp{Code: code, Path: recs[i]})
			i++
		}
	}
	return ops, nil
}

// UnmergedPaths lists paths with an unmerged index stage (`git ls-files -u`).
func UnmergedPaths(ctx context.Context, root string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout, _, err := runGit(ctx, root, gitOpt{readOnly: true, terminalPrompt: true}, "ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, rec := range bytes.Split(stdout, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		_, path, ok := strings.Cut(string(rec), "\t")
		if !ok || path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func stage0(out []byte, rel string) (mode, oid, stage string, ok bool) {
	recs := bytes.Split(out, []byte{0})
	found := false
	for _, rec := range recs {
		if len(rec) == 0 {
			continue
		}
		line := string(rec)
		meta, path, cut := strings.Cut(line, "\t")
		if !cut || path != rel {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 {
			return "", "", "", false
		}
		if found {
			return "", "", "unmerged", true
		}
		if fields[0] == "120000" {
			return fields[0], fields[1], fields[2], true
		}
		mode, oid, stage = fields[0], fields[1], fields[2]
		found = true
	}
	return mode, oid, stage, found
}

// StagedChanges lists paths added, copied, modified, or renamed in the
// index (`git diff --cached --name-only --diff-filter=ACMR -z`), relative
// to the repository root, as git prints them.
func StagedChanges(root string) ([]string, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	out, err := git(root, "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z")
	if err != nil {
		if notRepository(err) {
			return nil, &NotRepositoryError{Dir: root}
		}
		return nil, err
	}
	return splitNULPaths(out), nil
}

// splitNULPaths divides a -z path list and drops every empty record.
// Porcelain status uses splitStatusRecords instead, which keeps an empty
// record in the middle so a missing rename path is visible.
func splitNULPaths(out []byte) []string {
	if len(out) == 0 {
		return nil
	}
	parts := strings.Split(string(out), "\x00")
	var names []string
	for _, p := range parts {
		if p != "" {
			names = append(names, p)
		}
	}
	return names
}
