package watchfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestWatchDirsAndExistingAncestor(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "one", "two")
	files := []string{
		filepath.Join(nested, "b.yaml"),
		filepath.Join(dir, "a.yaml"),
		filepath.Join(nested, "b.yaml"),
		"relative.yaml",
	}
	wantDirs := []string{dir, nested}
	if got := WatchDirs(files); !reflect.DeepEqual(got, wantDirs) {
		t.Fatalf("WatchDirs = %v, want %v", got, wantDirs)
	}
	if got := WatchDirs(nil); len(got) != 0 {
		t.Fatalf("WatchDirs(nil) = %v", got)
	}
	if got := ExistingAncestor(nested); got != dir {
		t.Fatalf("ExistingAncestor = %q, want %q", got, dir)
	}
	if got := ExistingAncestor(dir); got != dir {
		t.Fatalf("ExistingAncestor(existing) = %q, want %q", got, dir)
	}
}

func TestEventTriggers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data", "feed.csv")
	files := map[string]struct{}{target: {}}
	tests := []struct {
		name string
		path string
		op   fsnotify.Op
		want bool
	}{
		{name: "write", path: target, op: fsnotify.Write, want: true},
		{name: "rename", path: target, op: fsnotify.Rename, want: true},
		{name: "chmod", path: target, op: fsnotify.Chmod, want: true},
		{name: "remove", path: target, op: fsnotify.Remove, want: true},
		{name: "created parent", path: filepath.Join(dir, "data"), op: fsnotify.Create, want: true},
		{name: "parent write", path: filepath.Join(dir, "data"), op: fsnotify.Write},
		{name: "sibling", path: filepath.Join(dir, "data", "other.csv"), op: fsnotify.Create},
		{name: "similar prefix", path: dir + "-other", op: fsnotify.Create},
		{name: "empty name", op: fsnotify.Write},
		{name: "empty operation", path: target},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EventTriggers(files, tt.path, tt.op); got != tt.want {
				t.Fatalf("EventTriggers(%q, %v) = %v, want %v", tt.path, tt.op, got, tt.want)
			}
		})
	}
}

func TestWatcherDetectsRenameSaveAndIgnoresSibling(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Set([]string{target, target})
	if !w.active[dir] {
		t.Fatalf("parent directory is not watched: %v", w.active)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type waitResult struct {
		paths []string
		err   error
	}
	result := make(chan waitResult, 1)
	go func() {
		paths, err := w.Wait(ctx, 20*time.Millisecond)
		result <- waitResult{paths: paths, err: err}
	}()
	if err := os.WriteFile(filepath.Join(dir, "other.yaml"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !reflect.DeepEqual(got.paths, []string{target}) {
			t.Fatalf("changed = %v, want [%s]", got.paths, target)
		}
	case <-ctx.Done():
		t.Fatal("watcher did not report rename save")
	}

	w.Set(nil)
	if len(w.active) != 0 {
		t.Fatalf("unused watches remain active: %v", w.active)
	}
}

func TestWatcherFindsFileBelowMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "missing", "nested", "project.yaml")
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Set([]string{target})
	if !w.active[dir] {
		t.Fatalf("nearest existing ancestor is not watched: %v", w.active)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := w.Wait(ctx, 20*time.Millisecond)
		result <- err
	}()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("creation below a missing directory was not reported")
	}
}

func TestWatcherCancellationAndClose(t *testing.T) {
	var nilWatcher *Watcher
	if err := nilWatcher.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	w.Set([]string{filepath.Join(t.TempDir(), "project.yaml")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Wait(ctx, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context cancellation", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := w.Wait(ctx, time.Millisecond); err == nil {
		t.Fatal("Wait succeeded after the event streams closed")
	}
}

func TestSuppressUnmarkAndNilReceivers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	body := []byte("content\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSuppress()
	s.Mark(path, body)
	if got := s.Decide(path); got != Drop {
		t.Fatalf("marked write decision = %v, want Drop", got)
	}
	s.Mark(path, body)
	s.Unmark(path)
	if got := s.Decide(path); got != Emit {
		t.Fatalf("unmarked write decision = %v, want Emit", got)
	}

	var nilSuppress *Suppress
	nilSuppress.Begin(path)
	nilSuppress.Mark(path, body)
	nilSuppress.Unmark(path)
	nilSuppress.End(path)
	nilSuppress.Forget()
	if got := nilSuppress.Decide(path); got != Emit {
		t.Fatalf("nil decision = %v, want Emit", got)
	}
	if err := nilSuppress.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
}
