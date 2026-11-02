package main

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/runner"
)

func TestCommonRunTimeout(t *testing.T) {
	for _, command := range []string{"run", "load"} {
		t.Run(command, func(t *testing.T) {
			for _, timeout := range []string{"--timeout=-1s", "--timeout=invalid"} {
				args := []string{command, "missing-project.yaml", timeout}
				if command == "load" {
					args = append(args, "--iterations=1")
				}
				if code := run(args); code != exitUsage {
					t.Fatalf("%s %s: exit %d, want %d", command, timeout, code, exitUsage)
				}
			}
		})
	}
	for _, raw := range []string{"0", "1ms", "30s"} {
		fs := flag.NewFlagSet("common", flag.ContinueOnError)
		common := bindCommonRunFlags(fs)
		if common.timeout != 30*time.Second {
			t.Fatalf("default timeout = %v, want 30s", common.timeout)
		}
		if err := fs.Parse([]string{"--timeout=" + raw}); err != nil {
			t.Fatal(err)
		}
		want, _ := time.ParseDuration(raw)
		if common.timeout != want {
			t.Fatalf("timeout = %v, want %v", common.timeout, want)
		}
	}
}

func TestWatchInterruptCancelsThenForceExits(t *testing.T) {
	t.Run("second signal exits", func(t *testing.T) {
		sigs := make(chan os.Signal, 2)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		exited := make(chan int, 1)
		go watchInterrupt(sigs, cancel, done, func(code int) { exited <- code })
		sigs <- syscall.SIGINT
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("first signal did not cancel")
		}
		sigs <- syscall.SIGTERM
		select {
		case code := <-exited:
			if code != exitInterrupted {
				t.Fatalf("exit %d, want %d", code, exitInterrupted)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("second signal did not exit")
		}
	})

	t.Run("finish before a second signal does not exit", func(t *testing.T) {
		sigs := make(chan os.Signal, 2)
		_, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		exited := make(chan int, 1)
		go watchInterrupt(sigs, cancel, done, func(code int) { exited <- code })
		sigs <- syscall.SIGINT
		close(done)
		select {
		case code := <-exited:
			t.Fatalf("exited %d after the command finished", code)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

func TestFinishRunWritesPartialReportsWhenCancelled(t *testing.T) {
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

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "partial",
		Environments:  []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{
				{Name: "slow", Steps: []model.Step{{
					Name: "block", Protocol: "rest",
					Request: map[string]any{"url": srv.URL + "/block"},
				}}},
				{Name: "later", Steps: []model.Step{{
					Name: "next", Protocol: "rest",
					Request: map[string]any{"url": srv.URL + "/next"},
				}}},
			},
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(5 * time.Second):
		}
	}()

	dir := t.TempDir()
	junitPath := filepath.Join(dir, "junit.xml")
	jsonPath := filepath.Join(dir, "report.json")
	code := finishRun(ctx, bootstrap.Default(), project, runner.Options{Timeout: 5 * time.Second}, false, true, junitPath, jsonPath, "")
	if code != exitInterrupted {
		t.Fatalf("exit %d, want %d", code, exitInterrupted)
	}
	junit, err := os.ReadFile(junitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(junit), `message="aborted"`) || !strings.Contains(string(junit), "<skipped") {
		t.Fatalf("junit missing aborted skip:\n%s", junit)
	}
	js, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `"aborted": true`) || !strings.Contains(string(js), `"status": "aborted"`) {
		t.Fatalf("json report missing aborted status:\n%s", js)
	}
}
