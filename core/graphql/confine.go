package graphql

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/verr"
)

// maxSchemaBytes is the largest SDL or introspection body Verity will read.
const maxSchemaBytes = 8 << 20

// OpenConfined reads an SDL file. path is relative to dir, or absolute if it
// still resolves inside dir. Symlinks are followed and must also stay inside
// dir. A path outside the project is rejected even when the file is missing.
func OpenConfined(dir, path string) (abs string, body []byte, err error) {
	abs, err = confinedPath(dir, path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, verr.Newf(verr.CodeValidation, "schema file %q not found", path)
		}
		return "", nil, verr.Newf(verr.CodeValidation, "schema file %q cannot be read", path)
	}
	if info.IsDir() {
		return "", nil, verr.Newf(verr.CodeValidation, "schema file %q is a directory", path)
	}
	if info.Size() > maxSchemaBytes {
		return "", nil, verr.Newf(verr.CodeValidation, "schema file %q is larger than %d bytes", path, maxSchemaBytes)
	}
	body, err = os.ReadFile(abs)
	if err != nil {
		return "", nil, verr.Newf(verr.CodeValidation, "schema file %q cannot be read", path)
	}
	return abs, body, nil
}

func confinedPath(dir, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", verr.New(verr.CodeValidation, "GraphQL schema file path is empty")
	}
	if strings.ContainsAny(path, "\r\n") {
		return "", verr.New(verr.CodeValidation, "GraphQL schema file path must be a single line")
	}
	if dir == "" {
		return "", verr.New(verr.CodeValidation, "project directory is unknown")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", verr.New(verr.CodeValidation, "project directory is unknown")
	}
	joined := path
	if !filepath.IsAbs(joined) {
		joined = filepath.Join(root, joined)
	}
	joined = filepath.Clean(joined)
	if !fsutil.Within(root, joined) {
		return "", verr.Newf(verr.CodeValidation, "schema file %q is outside the project directory", path)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", verr.New(verr.CodeValidation, "project directory is unknown")
	}
	targetReal, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			return "", verr.Newf(verr.CodeValidation, "schema file %q not found", path)
		}
		return "", verr.Newf(verr.CodeValidation, "schema file %q cannot be read", path)
	}
	if !fsutil.Within(rootReal, targetReal) {
		return "", verr.Newf(verr.CodeValidation, "schema file %q is outside the project directory", path)
	}
	return targetReal, nil
}
