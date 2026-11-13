package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func requireOutside(t *testing.T, err error) {
	t.Helper()
	got, ok := verr.As(err)
	if !ok || got.Code != verr.CodeConfig {
		t.Fatalf("error = %v, want %s", err, verr.CodeConfig)
	}
}

func TestResolveInside(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveInside(project, "sub/a.txt", false, "file")
	if err != nil || got != filepath.Join(project, "sub", "a.txt") {
		t.Fatalf("relative inside = %q, %v", got, err)
	}
	if _, err := ResolveInside(project, filepath.Join(project, "sub", "a.txt"), false, "file"); err != nil {
		t.Fatalf("absolute inside: %v", err)
	}
	for _, p := range []string{"../secret.txt", outsideFile, "sub/../../secret.txt"} {
		_, err := ResolveInside(project, p, false, "file")
		requireOutside(t, err)
		if _, err := ResolveInside(project, p, true, "file"); err != nil {
			t.Fatalf("%s with allowExternal: %v", p, err)
		}
	}
	_, err = ResolveInside("", outsideFile, false, "file")
	requireOutside(t, err)
	if _, err := ResolveInside("", outsideFile, true, "file"); err != nil {
		t.Fatalf("unknown dir with allowExternal: %v", err)
	}
	_, err = ResolveInside(project, " ", true, "file")
	requireOutside(t, err)

	if runtime.GOOS != "windows" {
		link := filepath.Join(project, "link.txt")
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Fatal(err)
		}
		_, err := ResolveInside(project, "link.txt", false, "file")
		requireOutside(t, err)
	}
	if _, err := ReadInside(project, "../secret.txt", false, "file"); err == nil {
		t.Fatal("ReadInside read outside the project")
	}
	if !AllowExternal(map[string]any{AllowExternalKey: true}) || AllowExternal(nil) {
		t.Fatal("AllowExternal flag")
	}
}
