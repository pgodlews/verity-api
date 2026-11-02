package workspace

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestSessionRejectsASecondRunAndCancelStopsTheFirst(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			once.Do(func() { close(started) })
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	body := fmt.Sprintf(`schemaVersion: "1.0"
name: Hold
environments:
  - name: local
    default: true
suites:
  - name: S
    cases:
      - name: A
        steps:
          - name: block
            protocol: rest
            request: {method: GET, url: %q}
      - name: B
        steps:
          - name: later
            protocol: rest
            request: {method: GET, url: %q}
`, srv.URL+"/block", srv.URL+"/later")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var s Session
	s.Cancel() // idle cancel is a no-op

	var (
		res *spi.RunResult
		err error
	)
	done := make(chan struct{})
	go func() {
		res, err = s.Run(context.Background(), RunOptions{Path: path}, nil, nil)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first run did not reach the server")
	}

	_, err2 := s.Run(context.Background(), RunOptions{Path: path}, nil, nil)
	var ve *verr.Error
	if !errors.As(err2, &ve) || ve.Code != verr.CodeBusy {
		t.Fatalf("second run error %v, want busy", err2)
	}

	s.Cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish the in-flight run")
	}
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.Summary.Aborted {
		t.Fatalf("result aborted=%v", res)
	}
	if len(res.Suites) != 1 || len(res.Suites[0].Cases) != 2 {
		t.Fatalf("cases: %+v", res.Suites)
	}
	if res.Suites[0].Cases[1].Status != spi.StatusAborted {
		t.Fatalf("later case status %s, want aborted", res.Suites[0].Cases[1].Status)
	}
}

func TestRunStreamForwardsEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	body := fmt.Sprintf(`schemaVersion: "1.0"
name: Stream
environments:
  - name: local
    default: true
suites:
  - name: S
    cases:
      - name: A
        steps:
          - name: ok
            protocol: rest
            request: {method: GET, url: %q}
            assertions:
              - {type: status, value: 200}
`, srv.URL+"/ok")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var kinds []string
	res, err := RunStream(context.Background(), RunOptions{Path: path}, func(ev spi.RunEvent) {
		kinds = append(kinds, ev.Kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("status %s", res.Summary.Status)
	}
	want := []string{spi.EventCaseStarted, spi.EventStepDone, spi.EventCaseDone}
	if len(kinds) != len(want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("events %v, want %v", kinds, want)
		}
	}

	plain, err := Run(context.Background(), RunOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Summary.Status != spi.StatusPassed || plain.Summary.Steps != 1 {
		t.Fatalf("Run without a callback: status %s steps %d", plain.Summary.Status, plain.Summary.Steps)
	}
}
