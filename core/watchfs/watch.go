// Package watchfs watches a set of files for editor saves.
//
// The parent directory is watched, not the file: vim and VS Code save by
// writing a temp file and renaming it into place, which drops a watch on the
// old inode. Events for other names in those directories are ignored. A
// missing directory is replaced by its nearest existing ancestor so creating
// it (and then the file) is noticed. A short trailing debounce collapses the
// burst of events one save produces.
//
// verity run --watch and the desktop app both use this. The CLI owns the
// re-run; the app owns what it emits.
package watchfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debounce is how long a burst of filesystem events must stay quiet before
// it counts as one change. Editors emit several events for one save.
const Debounce = 300 * time.Millisecond

// Watcher is one fsnotify watcher and the set of files it cares about.
type Watcher struct {
	fs     *fsnotify.Watcher
	active map[string]bool
	files  map[string]struct{}
}

// Start opens a watcher. Close it when the watch stops.
func Start() (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{fs: w, active: map[string]bool{}}, nil
}

// Close releases the watcher.
func (w *Watcher) Close() error {
	if w == nil || w.fs == nil {
		return nil
	}
	return w.fs.Close()
}

// Set watches the parent directory of each file and drops directories that
// are no longer needed.
func (w *Watcher) Set(files []string) {
	w.files = make(map[string]struct{}, len(files))
	for _, f := range files {
		w.files[filepath.Clean(f)] = struct{}{}
	}
	EnsureWatches(w.fs, WatchDirs(files), w.active)
}

// Wait blocks until a watched file changes and the debounce window stays
// quiet, or until ctx is cancelled. The returned paths are the cleaned
// absolute paths that had a matching event during the window.
func (w *Watcher) Wait(ctx context.Context, debounce time.Duration) ([]string, error) {
	if debounce <= 0 {
		debounce = Debounce
	}
	return WaitForSourceChange(ctx, w.fs, w.files, debounce)
}

// WatchDirs returns the parent directory of each file, deduplicated and sorted.
func WatchDirs(files []string) []string {
	set := make(map[string]struct{}, len(files))
	for _, f := range files {
		dir := filepath.Clean(filepath.Dir(f))
		if dir == "" || dir == "." {
			continue
		}
		set[dir] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// ExistingAncestor returns dir, or its nearest parent that exists.
func ExistingAncestor(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// EnsureWatches adds a directory watch for each parent and drops directories
// that no longer contain a source file. A directory that does not exist yet
// is replaced by its nearest existing ancestor. Adding a watch that is already
// active is a no-op, so a directory that was deleted and recreated is picked
// up again.
func EnsureWatches(w *fsnotify.Watcher, dirs []string, active map[string]bool) {
	want := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		want[ExistingAncestor(d)] = struct{}{}
	}
	for dir := range active {
		if _, ok := want[dir]; ok {
			continue
		}
		if err := w.Remove(dir); err != nil {
			fmt.Fprintf(os.Stderr, "watch: remove %s: %v\n", dir, err)
		}
		delete(active, dir)
	}
	for dir := range want {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := w.Add(dir); err != nil {
			fmt.Fprintf(os.Stderr, "watch: %s: %v\n", dir, err)
			continue
		}
		active[dir] = true
	}
}

// WaitForSourceChange returns when a source file has an event and the debounce
// window then stays quiet, or when ctx is cancelled. changed lists the source
// files (and created ancestor directories) that matched.
func WaitForSourceChange(ctx context.Context, w *fsnotify.Watcher, files map[string]struct{}, debounce time.Duration) ([]string, error) {
	var (
		deadline time.Time
		timer    *time.Timer
		timerC   <-chan time.Time
		changed  []string
		seen     = map[string]struct{}{}
	)
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	defer stopTimer()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timerC:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if DebounceFired(deadline, time.Now()) {
				return changed, nil
			}
		case ev, ok := <-w.Events:
			if !ok {
				return nil, fmt.Errorf("watch: event stream closed")
			}
			if !EventTriggers(files, ev.Name, ev.Op) {
				continue
			}
			if abs, err := filepath.Abs(ev.Name); err == nil {
				abs = filepath.Clean(abs)
				if _, dup := seen[abs]; !dup {
					seen[abs] = struct{}{}
					changed = append(changed, abs)
				}
			}
			deadline = PushDebounce(deadline, time.Now(), debounce)
			stopTimer()
			timer = time.NewTimer(time.Until(deadline))
			timerC = timer.C
		case err, ok := <-w.Errors:
			if !ok {
				return nil, fmt.Errorf("watch: error stream closed")
			}
			fmt.Fprintf(os.Stderr, "watch: %v\n", err)
		}
	}
}

// PushDebounce moves a trailing debounce deadline. An event at eventAt fires
// at eventAt+d unless a later event pushes the deadline further out. An event
// timestamp that would land earlier does not shorten the window.
func PushDebounce(deadline, eventAt time.Time, d time.Duration) time.Time {
	next := eventAt.Add(d)
	if next.After(deadline) {
		return next
	}
	return deadline
}

// DebounceFired reports whether now is at or past a pending debounce deadline.
func DebounceFired(deadline, now time.Time) bool {
	return !deadline.IsZero() && !now.Before(deadline)
}

// EventTriggers reports whether an fsnotify event names one of the source
// files, or creates a missing directory on the way to one. Sibling files in
// a watched directory do not trigger.
func EventTriggers(files map[string]struct{}, name string, op fsnotify.Op) bool {
	if name == "" || op == 0 {
		return false
	}
	abs, err := filepath.Abs(name)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	if _, ok := files[abs]; ok {
		return true
	}
	if op&fsnotify.Create == 0 {
		return false
	}
	prefix := abs + string(filepath.Separator)
	for f := range files {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}
