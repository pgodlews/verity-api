package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestWatchDebounce(t *testing.T) {
	if watchDebounce != 300*time.Millisecond {
		t.Fatalf("watchDebounce = %s, want 300ms", watchDebounce)
	}
	d := watchDebounce
	t0 := time.Unix(1_700_000_000, 0)
	deadline := pushDebounce(time.Time{}, t0, d)
	if !deadline.Equal(t0.Add(d)) {
		t.Fatalf("first deadline %s", deadline)
	}
	later := t0.Add(100 * time.Millisecond)
	pushed := pushDebounce(deadline, later, d)
	if !pushed.Equal(later.Add(d)) {
		t.Fatalf("pushed deadline %s, want %s", pushed, later.Add(d))
	}
	if got := pushDebounce(pushed, t0, d); !got.Equal(pushed) {
		t.Fatalf("earlier event shortened the window to %s", got)
	}
	if debounceFired(time.Time{}, t0) {
		t.Fatal("zero deadline fired")
	}
	if debounceFired(pushed, pushed.Add(-time.Nanosecond)) {
		t.Fatal("fired before the deadline")
	}
	if !debounceFired(pushed, pushed) {
		t.Fatal("deadline itself did not fire")
	}
	if !debounceFired(pushed, pushed.Add(time.Millisecond)) {
		t.Fatal("past the deadline did not fire")
	}
}

func TestEventTriggers(t *testing.T) {
	const file = "/proj/app.yaml"
	files := map[string]struct{}{file: {}}
	tests := []struct {
		name string
		path string
		op   fsnotify.Op
		want bool
	}{
		{name: "write", path: file, op: fsnotify.Write, want: true},
		{name: "create", path: file, op: fsnotify.Create, want: true},
		{name: "rename", path: file, op: fsnotify.Rename, want: true},
		{name: "remove", path: file, op: fsnotify.Remove, want: true},
		{name: "chmod", path: file, op: fsnotify.Chmod, want: true},
		{name: "sibling", path: "/proj/other.yaml", op: fsnotify.Write, want: false},
		{name: "other dir", path: "/tmp/app.yaml", op: fsnotify.Write, want: false},
		{name: "empty op", path: file, op: 0, want: false},
		{name: "empty name", path: "", op: fsnotify.Write, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventTriggers(files, tt.path, tt.op); got != tt.want {
				t.Fatalf("eventTriggers(%q, %v) = %v, want %v", tt.path, tt.op, got, tt.want)
			}
		})
	}
}

func TestWatchDirs(t *testing.T) {
	got := watchDirs([]string{"/proj/a.yaml", "/proj/sub/b.yaml", "/proj/a.yaml"})
	want := []string{"/proj", "/proj/sub"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watchDirs = %v, want %v", got, want)
	}
	if dirs := watchDirs(nil); len(dirs) != 0 {
		t.Fatalf("empty = %v", dirs)
	}
}

func TestClearScreen(t *testing.T) {
	var b bytes.Buffer
	clearScreen(&b)
	if b.String() != "\033[2J\033[H" {
		t.Fatalf("clear = %q", b.String())
	}
}

func TestCountDelta(t *testing.T) {
	tests := []struct {
		name               string
		prev, next         map[string]spi.Status
		newly, fixed, same int
	}{
		{
			name: "same pass",
			prev: statuses("s/c/a", "passed"),
			next: statuses("s/c/a", "passed"),
			same: 1,
		},
		{
			name:  "newly failing",
			prev:  statuses("s/c/a", "passed"),
			next:  statuses("s/c/a", "failed"),
			newly: 1,
		},
		{
			name:  "fixed",
			prev:  statuses("s/c/a", "failed"),
			next:  statuses("s/c/a", "passed"),
			fixed: 1,
		},
		{
			name: "still failing",
			prev: statuses("s/c/a", "failed"),
			next: statuses("s/c/a", "failed"),
			same: 1,
		},
		{
			name: "failed and error are both failing",
			prev: statuses("s/c/a", "failed"),
			next: statuses("s/c/a", "error"),
			same: 1,
		},
		{
			name:  "skipped to failed",
			prev:  statuses("s/c/a", "skipped"),
			next:  statuses("s/c/a", "failed"),
			newly: 1,
		},
		{
			name:  "failed to skipped",
			prev:  statuses("s/c/a", "failed"),
			next:  statuses("s/c/a", "skipped"),
			fixed: 1,
		},
		{
			name: "passed to skipped is not a failure delta",
			prev: statuses("s/c/a", "passed"),
			next: statuses("s/c/a", "skipped"),
		},
		{
			name:  "new failing step",
			prev:  statuses("s/c/a", "passed"),
			next:  statuses("s/c/a", "passed", "s/c/b", "error"),
			newly: 1,
			same:  1,
		},
		{
			name:  "removed failing step",
			prev:  statuses("s/c/a", "error"),
			next:  map[string]spi.Status{},
			fixed: 1,
		},
		{
			name: "added passing step is not counted",
			prev: statuses("s/c/a", "passed"),
			next: statuses("s/c/a", "passed", "s/c/b", "passed"),
			same: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newly, fixed, same := countDelta(tt.prev, tt.next)
			if newly != tt.newly || fixed != tt.fixed || same != tt.same {
				t.Fatalf("got newly=%d fixed=%d unchanged=%d, want %d %d %d",
					newly, fixed, same, tt.newly, tt.fixed, tt.same)
			}
		})
	}
}

func TestFormatDelta(t *testing.T) {
	prevMap := map[string]spi.Status{}
	nextMap := map[string]spi.Status{}
	for i := 0; i < 14; i++ {
		k := fmt.Sprintf("s/c/u%d", i)
		prevMap[k] = spi.StatusPassed
		nextMap[k] = spi.StatusPassed
	}
	prevMap["s/c/fix"] = spi.StatusFailed
	nextMap["s/c/fix"] = spi.StatusPassed
	prevMap["s/c/n1"] = spi.StatusPassed
	nextMap["s/c/n1"] = spi.StatusFailed
	prevMap["s/c/n2"] = spi.StatusSkipped
	nextMap["s/c/n2"] = spi.StatusError
	newly, fixed, same := countDelta(prevMap, nextMap)
	got := fmt.Sprintf("%d newly failing, %d fixed, %d unchanged", newly, fixed, same)
	const want = "2 newly failing, 1 fixed, 14 unchanged"
	if got != want {
		t.Fatalf("got %q", got)
	}

	prev := &spi.RunResult{Suites: []spi.SuiteResult{{
		Name: "s",
		Cases: []spi.CaseResult{{
			Name: "c", Iteration: -1,
			Steps: []spi.StepResult{
				{Name: "a", Status: spi.StatusPassed},
				{Name: "b", Status: spi.StatusFailed},
				{Name: "d", Status: spi.StatusPassed},
			},
		}},
	}}}
	next := &spi.RunResult{Suites: []spi.SuiteResult{{
		Name: "s",
		Cases: []spi.CaseResult{{
			Name: "c", Iteration: -1,
			Steps: []spi.StepResult{
				{Name: "a", Status: spi.StatusFailed},
				{Name: "b", Status: spi.StatusPassed},
				{Name: "d", Status: spi.StatusPassed},
			},
		}},
	}}}
	if got := formatDelta(prev, next); got != "1 newly failing, 1 fixed, 1 unchanged" {
		t.Fatalf("formatDelta = %q", got)
	}
}

func TestStepStatusKeys(t *testing.T) {
	r := &spi.RunResult{Suites: []spi.SuiteResult{{
		Name: "suite",
		Cases: []spi.CaseResult{
			{
				Name: "case", Iteration: -1,
				Steps: []spi.StepResult{
					{Name: "get", Status: spi.StatusPassed},
					{Name: "get", Status: spi.StatusFailed},
				},
			},
			{
				Name: "case", Iteration: 1,
				Steps: []spi.StepResult{{Name: "get", Status: spi.StatusError}},
			},
		},
	}}}
	got := stepStatuses(r)
	want := map[string]spi.Status{
		"suite/case/get":   spi.StatusPassed,
		"suite/case/get#2": spi.StatusFailed,
		"suite/case#1/get": spi.StatusError,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %#v, want %#v", got, want)
	}
}

func TestRunOnceLoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(path, []byte(":\n- ["), 0o644); err != nil {
		t.Fatal(err)
	}
	code, result, files := runOnce(context.Background(), watchConfig{ProjectPath: path})
	if code != exitConfig || result != nil || files != nil {
		t.Fatalf("code=%d result=%v files=%v", code, result, files)
	}
}

func TestRunOnceValidationKeepsSourceFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.yaml")
	body := []byte("schemaVersion: \"1.0\"\nsuites: []\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	code, result, files := runOnce(context.Background(), watchConfig{ProjectPath: path})
	if code != exitConfig || result != nil {
		t.Fatalf("code=%d result=%v", code, result)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != abs {
		t.Fatalf("files = %v, want [%s]", files, abs)
	}
}

func TestWatchRerunsAfterRename(t *testing.T) {
	skipWithoutFSNotify(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	proj := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(proj, []byte(":\n- ["), 0o644); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "out", "report.json")

	ctx, cancel := context.WithCancel(context.Background())
	iter := make(chan int)
	var delta bytes.Buffer
	done := make(chan int, 1)
	var exitOnce sync.Once
	readExit := func() int {
		t.Helper()
		var code int
		exitOnce.Do(func() {
			cancel()
			select {
			case code = <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("watchLoop did not return")
				code = -1
			}
		})
		return code
	}
	defer readExit()

	go func() {
		done <- watchLoop(ctx, watchConfig{
			ProjectPath:     proj,
			JSONOut:         report,
			Quiet:           true,
			FollowRedirects: true,
			Parallel:        1,
			Timeout:         5 * time.Second,
			Bail:            true,
			Vars:            map[string]string{"BASE_URL": srv.URL},
			DeltaOut:        &delta,
			Iter:            iter,
		})
	}()

	if code := recvIter(t, iter, &delta, report); code != exitConfig {
		t.Fatalf("first iteration code = %d, want load error %d", code, exitConfig)
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Fatalf("report exists after load error: %v", err)
	}

	atomicWrite(t, proj, watchProjectYAML(200))
	if code := recvIter(t, iter, &delta, report); code != exitOK {
		t.Fatalf("second iteration code = %d, want pass", code)
	}
	if got := reportSummary(report); got != "passed" {
		t.Fatalf("report status %q, want passed", got)
	}

	atomicWrite(t, proj, watchProjectYAML(201))
	if code := recvIter(t, iter, &delta, report); code != exitTestFailed {
		t.Fatalf("third iteration code = %d, want failure", code)
	}
	const wantDelta = "1 newly failing, 0 fixed, 0 unchanged"
	if !bytes.Contains(delta.Bytes(), []byte(wantDelta)) {
		t.Fatalf("delta %q, want %q", delta.String(), wantDelta)
	}
	if got := reportSummary(report); got != "failed" {
		t.Fatalf("rewritten report status %q, want failed", got)
	}
	if code := readExit(); code != exitTestFailed {
		t.Fatalf("watch exit = %d, want last run %d", code, exitTestFailed)
	}
}

func skipWithoutFSNotify(t *testing.T) {
	t.Helper()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("fsnotify is unavailable: %v", err)
	}
	dir := t.TempDir()
	if err := w.Add(dir); err != nil {
		w.Close()
		t.Skipf("fsnotify is unavailable: %v", err)
	}
	w.Close()
}

func recvIter(t *testing.T, iter <-chan int, delta *bytes.Buffer, report string) int {
	t.Helper()
	select {
	case code := <-iter:
		return code
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for a watch iteration\ndelta: %q\nreport: %s", delta.Bytes(), reportSummary(report))
	}
	return 0
}

func atomicWrite(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func watchProjectYAML(status int) string {
	return fmt.Sprintf(`
schemaVersion: "1.0"
name: watch
environments:
  - name: local
    default: true
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: get
            protocol: rest
            request:
              method: GET
              url: "${BASE_URL}/"
            assertions:
              - type: status
                value: %d
`, status)
}

func reportSummary(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var v struct {
		Summary struct {
			Status string `json:"status"`
		} `json:"summary"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.Summary.Status
}

func statuses(kv ...string) map[string]spi.Status {
	m := make(map[string]spi.Status, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = spi.Status(kv[i+1])
	}
	return m
}

func TestWatchExitCodeOfAbortedRun(t *testing.T) {
	r := &spi.RunResult{Summary: spi.Summary{Status: spi.StatusAborted, Aborted: true}}
	if got := exitCodeOf(r); got != exitInterrupted {
		t.Fatalf("aborted run: exit %d, want %d (same as verity run)", got, exitInterrupted)
	}
	r.Summary.Status = spi.StatusFailed
	if got := exitCodeOf(r); got != exitInterrupted {
		t.Fatalf("aborted run with a failure: exit %d, want %d", got, exitInterrupted)
	}
}

func TestEventTriggersOnMissingDirectory(t *testing.T) {
	files := map[string]struct{}{"/proj/data/rows.csv": {}}
	for _, tt := range []struct {
		path string
		op   fsnotify.Op
		want bool
	}{
		{"/proj/data", fsnotify.Create, true},
		{"/proj/data", fsnotify.Write, false},
		{"/proj/datax", fsnotify.Create, false},
		{"/proj/data/rows.csv", fsnotify.Create, true},
	} {
		if got := eventTriggers(files, tt.path, tt.op); got != tt.want {
			t.Errorf("eventTriggers(%q, %v) = %v, want %v", tt.path, tt.op, got, tt.want)
		}
	}
}

func TestExistingAncestor(t *testing.T) {
	dir := t.TempDir()
	if got := existingAncestor(filepath.Join(dir, "a", "b")); got != dir {
		t.Fatalf("existingAncestor = %q, want %q", got, dir)
	}
	if got := existingAncestor(dir); got != dir {
		t.Fatalf("existing dir: got %q", got)
	}
}
