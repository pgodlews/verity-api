package watchfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
)

// Decision is what the watch loop should do with one changed path.
type Decision int

const (
	// Emit delivers the path as an outside edit.
	Emit Decision = iota
	// Drop ignores it: the bytes on disk are what this process wrote.
	Drop
	// Hold means a save of this path is still in flight. Recheck after it finishes.
	Hold
)

// Suppress ignores filesystem events whose bytes still match a save this
// process made. Mark records the hash before the write; an outside edit that
// lands in the same moment has different bytes and is delivered. Time is not
// used: a different write a millisecond later is an outside edit.
type Suppress struct {
	mu       sync.Mutex
	hash     map[string]string
	inflight map[string]int
	waiters  []chan struct{}
}

// NewSuppress returns an empty suppression set.
func NewSuppress() *Suppress {
	return &Suppress{hash: map[string]string{}, inflight: map[string]int{}}
}

// Begin marks a save of file as in flight. Pair it with End, including when
// the write fails. Events seen while the count is positive are held.
func (s *Suppress) Begin(file string) {
	if s == nil || file == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight[cleanPath(file)]++
}

// Mark records the hash of the bytes about to be written. Call it before the
// rename so an event from the write itself already has a hash to match.
func (s *Suppress) Mark(file string, content []byte) {
	if s == nil || file == "" {
		return
	}
	sum := sha256.Sum256(content)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hash[cleanPath(file)] = hex.EncodeToString(sum[:])
}

// Unmark drops the hash recorded for file. A save that failed still called
// Mark with bytes that were not renamed; leaving that hash would hide a
// later write of those same bytes.
func (s *Suppress) Unmark(file string) {
	if s == nil || file == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hash, cleanPath(file))
}

// Forget drops remembered save hashes. A git operation Verity runs (pull,
// switch, merge, merge abort) rewrites files; those events must be delivered
// even when the bytes match an earlier save. In-flight Begin counts stay,
// so a save that is still running is held until End.
func (s *Suppress) Forget() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hash = map[string]string{}
}

// End finishes one Begin. When nothing is in flight, waiters blocked in
// WaitIdle are released.
func (s *Suppress) End(file string) {
	if s == nil || file == "" {
		return
	}
	s.mu.Lock()
	key := cleanPath(file)
	if n := s.inflight[key]; n > 1 {
		s.inflight[key] = n - 1
	} else {
		delete(s.inflight, key)
	}
	idle := len(s.inflight) == 0
	var waiters []chan struct{}
	if idle {
		waiters = s.waiters
		s.waiters = nil
	}
	s.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// Decide reads file and compares it with the hash Mark stored.
// Matching bytes are a self-write. A different file while Begin is unmatched
// is held so the watch loop can look again after End.
func (s *Suppress) Decide(file string) Decision {
	if s == nil || file == "" {
		return Emit
	}
	key := cleanPath(file)
	sum, err := hashFile(file)
	s.mu.Lock()
	defer s.mu.Unlock()
	want, marked := s.hash[key]
	busy := s.inflight[key] > 0
	if err == nil && marked && sum == want {
		return Drop
	}
	if busy {
		return Hold
	}
	if marked {
		delete(s.hash, key)
	}
	return Emit
}

// WaitIdle blocks until every Begin has been Ended, or ctx is cancelled.
func (s *Suppress) WaitIdle(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if len(s.inflight) == 0 {
		s.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	s.waiters = append(s.waiters, ch)
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		s.mu.Lock()
		kept := make([]chan struct{}, 0, len(s.waiters))
		for _, w := range s.waiters {
			if w != ch {
				kept = append(kept, w)
			}
		}
		s.waiters = kept
		s.mu.Unlock()
		return ctx.Err()
	case <-ch:
		return nil
	}
}

func hashFile(file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func cleanPath(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return filepath.Clean(file)
	}
	return filepath.Clean(abs)
}
