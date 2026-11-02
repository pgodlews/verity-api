package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/load"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/zalando/go-keyring"
)

func TestLoadUsage(t *testing.T) {
	if code := run([]string{"load", "--vus", "1001", "--iterations", "1"}); code != exitUsage {
		t.Fatalf("vus cap exit %d", code)
	}
	if code := run([]string{"load", "--vus", "0", "--iterations", "1"}); code != exitUsage {
		t.Fatalf("vus 0 exit %d", code)
	}
	if code := run([]string{"load", "project.yaml"}); code != exitUsage {
		t.Fatalf("missing mode exit %d", code)
	}
	if code := run([]string{"load", "--duration", "1s", "--iterations", "1", "project.yaml"}); code != exitUsage {
		t.Fatalf("both modes exit %d", code)
	}
	if code := run([]string{"load", "--iterations", "1", "--threshold", "nope", "project.yaml"}); code != exitUsage {
		t.Fatalf("bad threshold exit %d", code)
	}
	if code := run([]string{"load", "--iterations", "1", "--max-error-rate", "nope", "project.yaml"}); code != exitUsage {
		t.Fatalf("bad error rate exit %d", code)
	}
	if code := run([]string{"load", "--iterations", "1", "--max-error-rate", "200%", "project.yaml"}); code != exitUsage {
		t.Fatalf("error rate above 100%% exit %d", code)
	}
	if code := run([]string{"load", "--iterations", "1", "--var", "noequals", "project.yaml"}); code != exitUsage {
		t.Fatalf("bad --var exit %d", code)
	}
	if code := run([]string{"load", "--iterations", "1", "--timeout", "notaduration", "project.yaml"}); code != exitUsage {
		t.Fatalf("bad --timeout exit %d", code)
	}
	if code := run([]string{"load", "--iterations", "1", "--timeout", "-1s", "project.yaml"}); code != exitUsage {
		t.Fatalf("negative --timeout exit %d", code)
	}
}

func TestLoadEnvFileVarsAndTimeout(t *testing.T) {
	var (
		mu      sync.Mutex
		gotAuth string
		gotPath string
		gotTag  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(150 * time.Millisecond)
		}
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotTag = r.Header.Get("X-Tag")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	proj := `schemaVersion: "1.0"
name: load-flags
environments:
  - name: local
    default: true
    variables:
      endpoint: /from-env
      tag: env-tag
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: get
            protocol: rest
            request:
              method: GET
              url: ` + quoteYAML(srv.URL+"${endpoint}") + `
              headers:
                Authorization: "Bearer ${env:LOAD_TEST_TOKEN}-${env:LOAD_TEST_SECOND}"
                X-Tag: "${tag}"
            assertions:
              - type: status
                value: 200
`
	if err := os.WriteFile(path, []byte(proj), 0o644); err != nil {
		t.Fatal(err)
	}

	env1 := filepath.Join(dir, "first.env")
	env2 := filepath.Join(dir, "second.env")
	if err := os.WriteFile(env1, []byte("LOAD_TEST_TOKEN=from-first\nLOAD_TEST_SECOND=first-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env2, []byte("LOAD_TEST_SECOND=from-second\nLOAD_TEST_TOKEN=overridden-by-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Process env wins unless --env-file-override is set; later --env-file wins over earlier;
	// repeatable --var overrides environment variables and later --var wins.
	t.Setenv("LOAD_TEST_TOKEN", "from-process")
	t.Setenv("LOAD_TEST_SECOND", "")
	if err := os.Unsetenv("LOAD_TEST_SECOND"); err != nil {
		t.Fatal(err)
	}
	var code int
	_ = withStderr(t, func() {
		code = run([]string{
			"load", path, "--iterations", "1",
			"--env-file", env1, "--env-file", env2,
			"--var", "endpoint=/first", "--var", "endpoint=/custom",
			"--var", "tag=cli-tag",
		})
	})
	if code != exitOK {
		t.Fatalf("load exit %d", code)
	}
	mu.Lock()
	auth1, path1, tag1 := gotAuth, gotPath, gotTag
	mu.Unlock()
	if auth1 != "Bearer from-process-from-second" {
		t.Fatalf("Authorization = %q, want process env + second file", auth1)
	}
	if path1 != "/custom" || tag1 != "cli-tag" {
		t.Fatalf("path=%q tag=%q, want /custom and cli-tag", path1, tag1)
	}

	// With --env-file-override, the dotenv file replaces the existing process variable.
	t.Setenv("LOAD_TEST_TOKEN", "from-process")
	_ = withStderr(t, func() {
		code = run([]string{
			"load", path, "--iterations", "1",
			"--env-file", env1, "--env-file", env2, "--env-file-override",
		})
	})
	if code != exitOK {
		t.Fatalf("override exit %d", code)
	}
	mu.Lock()
	auth2 := gotAuth
	mu.Unlock()
	if auth2 != "Bearer overridden-by-file-from-second" {
		t.Fatalf("Authorization with override = %q", auth2)
	}

	// A malformed --env-file fails with exitConfig (3) before running and never prints the value.
	badEnv := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(badEnv, []byte("NOT_AN_ASSIGNMENT_SECRET_VALUE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := withStderr(t, func() {
		code = run([]string{"load", path, "--iterations", "1", "--env-file", badEnv})
	})
	if code != exitConfig {
		t.Fatalf("malformed env-file exit %d, want %d", code, exitConfig)
	}
	if strings.Contains(stderr, "NOT_AN_ASSIGNMENT_SECRET_VALUE") {
		t.Fatalf("malformed env-file leaked line contents: %s", stderr)
	}

	// --timeout applies per attempt when a step has no timeoutMs of its own.
	jsonOut := filepath.Join(dir, "timeout-report.json")
	_ = withStderr(t, func() {
		code = run([]string{
			"load", path, "--iterations", "1",
			"--var", "endpoint=/slow",
			"--timeout", "30ms",
			"--max-error-rate", "0%",
			"--json", jsonOut,
		})
	})
	if code != exitTestFailed {
		t.Fatalf("short --timeout exit %d, want %d", code, exitTestFailed)
	}
	repBytes, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(repBytes), `"transportErrors": 1`) {
		t.Fatalf("expected transport error from --timeout, got %s", repBytes)
	}
}

func TestLoadRefusesRemoteTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	body := []byte(`schemaVersion: "1.0"
name: remote
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
              url: "http://203.0.113.10/nope"
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := withStderr(t, func() {
		code = run([]string{"load", path, "--iterations", "1"})
	})
	if code != exitConfig {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "203.0.113.10") || !strings.Contains(stderr, "allow-remote") {
		t.Fatalf("stderr %s", stderr)
	}
}

func TestLoadPrintsTargetsBeforeStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	body := []byte("schemaVersion: \"1.0\"\nname: load\nenvironments:\n  - name: local\n    default: true\nsuites:\n  - name: s\n    cases:\n      - name: c\n        steps:\n          - name: get\n            protocol: rest\n            request:\n              method: GET\n              url: " + quoteYAML(srv.URL) + "\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	got := withStderr(t, func() {
		code = run([]string{"load", path, "--iterations", "1", "--vus", "2", "--allow-remote"})
	})
	if code != exitOK {
		t.Fatalf("exit %d, stderr %s", code, got)
	}
	if !strings.Contains(got, "vus=2") || !strings.Contains(got, "iterations=1") || !strings.Contains(got, "allow-remote") {
		t.Fatalf("stderr %s", got)
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, u.Hostname()) {
		t.Fatalf("stderr %s, want host %s", got, u.Hostname())
	}
}

func TestLoadThresholdExitAndPartialJSON(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			once.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		time.Sleep(60 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	reg := bootstrap.Default()

	slow := oneCLIProject(getCLIStep("slow", srv.URL+"/slow"))
	var console bytes.Buffer
	code := executeLoad(context.Background(), reg, slow, load.Options{
		VUs: 1, Iterations: 1, FollowRedirects: true,
		Thresholds: []string{"p95<10ms"},
	}, "", &console)
	if code != exitTestFailed {
		t.Fatalf("threshold exit %d", code)
	}
	if !strings.Contains(console.String(), "FAIL") {
		t.Fatalf("console %s", console.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-time.After(3 * time.Second):
			cancel()
		}
	}()
	jsonPath := filepath.Join(t.TempDir(), "load.json")
	start := time.Now()
	code = executeLoad(ctx, reg, oneCLIProject(getCLIStep("block", srv.URL+"/block")), load.Options{
		VUs: 1, Duration: 30 * time.Second, FollowRedirects: true, Timeout: 2 * time.Second,
	}, jsonPath, io.Discard)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	if code != exitInterrupted {
		t.Fatalf("interrupt exit %d", code)
	}
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"interrupted": true`) {
		t.Fatalf("json %s", b)
	}
}

func TestLoadVUWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(path, []byte(projectYAML(srv.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := withStderr(t, func() {
		code = run([]string{"load", path, "--iterations", "1", "--vus", "201"})
	})
	if code != exitOK {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "201") {
		t.Fatalf("stderr %s", stderr)
	}
}

// withStderr runs fn while os.Stderr is a pipe and returns what fn wrote.
func withStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	<-done
	return buf.String()
}

func projectYAML(url string) string {
	return "schemaVersion: \"1.0\"\nname: load\nenvironments:\n  - name: local\n    default: true\nsuites:\n  - name: s\n    cases:\n      - name: c\n        steps:\n          - name: get\n            protocol: rest\n            request:\n              method: GET\n              url: " + quoteYAML(url) + "\n            assertions:\n              - type: status\n                value: 200\n"
}

func quoteYAML(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "") + "\""
}

func oneCLIProject(step model.Step) *model.Project {
	return &model.Project{
		SchemaVersion: "1.0",
		Name:          "load",
		Environments:  []model.Environment{{Name: "local", Default: true}},
		Suites:        []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{step}}}}},
	}
}

func getCLIStep(name, url string) model.Step {
	return model.Step{
		Name: name, Protocol: "rest",
		Request:    map[string]any{"method": "GET", "url": url},
		Assertions: []model.Assertion{{Type: "status", Value: 200}},
	}
}

func TestLoadCommandRedactsSecretTargets(t *testing.T) {
	keyring.MockInit()
	for _, source := range []string{"env", "keychain"} {
		for _, mode := range []string{"invalid", "refused", "list"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				value, suffix := "bad host-sekret", "/x"
				wantCode := exitConfig
				if mode == "refused" {
					value = "203.0.113.10"
				}
				if mode == "list" {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
					defer server.Close()
					u, _ := url.Parse(server.URL)
					value, suffix, wantCode = u.Hostname(), ":"+u.Port()+"/x", exitOK
				}
				t.Setenv("LOAD_TARGET_SECRET", value)
				ref := "${env:LOAD_TARGET_SECRET}"
				if source == "keychain" {
					if err := (&secrets.Keychain{}).Set("project/LOAD_TARGET_SECRET", value); err != nil {
						t.Fatal(err)
					}
					ref = "${secret:keychain:project/LOAD_TARGET_SECRET}"
				}
				projectPath := filepath.Join(t.TempDir(), "project.yaml")
				project := `schemaVersion: "1.0"
name: target-secrets
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: get
            protocol: rest
            request:
              url: "http://` + ref + suffix + `"
`
				if err := os.WriteFile(projectPath, []byte(project), 0600); err != nil {
					t.Fatal(err)
				}
				var code int
				output := withStderr(t, func() { code = cmdLoad([]string{projectPath, "--iterations", "1"}) })
				if code != wantCode {
					t.Fatalf("exit %d, want %d: %s", code, wantCode, output)
				}
				if strings.Contains(output, value) || !strings.Contains(output, secrets.Mask) {
					t.Fatalf("unsafe load output: %s", output)
				}
			})
		}
	}
}
