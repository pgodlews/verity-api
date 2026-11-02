package workspace

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/load"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestLoadStreamTargetsCancelAndSaveReport(t *testing.T) {
	const secret = "workspace-load-secret-token"
	t.Setenv("WS_LOAD_SECRET", secret)

	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			once.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		time.Sleep(15 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q}`, secret)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	body := fmt.Sprintf(`schemaVersion: "1.0"
name: LoadWS
environments:
  - name: local
    default: true
    variables:
      baseUrl: %q
  - name: remote
    variables:
      baseUrl: "http://203.0.113.10"
suites:
  - name: S
    cases:
      - name: fast
        steps:
          - name: ping
            protocol: rest
            request:
              method: GET
              url: "${baseUrl}/ok"
              headers:
                Authorization: "Bearer ${env:WS_LOAD_SECRET}"
            assertions:
              - {type: jsonpath, expr: "$.token", value: "expected-other"}
      - name: slow
        steps:
          - name: block
            protocol: rest
            request: {method: GET, url: "${baseUrl}/block"}
`, srv.URL)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Target check on remote environment reports the refusal without traffic.
	targets, err := LoadTargets(context.Background(), LoadOptions{
		Path:        path,
		Environment: "remote",
		Cases:       []string{"fast"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets.Refused) != 1 || targets.Refused[0] != "203.0.113.10" || !strings.Contains(targets.Message, "allow-remote") {
		t.Fatalf("remote targets: %+v", targets)
	}

	// Refusal enforced when starting load without AllowRemote.
	_, err = Load(context.Background(), LoadOptions{
		Path:        path,
		Environment: "remote",
		Cases:       []string{"fast"},
		VUs:         1,
		Iterations:  1,
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") {
		t.Fatalf("expected remote target refusal, got %v", err)
	}

	// Live stream + redacted report + SaveLoadReport.
	var (
		mu      sync.Mutex
		samples []load.LiveStats
	)
	rep, err := LoadStream(context.Background(), LoadOptions{
		Path:         path,
		Environment:  "local",
		Cases:        []string{"fast"},
		VUs:          2,
		DurationMs:   120,
		Thresholds:   []string{"p95<5000ms", "ping:p95<5000ms"},
		MaxErrorRate: "100%",
		LiveEveryMs:  25,
	}, func(st load.LiveStats) {
		mu.Lock()
		samples = append(samples, st)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.Summary.Requests < 1 {
		t.Fatalf("rep passed=%v requests=%d", rep.Passed, rep.Summary.Requests)
	}
	mu.Lock()
	gotSamples := len(samples)
	mu.Unlock()
	if gotSamples == 0 {
		t.Fatal("expected live samples from LoadStream")
	}
	for _, msg := range rep.Summary.Errors {
		if strings.Contains(msg, secret) {
			t.Fatalf("secret leaked in summary errors: %s", msg)
		}
	}

	outPath, err := SaveLoadReport(SaveLoadReportRequest{
		Path:   path,
		Out:    "load-report.json",
		Report: rep,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("secret leaked in saved JSON report")
	}
	// Saving again without Overwrite fails with exists error.
	if _, err := SaveLoadReport(SaveLoadReportRequest{Path: path, Out: "load-report.json", Report: rep}); err == nil {
		t.Fatal("expected overwrite refusal")
	}

	// Session.Load rejects a concurrent run and Cancel stops the in-flight load.
	var sess Session
	done := make(chan struct{})
	var (
		cancelRep *load.Report
		cancelErr error
	)
	go func() {
		cancelRep, cancelErr = sess.Load(context.Background(), LoadOptions{
			Path:       path,
			Cases:      []string{"slow"},
			VUs:        1,
			DurationMs: 10_000,
			TimeoutMs:  2000,
		}, nil, nil)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("slow load did not reach server")
	}

	_, busyErr := sess.Load(context.Background(), LoadOptions{
		Path:       path,
		Cases:      []string{"fast"},
		VUs:        1,
		Iterations: 1,
	}, nil, nil)
	var ve *verr.Error
	if !errors.As(busyErr, &ve) || ve.Code != verr.CodeBusy {
		t.Fatalf("concurrent Load error = %v, want busy", busyErr)
	}

	sess.Cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Cancel did not stop Session.Load")
	}
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if cancelRep == nil || !cancelRep.Interrupted {
		t.Fatalf("cancelled report: %+v", cancelRep)
	}
}
