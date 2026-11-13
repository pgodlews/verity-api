package plugin

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"

	"github.com/pgodlews/verity-api/core/secrets"
)

// redactorRef is a swappable redactor shared by a Manager's plugin stderr
// writers, so the host can attach the run's redactor after plugins load.
type redactorRef struct {
	p atomic.Pointer[secrets.Redactor]
}

func newRedactorRef() *redactorRef {
	r := &redactorRef{}
	r.p.Store(secrets.NewRedactor())
	return r
}

func (r *redactorRef) get() *secrets.Redactor { return r.p.Load() }

func (r *redactorRef) set(red *secrets.Redactor) {
	if red != nil {
		r.p.Store(red)
	}
}

// stderrWriter forwards a plugin's stderr to the host's stderr one line at a
// time, prefixed with "[plugin NAME] " and passed through the redactor, so
// plugin logs and panics are visible but never print known secrets.
type stderrWriter struct {
	mu     sync.Mutex
	dst    io.Writer
	prefix string
	red    *redactorRef
	buf    []byte
}

// maxPluginLine bounds a buffered partial line; longer output is flushed.
const maxPluginLine = 64 << 10

func newStderrWriter(dst io.Writer, name string, red *redactorRef) *stderrWriter {
	return &stderrWriter{dst: dst, prefix: "[plugin " + name + "] ", red: red}
}

func (w *stderrWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxPluginLine {
		w.emit(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

// Flush writes a trailing partial line, if any.
func (w *stderrWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

func (w *stderrWriter) emit(line []byte) {
	text := string(bytes.TrimRight(line, "\r"))
	_, _ = io.WriteString(w.dst, w.prefix+w.red.get().Redact(text)+"\n")
}
