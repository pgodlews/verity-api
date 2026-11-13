package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestIncludeIsConfinedToTheProjectDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "project")
	for path, body := range map[string]string{
		filepath.Join(dir, "project.yaml"):   "schemaVersion: \"1.0\"\nname: p\ninclude: [sub/inner.yaml]\n",
		filepath.Join(dir, "sub/inner.yaml"): "include: [../../outside.yaml]\n",
		filepath.Join(base, "outside.yaml"):  "suites: []\n",
		filepath.Join(dir, "inside.yaml"):    "schemaVersion: \"1.0\"\nname: q\ninclude: [sub/ok.yaml]\n",
		filepath.Join(dir, "sub/ok.yaml"):    "suites: []\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A nested include is checked against the root project's directory.
	_, err := Load(filepath.Join(dir, "project.yaml"))
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodeConfig || ve.Details[fsutil.OutsideProjectDetail] != true {
		t.Fatalf("error = %v", err)
	}
	if _, err := LoadWith(filepath.Join(dir, "project.yaml"), LoadOptions{AllowExternalFiles: true}); err != nil {
		t.Fatalf("with the flag: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "inside.yaml")); err != nil {
		t.Fatalf("inside include: %v", err)
	}
}
