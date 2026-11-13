package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteKeepingModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingMode(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if b, _ := os.ReadFile(path); string(b) != "new" || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("content %q mode %v", b, st.Mode())
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingMode(link, []byte("via link")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link replaced")
	}
	if b, _ := os.ReadFile(path); string(b) != "via link" {
		t.Fatalf("target = %q", b)
	}
}

func TestCreateAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reports", "out.xml")
	f, err := CreateAtomic(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("<r/>"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("destination visible before Close")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "<r/>" {
		t.Fatalf("content %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}
