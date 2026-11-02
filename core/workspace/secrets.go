package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/verr"
)

// ReadFunc reads one project file. ScanSecrets uses os.ReadFile by default;
// `verity scan-secrets --staged` passes one that reads the git index. A
// file that does not exist there (fs.ErrNotExist) is skipped.
type ReadFunc func(file string) ([]byte, error)

// WalkProject calls fn for a project file and every fragment it includes,
// as read by read (os.ReadFile when nil), in include order. rel is the file
// relative to the project file's directory, with forward slashes.
func WalkProject(path string, read ReadFunc, fn func(file, rel string, data []byte) error) error {
	if read == nil {
		read = os.ReadFile
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(root)
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(f string) error {
		if seen[f] {
			return nil
		}
		seen[f] = true
		data, err := read(f)
		if errors.Is(err, fs.ErrNotExist) && f != root {
			return nil
		}
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, f)
		if relErr != nil {
			rel = f
		}
		if err := fn(f, filepath.ToSlash(rel), data); err != nil {
			return err
		}
		p, err := model.Decode(data, f)
		if err != nil {
			return err
		}
		for _, inc := range p.Include {
			if !filepath.IsAbs(inc) {
				inc = filepath.Join(filepath.Dir(f), inc)
			}
			if err := walk(filepath.Clean(inc)); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

// ScanSecrets scans a project file and every fragment it includes (as read
// by read) for plaintext credentials. Finding.File is relative to the
// project file's directory.
func ScanSecrets(path string, read ReadFunc) ([]secretscan.Finding, error) {
	var out []secretscan.Finding
	err := WalkProject(path, read, func(_, rel string, data []byte) error {
		found, err := secretscan.ScanFile(rel, data)
		out = append(out, found...)
		return err
	})
	return out, err
}

// CheckStep reports the plaintext credentials in a step, with paths
// relative to the step ("request", "headers", "X-Api-Key"). The GUI shows
// them next to the fields and offers ${env:NAME} instead; SaveStep refuses a
// step that has any.
func CheckStep(step model.Step) ([]secretscan.Finding, error) {
	step, err := normalizedStepJSONText(step)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(step)
	if err != nil {
		return nil, err
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	out := secretscan.ScanValue(v)
	if out == nil {
		out = []secretscan.Finding{}
	}
	return out, nil
}

// literalSecretError is the SaveStep/SaveEnvironment refusal for findings.
func literalSecretError(found []secretscan.Finding) error {
	msgs := make([]string, len(found))
	for i, f := range found {
		msgs[i] = f.Message()
	}
	return verr.New(verr.CodeValidation, strings.Join(msgs, "; ")).WithDetail("secrets", len(found))
}
