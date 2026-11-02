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
	repoLocks = map[string]*sync.Mutex{}
)

func lockProject(projectPath string) func() {
	key := projectPath
	if abs, err := filepath.Abs(projectPath); err == nil {
		key = abs
	}
	if root, err := repoRootOf(projectPath); err == nil && root != "" {
		key = root
	}
	repoMu.Lock()
	m := repoLocks[key]
	if m == nil {
		m = &sync.Mutex{}
		repoLocks[key] = m
	}
	repoMu.Unlock()
	m.Lock()
	return m.Unlock
}
