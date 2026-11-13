package workspace

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestRepoLocksArePruned(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unlock := lockProject(filepath.Join(dir, "p.yaml"))
			unlock()
		}(i)
	}
	wg.Wait()
	for i := 0; i < 50; i++ {
		lockProject(filepath.Join(t.TempDir(), "q.yaml"))()
	}
	repoMu.Lock()
	n := len(repoLocks)
	repoMu.Unlock()
	if n != 0 {
		t.Fatalf("%d repository locks left after every holder released them", n)
	}
}
