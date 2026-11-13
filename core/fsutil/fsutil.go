// Package fsutil holds the file-access rules shared by every part of Verity
// that reads a file a project names.
//
// A project may only read files inside its own directory unless the user
// runs it with --allow-external-files, so opening and running a downloaded
// project cannot send local files (keys, dotenv files) to its servers.
package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/verr"
)

// AllowExternalKey is the config key the runner injects (next to "__dir")
// into auth, assertion, and data-source config when --allow-external-files
// is set.
const AllowExternalKey = "__allowExternalFiles"

// ResolveInside resolves path for reading. A relative path is joined to dir.
// Unless allowExternal is true, the result must lie inside dir, both
// lexically and after resolving symlinks; otherwise a config_error that names
// what and path is returned. An unknown dir ("") refuses everything without
// allowExternal. A file that does not exist yet is not an error here; the
// caller's read reports it.
func ResolveInside(dir, path string, allowExternal bool, what string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", verr.Newf(verr.CodeConfig, "%s path is empty", what)
	}
	joined := path
	if !filepath.IsAbs(joined) && dir != "" {
		joined = filepath.Join(dir, joined)
	}
	joined = filepath.Clean(joined)
	if allowExternal {
		return joined, nil
	}
	outside := func() error {
		return verr.Newf(verr.CodeConfig,
			"%s %q is outside the project directory; run with --allow-external-files if this project is trusted", what, path).
			WithDetail("path", path)
	}
	if dir == "" {
		return "", verr.Newf(verr.CodeConfig,
			"%s %q cannot be checked against an unknown project directory; run with --allow-external-files if this project is trusted", what, path).
			WithDetail("path", path)
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", outside()
	}
	if !filepath.IsAbs(joined) {
		if joined, err = filepath.Abs(joined); err != nil {
			return "", outside()
		}
	}
	if !Within(root, joined) {
		return "", outside()
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", outside()
	}
	targetReal, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return joined, nil
		}
		return "", outside()
	}
	if !Within(rootReal, targetReal) {
		return "", outside()
	}
	return joined, nil
}

// ReadInside is ResolveInside followed by os.ReadFile.
func ReadInside(dir, path string, allowExternal bool, what string) ([]byte, error) {
	resolved, err := ResolveInside(dir, path, allowExternal, what)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(resolved)
}

// Within reports whether target is root or lies below it. Both must be
// absolute and cleaned.
func Within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// WriteKeepingMode replaces path atomically (temp file in the same
// directory, then rename) and keeps an existing file's permission bits; a new
// file gets 0644. A symlinked path is written through: its target is
// replaced and the link is kept.
func WriteKeepingMode(path string, data []byte) error {
	// Write through a symlinked project file: replace its target, not the
	// link, so the link and the file it points to stay as the user set them.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".verity-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// AtomicFile is a file written to a temporary name and renamed over its
// destination by Close, so readers never see a partial file.
type AtomicFile struct {
	*os.File
	dest string
	mode os.FileMode
	done bool
}

// CreateAtomic starts an atomic write of path, creating parent directories.
// Close commits it; Abort discards it.
func CreateAtomic(path string) (*AtomicFile, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".verity-write-*")
	if err != nil {
		return nil, err
	}
	return &AtomicFile{File: f, dest: path, mode: mode}, nil
}

// Close finishes the write and renames the file into place.
func (a *AtomicFile) Close() error {
	if a.done {
		return nil
	}
	a.done = true
	name := a.File.Name()
	if err := a.File.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, a.mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, a.dest); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// AllowExternal reads the injected AllowExternalKey flag from a config map.
func AllowExternal(config map[string]any) bool {
	v, _ := config[AllowExternalKey].(bool)
	return v
}
