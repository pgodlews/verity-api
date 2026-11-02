package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/model"
)

type projectServer struct {
	delay time.Duration
	mocks []compiledMock
}

type compiledMock struct {
	spec  int
	index int
	segs  []seg
	mock  model.Mock
}

func newProjectHandler(p *model.Project, opts Options) http.Handler {
	mocks := make([]compiledMock, len(p.Mocks))
	for i, m := range p.Mocks {
		segs := parseTemplate(m.Path)
		mocks[i] = compiledMock{spec: specificity(segs), index: i, segs: segs, mock: m}
	}
	return &projectServer{delay: opts.Delay, mocks: mocks}
}

func (s *projectServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(r)
	if err != nil {
		s.respond(w, r, s.delay, func() {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Payload Too Large", err.Error(), nil)
		})
		return
	}
	var best *compiledMock
	bestScore := -1
	for i := range s.mocks {
		m := &s.mocks[i]
		if !strings.EqualFold(m.mock.Method, r.Method) {
			continue
		}
		if _, ok := matchPath(m.segs, r.URL.Path); !ok {
			continue
		}
		if !rulesMatch(m.mock.Match, r, body) {
			continue
		}
		score := m.spec*100 + matchWeight(m.mock.Match)
		if score > bestScore {
			best = m
			bestScore = score
		}
	}
	if best == nil {
		s.respond(w, r, s.delay, func() {
			writeProblem(w, http.StatusNotFound, "Not Found",
				fmt.Sprintf("no mock for %s %s", r.Method, r.URL.Path), nil)
		})
		return
	}
	d := s.delay
	if best.mock.DelayMs > 0 {
		d = time.Duration(best.mock.DelayMs) * time.Millisecond
	}
	s.respond(w, r, d, func() {
		s.writeMatch(w, best.mock)
	})
}

func (s *projectServer) respond(w http.ResponseWriter, r *http.Request, d time.Duration, write func()) {
	pause(r, d)
	if r.Context().Err() != nil {
		return
	}
	write()
}

func (s *projectServer) writeMatch(w http.ResponseWriter, mock model.Mock) {
	status := mock.Status
	if status == 0 {
		status = http.StatusOK
	}
	for k, v := range mock.Headers {
		w.Header().Set(k, v)
	}
	if mock.JSON != nil {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(mock.JSON)
		return
	}
	if w.Header().Get("Content-Type") == "" && looksJSON([]byte(mock.Body)) {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(mock.Body))
}

func matchWeight(m *model.MockMatch) int {
	if m == nil {
		return 0
	}
	n := len(m.Query) + len(m.Headers)
	if m.BodyContains != "" {
		n++
	}
	return n
}

func rulesMatch(m *model.MockMatch, r *http.Request, body []byte) bool {
	if m == nil {
		return true
	}
	q := r.URL.Query()
	for k, want := range m.Query {
		if q.Get(k) != want {
			return false
		}
	}
	for k, want := range m.Headers {
		if !headerEquals(r.Header, k, want) {
			return false
		}
	}
	if m.BodyContains != "" && !bytes.Contains(body, []byte(m.BodyContains)) {
		return false
	}
	return true
}

func headerEquals(h http.Header, name, want string) bool {
	for k, vs := range h {
		if !strings.EqualFold(k, name) {
			continue
		}
		for _, v := range vs {
			if v == want {
				return true
			}
		}
	}
	return false
}

func looksJSON(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && (t[0] == '{' || t[0] == '[')
}
