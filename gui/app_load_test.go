package main

import (
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
	"github.com/pgodlews/verity-api/core/workspace"
)

func TestAppStartLoadCancelLiveTargetsAndSave(t *testing.T) {
	const secret = "gui-binding-secret-token"
	t.Setenv("GUI_LOAD_SECRET", secret)

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
name: DesktopLoad
environments:
  - name: local
    default: true
    variables:
      baseUrl: %q
  - name: public
    variables:
      baseUrl: "http://203.0.113.10"
suites:
  - name: Smoke
    cases:
      - name: list
        steps:
          - name: GET widgets
            protocol: rest
            request:
              method: GET
              url: "${baseUrl}/widgets"
              headers:
                Authorization: "Bearer ${env:GUI_LOAD_SECRET}"
            assertions:
              - {type: jsonpath, expr: "$.token", value: "mismatch"}
      - name: hang
        steps:
          - name: block
            protocol: rest
            request:
              method: GET
              url: "${baseUrl}/block"
`, srv.URL)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		mu      sync.Mutex
		samples []load.LiveStats
	)
	app := NewApp()
	app.onLoadLive = func(st load.LiveStats) {
		mu.Lock()
		samples = append(samples, st)
		mu.Unlock()
	}

	// 1. LoadTargets previews hosts and flags non-private targets with the CLI message.
	pubTargets, err := app.LoadTargets(workspace.LoadOptions{
		Path:        path,
		Environment: "public",
		Cases:       []string{"list"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pubTargets.Refused) != 1 || pubTargets.Refused[0] != "203.0.113.10" {
		t.Fatalf("refused targets: %+v", pubTargets)
	}
	if pubTargets.Message != "refusing non-private target 203.0.113.10; pass --allow-remote to run it" {
		t.Fatalf("refusal message %q", pubTargets.Message)
	}

	// 2. StartLoad refuses non-private targets unless AllowRemote is set.
	_, err = app.StartLoad(workspace.LoadOptions{
		Path:        path,
		Environment: "public",
		Cases:       []string{"list"},
		VUs:         1,
		Iterations:  1,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing non-private target 203.0.113.10; pass --allow-remote to run it") {
		t.Fatalf("StartLoad public target error: %v", err)
	}

	// 3. StartLoad against local httptest server streams live stats, evaluates thresholds, and redacts secrets.
	rep, err := app.StartLoad(workspace.LoadOptions{
		Path:         path,
		Environment:  "local",
		Suites:       []string{"Smoke"},
		Cases:        []string{"list"},
		VUs:          2,
		DurationMs:   120,
		RampUpMs:     20,
		ThinkMs:      5,
		Thresholds:   []string{"p95<5000ms", "GET widgets:p95<5000ms"},
		MaxErrorRate: "100%",
		LiveEveryMs:  25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.Summary.Requests < 1 || len(rep.Steps) != 1 || rep.Steps[0].Name != "GET widgets" {
		t.Fatalf("unexpected report: %+v", rep)
	}
	mu.Lock()
	liveCount := len(samples)
	mu.Unlock()
	if liveCount == 0 {
		t.Fatal("expected live stats callback during StartLoad")
	}
	for _, e := range rep.Summary.Errors {
		if strings.Contains(e, secret) {
			t.Fatalf("secret leaked in report error: %s", e)
		}
	}

	// 4. SaveLoadReport writes a redacted JSON report file.
	reportOut := filepath.Join(dir, "saved-load.json")
	written, err := app.SaveLoadReport(workspace.SaveLoadReportRequest{
		Path:   path,
		Out:    reportOut,
		Report: rep,
	})
	if err != nil {
		t.Fatal(err)
	}
	savedBytes, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(savedBytes), secret) || !strings.Contains(string(savedBytes), `"GET widgets"`) {
		t.Fatalf("unexpected saved JSON: %s", savedBytes)
	}

	// 5. CancelLoad stops an in-flight load test and returns a partial report with Interrupted=true;
	// a second StartLoad while running returns CodeBusy.
	done := make(chan struct{})
	var (
		cancelRep *load.Report
		cancelErr error
	)
	go func() {
		cancelRep, cancelErr = app.StartLoad(workspace.LoadOptions{
			Path:        path,
			Environment: "local",
			Cases:       []string{"hang"},
			VUs:         1,
			DurationMs:  10_000,
			TimeoutMs:   2000,
		})
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight load did not reach server")
	}

	_, busyErr := app.StartLoad(workspace.LoadOptions{
		Path:        path,
		Environment: "local",
		Cases:       []string{"list"},
		VUs:         1,
		Iterations:  1,
	})
	var ve *verr.Error
	if !errors.As(busyErr, &ve) || ve.Code != verr.CodeBusy {
		t.Fatalf("concurrent StartLoad error = %v, want busy", busyErr)
	}

	app.CancelLoad()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CancelLoad did not stop StartLoad")
	}
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if cancelRep == nil || !cancelRep.Interrupted {
		t.Fatalf("cancelled report: %+v", cancelRep)
	}
}
