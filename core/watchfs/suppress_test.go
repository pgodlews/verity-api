package watchfs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSuppressMatchesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	body := []byte("version: 1\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSuppress()
	s.Begin(path)
	s.Mark(path, body)
	if s.Decide(path) != Drop {
		t.Fatal("bytes Verity wrote are not an outside edit")
	}
	s.End(path)
	if s.Decide(path) != Drop {
		t.Fatal("the hash still matches after the save returns")
	}
	other := []byte("version: 2\n")
	if err := os.WriteFile(path, other, 0o644); err != nil {
		t.Fatal(err)
	}
	if s.Decide(path) != Emit {
		t.Fatal("a different write is delivered even when it follows immediately")
	}
	if s.Decide(filepath.Join(dir, "missing.yaml")) != Emit {
		t.Fatal("an unmarked path is an outside edit")
	}
}

func TestSuppressForgetDeliversGitRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	body := []byte("version: 1\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSuppress()
	s.Begin(path)
	s.Mark(path, body)
	s.End(path)
	if s.Decide(path) != Drop {
		t.Fatal("the save itself is still suppressed before Forget")
	}
	s.Forget()
	if s.Decide(path) != Emit {
		t.Fatal("a git rewrite of the same bytes must not be hidden as a self-write")
	}
}

func TestSuppressHoldsUntilWriteFinishes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	old := []byte("old\n")
	next := []byte("new\n")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSuppress()
	s.Begin(path)
	s.Mark(path, next)
	if s.Decide(path) != Hold {
		t.Fatal("the pre-write bytes must be held, not reported as an outside edit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- s.WaitIdle(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, next, 0o644); err != nil {
		t.Fatal(err)
	}
	s.End(path)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Decide(path) != Drop {
		t.Fatal("after End the written bytes are suppressed")
	}
	cancel()
}

func TestSuppressWaitIdleCancel(t *testing.T) {
	s := NewSuppress()
	s.Begin("/proj/a.yaml")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.WaitIdle(ctx); err == nil {
		t.Fatal("a cancelled watch should not wait out a save")
	}
	s.End("/proj/a.yaml")
}

func TestSuppressConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	body := []byte("ok\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSuppress()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Begin(path)
			s.Mark(path, body)
			s.Decide(path)
			s.End(path)
			s.Decide(path)
		}()
	}
	wg.Wait()
}

func TestDebounce(t *testing.T) {
	if Debounce != 300*time.Millisecond {
		t.Fatalf("Debounce = %s, want 300ms", Debounce)
	}
	t0 := time.Unix(1_700_000_000, 0)
	deadline := PushDebounce(time.Time{}, t0, Debounce)
	if !deadline.Equal(t0.Add(Debounce)) {
		t.Fatalf("first deadline %s", deadline)
	}
	later := t0.Add(100 * time.Millisecond)
	pushed := PushDebounce(deadline, later, Debounce)
	if !pushed.Equal(later.Add(Debounce)) {
		t.Fatalf("pushed deadline %s", pushed)
	}
	if got := PushDebounce(pushed, t0, Debounce); !got.Equal(pushed) {
		t.Fatalf("earlier event shortened the window to %s", got)
	}
	if DebounceFired(time.Time{}, t0) {
		t.Fatal("zero deadline fired")
	}
	if !DebounceFired(pushed, pushed) {
		t.Fatal("deadline itself did not fire")
	}
}
