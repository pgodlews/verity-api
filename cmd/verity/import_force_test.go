package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportRefusesToOverwriteWithoutForce(t *testing.T) {
	spec, err := filepath.Abs(filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "project.yaml")
	if err := os.WriteFile(out, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	withStdout(t, func() { code = run([]string{"import", "openapi", spec, "--out", out, "--json"}) })
	if code != exitConfig {
		t.Fatalf("exit %d, want %d", code, exitConfig)
	}
	if b, _ := os.ReadFile(out); string(b) != "keep me\n" {
		t.Fatal("existing file overwritten without --force")
	}
	withStdout(t, func() { code = run([]string{"import", "openapi", spec, "--out", out, "--force", "--json"}) })
	if code != exitOK {
		t.Fatalf("--force exit %d", code)
	}
	if b, _ := os.ReadFile(out); string(b) == "keep me\n" {
		t.Fatal("--force did not write the project")
	}
}
