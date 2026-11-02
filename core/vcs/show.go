package vcs

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// ReadAt returns the blob at rev:path. path is relative to the repository
// root (forward slashes). A path that is not in that revision is
// fs.ErrNotExist. A symlink is refused: its blob is the link text, not the
// file a scanner should read, which is the same rule as scan-secrets --staged.
// rev and path are passed as separate git arguments, path after `--`.
func ReadAt(root, rev, path string) ([]byte, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	if err := validateRev(rev); err != nil {
		return nil, err
	}
	if err := validateRelPath(path); err != nil {
		return nil, err
	}
	// :(literal) stops git treating brackets in the path as a pathspec class.
	// `project[1].yaml` would otherwise match `project1.yaml` or nothing.
	listed, err := git(root, "ls-tree", "-z", rev, "--", ":(literal)"+path)
	if err != nil {
		if badRevision(err) {
			return nil, &RevisionError{Rev: rev}
		}
		if notRepository(err) {
			return nil, &NotRepositoryError{Dir: root}
		}
		return nil, err
	}
	mode, oid, ok := treeEntry(listed)
	if !ok {
		return nil, fs.ErrNotExist
	}
	switch mode {
	case "120000":
		return nil, fmt.Errorf("%s is a symlink at %s; refusing to read a symlinked project file", path, rev)
	case "160000", "040000", "40000":
		return nil, fmt.Errorf("%s is not a file at %s", path, rev)
	}
	// cat-file reads the blob by id. The path is not part of the object
	// name, so a newline or a bracket in the path cannot change which blob
	// is printed, and textconv does not run.
	if !isBlobID(oid) {
		return nil, fmt.Errorf("git ls-tree returned an unexpected object id")
	}
	data, err := git(root, "cat-file", "blob", oid)
	if err != nil {
		if badRevision(err) || missingPath(err) {
			return nil, fs.ErrNotExist
		}
		return nil, err
	}
	return data, nil
}

func validateRev(rev string) error {
	// A colon, space, tab, newline, NUL, or leading dash is not a revision
	// we pass to git. Newlines and tabs in a path are legal and are checked
	// separately.
	if rev == "" || strings.HasPrefix(rev, "-") || strings.ContainsAny(rev, ": \t\n\r\x00") {
		return fmt.Errorf("invalid git revision %q", rev)
	}
	return nil
}

func validateRelPath(path string) error {
	// Newlines and tabs are legal git path bytes. NUL cannot survive an
	// argument. A leading dash is safe after -- and the literal prefix.
	// `:(literal)` on ls-tree covers metacharacters such as '['.
	if path == "" || path == "." || strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return fmt.Errorf("git path must be a relative repository path, got %q", path)
	}
	return nil
}

// treeEntry reads mode and blob id from `git ls-tree -z` output:
// `<mode> SP <type> SP <object> TAB <path> NUL`.
// The path is not trimmed: a trailing newline is part of the name, and the
// id sits before the tab.
func treeEntry(out []byte) (mode, oid string, ok bool) {
	if i := bytes.IndexByte(out, 0); i >= 0 {
		out = out[:i]
	}
	if len(out) == 0 {
		return "", "", false
	}
	mode, rest, ok := strings.Cut(string(out), " ")
	if !ok || mode == "" {
		return "", "", false
	}
	_, rest, ok = strings.Cut(rest, " ")
	if !ok {
		return "", "", false
	}
	oid, _, ok = strings.Cut(rest, "\t")
	if !ok || oid == "" {
		return "", "", false
	}
	return mode, oid, true
}

func isBlobID(oid string) bool {
	if len(oid) < 4 {
		return false
	}
	for _, r := range oid {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func missingPath(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	msg := strings.ToLower(ce.Stderr)
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "exists on disk, but not in") ||
		strings.Contains(msg, "not a valid object name")
}
