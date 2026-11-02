package mock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// readExternal loads an OpenAPI externalValue example. The reference must be
// a relative path inside the spec file's directory: no URL, no absolute path,
// no ".." component, and no symlink that resolves outside that directory.
func readExternal(specPath, ref string) ([]byte, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("empty externalValue")
	}
	if specPath == "" {
		return nil, errors.New("externalValue needs a spec file path")
	}
	if strings.Contains(ref, "://") || strings.ContainsAny(ref, "\\") || filepath.IsAbs(ref) {
		return nil, errors.New("externalValue must be a file relative to the spec")
	}
	if hasDotDot(ref) {
		return nil, errors.New("externalValue escapes the spec directory")
	}
	dir := filepath.Dir(specPath)
	full := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	rel, err := filepath.Rel(dir, full)
	if err != nil || escapes(rel) {
		return nil, errors.New("externalValue escapes the spec directory")
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(full); err != nil {
		return nil, err
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, err
	}
	rel, err = filepath.Rel(realDir, realFull)
	if err != nil || escapes(rel) {
		return nil, errors.New("externalValue escapes the spec directory")
	}
	b, err := os.ReadFile(realFull)
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return nil, errors.New("externalValue exceeds 1MiB")
	}
	return b, nil
}

func hasDotDot(ref string) bool {
	for _, p := range strings.FieldsFunc(ref, func(r rune) bool { return r == '/' || r == '\\' }) {
		if p == ".." {
			return true
		}
	}
	return false
}

func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
