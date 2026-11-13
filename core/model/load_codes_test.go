package model

import (
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestLoadErrorsCarryCodes(t *testing.T) {
	_, missing := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	_, badYAML := Decode([]byte("a: [unterminated"), "p.yaml")
	_, badJSON := Decode([]byte(`{"name":`), "p.json")
	_, noEnv := (&Project{Environments: []Environment{{Name: "a"}}}).SelectEnvironment("b")
	for _, tc := range []struct {
		name string
		err  error
		code verr.Code
	}{
		{"missing file", missing, verr.CodeConfig},
		{"bad yaml", badYAML, verr.CodeValidation},
		{"bad json", badJSON, verr.CodeValidation},
		{"unknown env", noEnv, verr.CodeNotFound},
	} {
		if got, ok := verr.As(tc.err); !ok || got.Code != tc.code {
			t.Errorf("%s: error = %v, want %s", tc.name, tc.err, tc.code)
		}
	}
}
