package graphql

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A cached SDL file is not read again: it is served even when the file can
// no longer be read.
func TestLoadFileUsesCacheBeforeReading(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.graphql")
	if err := os.WriteFile(path, []byte("type Query { hello: String }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := NewCache()
	if _, _, err := cache.LoadFile(dir, "schema.graphql"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if _, sdl, err := cache.LoadFile(dir, "schema.graphql"); err != nil || sdl == "" {
		t.Fatalf("cached load read the file again: %v", err)
	}
}
