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

// AllowExternal reads the injected AllowExternalKey flag from a config map.
func AllowExternal(config map[string]any) bool {
	v, _ := config[AllowExternalKey].(bool)
	return v
}
