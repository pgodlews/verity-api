package workspace

import (
	"os"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/importers"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// ImportStats counts what an import produced.
type ImportStats struct {
	Suites       int `json:"suites"`
	Cases        int `json:"cases"`
	Steps        int `json:"steps"`
	Assertions   int `json:"assertions"`
	Auth         int `json:"auth"`
	Environments int `json:"environments"`
}

// ImportPreview is the result of converting a spec without saving it.
type ImportPreview struct {
	YAML     string      `json:"yaml"`
	Warnings []string    `json:"warnings"`
	Stats    ImportStats `json:"stats"`
}

// ImportSaveRequest asks for an import to be written to Out.
type ImportSaveRequest struct {
	Kind      string   `json:"kind"`
	Spec      string   `json:"spec"`
	Envs      []string `json:"envs"`
	Out       string   `json:"out"`
	Overwrite bool     `json:"overwrite"`
}

// Preview converts a spec (mirrors `verity import`) and returns the YAML,
// the importer's warnings and counts, without writing anything.
func Preview(kind, spec string, envs []string) (*ImportPreview, error) {
	res, err := importers.Import(kind, spec, envs...)
	if err != nil {
		return nil, err
	}
	data, err := res.Project.Marshal("project.yaml")
	if err != nil {
		return nil, err
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return &ImportPreview{YAML: string(data), Warnings: warnings, Stats: stats(res.Project)}, nil
}

// Save converts a spec and writes the project to req.Out (relative paths are
// resolved next to the spec). It refuses to replace an existing file unless
// req.Overwrite is set. It returns the absolute path written.
func Save(req ImportSaveRequest) (string, error) {
	out := req.Out
	if !filepath.IsAbs(out) {
		out = filepath.Join(filepath.Dir(req.Spec), out)
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(out); err == nil && !req.Overwrite {
		return "", verr.Newf(verr.CodeConfig, "%s already exists", out)
	}
	res, err := importers.Import(req.Kind, req.Spec, req.Envs...)
	if err != nil {
		return "", err
	}
	if err := res.Project.Save(out); err != nil {
		return "", err
	}
	if _, err := Init(InitOptions{Dir: filepath.Dir(out), GitignoreOnly: true}); err != nil {
		return "", err
	}
	return out, nil
}

func stats(p *model.Project) ImportStats {
	s := ImportStats{Suites: len(p.Suites), Auth: len(p.Auth), Environments: len(p.Environments)}
	for _, su := range p.Suites {
		s.Cases += len(su.Cases)
		for _, c := range su.Cases {
			s.Steps += len(c.Steps)
			for _, st := range c.Steps {
				s.Assertions += len(st.Assertions)
			}
		}
	}
	return s
}
