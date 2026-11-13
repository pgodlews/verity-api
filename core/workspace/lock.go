package workspace

import (
	"path/filepath"
	"sync"
)

// repoLocks serializes SaveStep, SaveEnvironment, and git writes that share
// a repository. The compare-and-rename in a save then cannot interleave with
// another Verity write of the same work tree. It does not lock out editors
// outside this process.
var (
	repoMu    sync.Mutex
	repoLocks = map[string]*repoLock{}
)

// repoLock is one repository's mutex plus how many callers hold or wait for
// it, so the entry can be removed when nobody uses it.
type repoLock struct {
	mu    sync.Mutex
	users int
}

func lockProject(projectPath string) func() {
	key := projectPath
	if abs, err := filepath.Abs(projectPath); err == nil {
		key = abs
	}
	if root, err := repoRootOf(projectPath); err == nil && root != "" {
		key = root
	}
	repoMu.Lock()
	l := repoLocks[key]
	if l == nil {
		l = &repoLock{}
		repoLocks[key] = l
	}
	l.users++
	repoMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		repoMu.Lock()
		l.users--
		if l.users == 0 {
			delete(repoLocks, key)
		}
		repoMu.Unlock()
	}
}
