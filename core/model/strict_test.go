package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func requireUnknownField(t *testing.T, err error, field, path string) {
	t.Helper()
	got, ok := verr.As(err)
	if !ok || got.Code != verr.CodeValidation {
		t.Fatalf("error = %v, want %s", err, verr.CodeValidation)
	}
	if got.Details["field"] != field || got.Path != path {
		t.Fatalf("error field=%v path=%q, want %q at %q", got.Details["field"], got.Path, field, path)
	}
}

func TestDecodeRejectsUnknownKeys(t *testing.T) {
	yamlProject := `schemaVersion: "1.0"
name: typo
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: get
            protocol: rest
            request: { method: GET, url: "http://127.0.0.1/", anyKey: "free-form" }
            asertions:
              - type: status
                value: 200
`
	_, err := Decode([]byte(yamlProject), "p.yaml")
	requireUnknownField(t, err, "asertions", "suites[0].cases[0].steps[0]")

	jsonProject := `{"schemaVersion":"1.0","name":"typo","Suites":[{"name":"s","cases":[{"name":"c","retrys":2,"steps":[]}]}]}`
	_, err = Decode([]byte(jsonProject), "p.json")
	requireUnknownField(t, err, "retrys", "suites[0].cases[0]")

	_, err = Decode([]byte("schemaVersion: \"1.0\"\nnmae: x\n"), "p.yaml")
	requireUnknownField(t, err, "nmae", "")

	// Free-form maps (request, config, vars) accept any key.
	ok := `schemaVersion: "1.0"
name: ok
environments:
  - name: e
    variables: { anything: "x" }
suites:
  - name: s
    vars: { whatever: "y" }
    cases:
      - name: c
        steps:
          - name: get
            protocol: rest
            request: { method: GET, url: "http://127.0.0.1/", custom: { deep: 1 } }
            assertions:
              - type: json_schema
                config: { schema: { type: object, additionalProperties: false } }
`
	if _, err := Decode([]byte(ok), "p.yaml"); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}
}

func TestLoadRejectsUnknownKeyInFragment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "frag.yaml"), []byte("suites:\n  - name: s\n    caes: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(root, []byte("schemaVersion: \"1.0\"\nname: p\ninclude: [frag.yaml]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	requireUnknownField(t, err, "caes", "suites[0]")
}

func TestExamplesHaveNoUnknownKeys(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "examples", "*", "project.yaml"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no example projects found: %v", err)
	}
	for _, m := range matches {
		if _, err := Load(m); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}
