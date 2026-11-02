package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// newTestServer returns an httptest server that echoes JSON and checks auth.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "verity")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"id":     42,
			"name":   "widget",
			"nested": map[string]any{"active": true},
		})
	})
	mux.HandleFunc("/needauth", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-bearer-token-123456" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(200)
		w.Write([]byte(`<?xml version="1.0"?><Resp><Status>OK</Status></Resp>`))
	})
	return httptest.NewServer(mux)
}

func TestRunRESTWithAssertions(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "test",
		Environments: []model.Environment{{
			Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL},
		}},
		Suites: []model.Suite{{
			Name: "s1",
			Cases: []model.TestCase{{
				Name: "get json",
				Steps: []model.Step{{
					Name:     "fetch",
					Protocol: "rest",
					Request: map[string]any{
						"method": "GET",
						"url":    "${BASE_URL}/json",
					},
					Assertions: []model.Assertion{
						{Type: "status", Value: 200},
						{Type: "jsonpath", Expr: "$.name", Value: "widget"},
						{Type: "jsonpath", Expr: "$.id", Op: "eq", Value: 42},
						{Type: "jsonpath", Expr: "$.nested.active", Op: "eq", Value: true},
						{Type: "header", Expr: "X-Custom", Value: "verity"},
						{Type: "response_time", Op: "lt", Value: 10000},
					},
				}},
			}},
		}},
	}

	reg := bootstrap.Default()
	r := New(reg)
	res, err := r.Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("expected passed, got %s (failed=%d errors=%d)",
			res.Summary.Status, res.Summary.Failed, res.Summary.Errors)
	}
	if res.Summary.Passed != 6 {
		t.Fatalf("expected 6 passing assertions, got %d", res.Summary.Passed)
	}
}

func TestRunFailingAssertion(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0", Name: "test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Suites: []model.Suite{{Name: "s1", Cases: []model.TestCase{{
			Name: "c1", Steps: []model.Step{{
				Name: "fetch", Protocol: "rest",
				Request: map[string]any{"url": "${BASE_URL}/json"},
				Assertions: []model.Assertion{
					{Type: "status", Value: 500}, // wrong on purpose
				},
			}},
		}}}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusFailed {
		t.Fatalf("expected failed status, got %s", res.Summary.Status)
	}
}

func TestRunWithBearerAuthAndTransfer(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	tf := false
	project := &model.Project{
		SchemaVersion: "1.0", Name: "test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Auth: []model.AuthDef{{
			Name: "api", Type: "bearer",
			Config: map[string]any{"token": "test-bearer-token-123456"},
		}},
		Suites: []model.Suite{{
			Name: "s1",
			Auth: &model.AuthRef{Use: []string{"api"}},
			Cases: []model.TestCase{{
				Name: "c1", ContinueOnError: &tf,
				Steps: []model.Step{{
					Name: "authed", Protocol: "rest",
					Request: map[string]any{"url": "${BASE_URL}/needauth"},
					Assertions: []model.Assertion{
						{Type: "status", Value: 200},
						{Type: "jsonpath", Expr: "$.ok", Op: "eq", Value: true},
					},
				}},
			}},
		}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("expected passed (auth applied), got %s", res.Summary.Status)
	}
}

// TestVarPrecedence exercises every adjacent link in the documented chain:
// env < suite < case < feed row < CLI --var. Each step echoes the variable's
// value back in the response body via query string so we can assert on it.
func TestVarPrecedence(t *testing.T) {
	var gotVal string
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		gotVal = r.URL.Query().Get("v")
		w.WriteHeader(200)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	newProject := func(caseVars, suiteVars, feedRow map[string]string, extraVars map[string]string) (*model.Project, Options) {
		var feed *model.Feed
		if feedRow != nil {
			feed = &model.Feed{Type: "static-row"}
		}
		return &model.Project{
			SchemaVersion: "1.0", Name: "test",
			Environments: []model.Environment{{Name: "default", Default: true,
				Variables: map[string]string{"BASE_URL": srv.URL, "V": "env"}}},
			Suites: []model.Suite{{
				Name: "s1", Vars: suiteVars,
				Cases: []model.TestCase{{
					Name: "c1", Vars: caseVars, Feed: feed,
					Steps: []model.Step{{
						Name: "echo", Protocol: "rest",
						Request: map[string]any{"url": "${BASE_URL}/echo?v=${V}"},
					}},
				}},
			}},
		}, Options{ExtraVars: extraVars}
	}

	run := func(t *testing.T, caseVars, suiteVars, feedRow, extraVars map[string]string) string {
		t.Helper()
		project, opts := newProject(caseVars, suiteVars, feedRow, extraVars)
		reg := bootstrap.Default()
		if feedRow != nil {
			reg.RegisterDataSource(staticRowSource{row: feedRow})
		}
		gotVal = ""
		if _, err := New(reg).Run(context.Background(), project, opts); err != nil {
			t.Fatal(err)
		}
		return gotVal
	}

	t.Run("env is the default", func(t *testing.T) {
		if got := run(t, nil, nil, nil, nil); got != "env" {
			t.Fatalf("got %q, want %q", got, "env")
		}
	})
	t.Run("suite overrides env", func(t *testing.T) {
		if got := run(t, nil, map[string]string{"V": "suite"}, nil, nil); got != "suite" {
			t.Fatalf("got %q, want %q", got, "suite")
		}
	})
	t.Run("case overrides suite", func(t *testing.T) {
		if got := run(t, map[string]string{"V": "case"}, map[string]string{"V": "suite"}, nil, nil); got != "case" {
			t.Fatalf("got %q, want %q", got, "case")
		}
	})
	t.Run("feed row overrides case", func(t *testing.T) {
		if got := run(t, map[string]string{"V": "case"}, nil, map[string]string{"V": "row"}, nil); got != "row" {
			t.Fatalf("got %q, want %q", got, "row")
		}
	})
	t.Run("CLI --var overrides feed row", func(t *testing.T) {
		if got := run(t, nil, nil, map[string]string{"V": "row"}, map[string]string{"V": "cli"}); got != "cli" {
			t.Fatalf("got %q, want %q", got, "cli")
		}
	})
}

// staticRowSource is a test-only DataSource that always yields one fixed row.
type staticRowSource struct{ row map[string]string }

func (staticRowSource) Type() string { return "static-row" }
func (s staticRowSource) Rows(_ string, _ map[string]any, _ []map[string]string, _ string) ([]map[string]string, error) {
	return []map[string]string{s.row}, nil
}

func TestRunUnknownFeedTypeFailsCase(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0", Name: "test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Suites: []model.Suite{{Name: "s1", Cases: []model.TestCase{{
			Name: "c1",
			Feed: &model.Feed{Type: "nope", Inline: []map[string]string{{"x": "1"}}},
			Steps: []model.Step{{
				Name: "fetch", Protocol: "rest",
				Request:    map[string]any{"url": "${BASE_URL}/json"},
				Assertions: []model.Assertion{{Type: "status", Value: 200}},
			}},
		}}}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusError {
		t.Fatalf("expected error status for unknown feed type, got %s", res.Summary.Status)
	}
	if len(res.Suites) != 1 || len(res.Suites[0].Cases) != 1 {
		t.Fatalf("expected exactly one (failed) case, got %+v", res.Suites)
	}
	c := res.Suites[0].Cases[0]
	if c.Status != spi.StatusError {
		t.Fatalf("expected case status error, got %s", c.Status)
	}
	if len(c.Steps) != 1 || c.Steps[0].Name != "feed" || c.Steps[0].Status != spi.StatusError {
		t.Fatalf("expected a single 'feed' error step, got %+v", c.Steps)
	}
}

// TestRedactionInReporters is the required direct redaction test: a secret used
// in auth must never appear in any reporter's output.
func TestRedactionInReporters(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	const secret = "test-bearer-token-123456"
	project := &model.Project{
		SchemaVersion: "1.0", Name: "test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Auth: []model.AuthDef{{
			Name: "api", Type: "bearer",
			Config: map[string]any{"token": secret},
		}},
		Suites: []model.Suite{{
			Name: "s1", Auth: &model.AuthRef{Use: []string{"api"}},
			Cases: []model.TestCase{{Name: "c1", Steps: []model.Step{{
				Name: "authed", Protocol: "rest",
				Request:    map[string]any{"url": "${BASE_URL}/needauth"},
				Assertions: []model.Assertion{{Type: "status", Value: 200}},
			}}}},
		}},
	}
	reg := bootstrap.Default()
	res, err := New(reg).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Run every reporter to a buffer and assert the secret never appears.
	for _, repType := range []string{"console", "json", "junit", "html"} {
		rep, err := reg.Reporter(repType)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := reportTo(rep, res, &buf); err != nil {
			t.Fatalf("reporter %s: %v", repType, err)
		}
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("SECRET LEAKED in %s reporter output:\n%s", repType, buf.String())
		}
		if !strings.Contains(buf.String(), "s1") {
			t.Fatalf("reporter %s produced no recognizable output", repType)
		}
	}
}

func TestSummaryCountingUnits(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// 1 step with 1 passing and 2 failing assertions
	project := &model.Project{
		SchemaVersion: "1.0", Name: "summary-test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Suites: []model.Suite{{
			Name: "s1",
			Cases: []model.TestCase{{
				Name: "c1",
				Steps: []model.Step{{
					Name: "step1", Protocol: "rest",
					Request: map[string]any{"url": "${BASE_URL}/json"},
					Assertions: []model.Assertion{
						{Type: "status", Value: 200}, // passes
						{Type: "status", Value: 404}, // fails
						{Type: "status", Value: 500}, // fails
					},
				}},
			}},
		}},
	}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}

	s := res.Summary
	if s.Steps != 1 {
		t.Fatalf("expected Steps=1, got %d", s.Steps)
	}
	if s.StepsFailed != 1 {
		t.Fatalf("expected StepsFailed=1, got %d", s.StepsFailed)
	}
	if s.Assertions != 3 {
		t.Fatalf("expected Assertions=3, got %d", s.Assertions)
	}
	if s.Passed != 1 {
		t.Fatalf("expected Passed=1, got %d", s.Passed)
	}
	if s.Failed != 2 {
		t.Fatalf("expected Failed=2 (assertions only, not 1+2=3), got %d", s.Failed)
	}
}

func TestCaptureMultiValueHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("X-Multi", "val1")
		w.Header().Add("X-Multi", "val2")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0", Name: "multi-header-test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Suites: []model.Suite{{
			Name: "s1",
			Cases: []model.TestCase{{
				Name: "c1",
				Steps: []model.Step{{
					Name: "step1", Protocol: "rest",
					Request:    map[string]any{"url": "${BASE_URL}/"},
					Assertions: []model.Assertion{{Type: "status", Value: 200}},
				}},
			}},
		}},
	}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}

	stepRes := res.Suites[0].Cases[0].Steps[0]
	if stepRes.Response == nil {
		t.Fatal("expected response capture")
	}
	multi := stepRes.Response.Headers["X-Multi"]
	if multi != "val1, val2" {
		t.Fatalf("expected multi-value header joined as 'val1, val2', got %q", multi)
	}
}

func TestRunnerDoesNotMutateAssertionConfig(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	origConfig := map[string]any{"customKey": "customVal"}
	project := &model.Project{
		SchemaVersion: "1.0", Name: "mutation-test",
		Environments: []model.Environment{{Name: "default", Default: true,
			Variables: map[string]string{"BASE_URL": srv.URL}}},
		Suites: []model.Suite{{
			Name: "s1",
			Cases: []model.TestCase{{
				Name: "c1",
				Steps: []model.Step{{
					Name: "step1", Protocol: "rest",
					Request: map[string]any{"url": "${BASE_URL}/json"},
					Assertions: []model.Assertion{
						{Type: "status", Value: 200, Config: origConfig},
					},
				}},
			}},
		}},
	}

	_, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if _, mutated := origConfig["__dir"]; mutated {
		t.Fatal("origConfig was mutated in-place with __dir")
	}
}

func TestRunnerSharesOAuthTokenAcrossCasesAndFeeds(t *testing.T) {
	var tokenHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		tokenHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "shared-oauth-token-xyz",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer shared-oauth-token-xyz" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "oauth-sharing-test",
		Environments: []model.Environment{{
			Name:    "default",
			Default: true,
			Variables: map[string]string{
				"BASE_URL":  srv.URL,
				"TOKEN_URL": srv.URL + "/oauth/token",
			},
		}},
		Auth: []model.AuthDef{{
			Name: "oauth",
			Type: "oauth2_client_credentials",
			Config: map[string]any{
				"tokenUrl":     "${TOKEN_URL}",
				"clientId":     "my-client",
				"clientSecret": "my-secret",
			},
		}},
		Suites: []model.Suite{{
			Name: "s1",
			Auth: &model.AuthRef{Use: []string{"oauth"}},
			Cases: []model.TestCase{
				{
					Name: "case 1",
					Steps: []model.Step{{
						Name: "step1", Protocol: "rest",
						Request:    map[string]any{"url": "${BASE_URL}/api/data"},
						Assertions: []model.Assertion{{Type: "status", Value: 200}},
					}},
				},
				{
					Name: "case 2",
					Steps: []model.Step{{
						Name: "step2", Protocol: "rest",
						Request:    map[string]any{"url": "${BASE_URL}/api/data"},
						Assertions: []model.Assertion{{Type: "status", Value: 200}},
					}},
				},
				{
					Name: "case 3 feed",
					Feed: &model.Feed{
						Type: "json",
						Inline: []map[string]string{
							{"item": "1"},
							{"item": "2"},
							{"item": "3"},
						},
					},
					Steps: []model.Step{{
						Name: "step3", Protocol: "rest",
						Request:    map[string]any{"url": "${BASE_URL}/api/data"},
						Assertions: []model.Assertion{{Type: "status", Value: 200}},
					}},
				},
			},
		}},
	}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{
		Parallel: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("expected all cases to pass, got %s", res.Summary.Status)
	}

	hits := tokenHits.Load()
	if hits != 1 {
		t.Fatalf("expected OAuth token endpoint to be hit exactly once, got %d hits", hits)
	}
}

func TestRunnerEnvironmentAuthInheritance(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/secure", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer env-token-12345" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "env-auth-test",
		Environments: []model.Environment{{
			Name:    "default",
			Default: true,
			Variables: map[string]string{
				"BASE_URL": srv.URL,
			},
			Auth: &model.AuthRef{Use: []string{"bearer-auth"}},
		}},
		Auth: []model.AuthDef{{
			Name: "bearer-auth",
			Type: "bearer",
			Config: map[string]any{
				"token": "env-token-12345",
			},
		}},
		Suites: []model.Suite{{
			Name: "s1",
			Cases: []model.TestCase{{
				Name: "c1",
				Steps: []model.Step{{
					Name:     "step1",
					Protocol: "rest",
					Request:  map[string]any{"url": "${BASE_URL}/secure"},
					Assertions: []model.Assertion{
						{Type: "status", Value: 200},
					},
				}},
			}},
		}},
	}

	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("expected step inheriting env auth to pass, got %s", res.Summary.Status)
	}
}

func TestRunMultipartForm(t *testing.T) {
	dir := t.TempDir()
	const sentinel = "RAW-FILE-BYTES"
	const token = "upload-token-value-123456"
	if err := os.WriteFile(filepath.Join(dir, "pic.bin"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	projectYAML := []byte(`
schemaVersion: "1.0"
name: multipart
environments:
  - name: default
    default: true
    variables:
      NOTE: hello-note
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: upload
            protocol: rest
            request:
              method: POST
              url: "${BASE_URL}/form"
              multipart:
                - name: note
                  value: "${NOTE}"
                - name: token
                  value: "${env:UPLOAD_TOKEN}"
                - name: file
                  file: pic.bin
                  filename: "my file.txt"
                  contentType: application/octet-stream
            assertions:
              - type: status
                value: 204
`)
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), projectYAML, 0o644); err != nil {
		t.Fatal(err)
	}

	type gotPart struct {
		name, filename, body string
	}
	var parts []gotPart
	var serverErr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || params["boundary"] == "" {
			serverErr = "content-type"
			http.Error(w, "content-type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				serverErr = "parse"
				http.Error(w, "parse", http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(p)
			parts = append(parts, gotPart{name: p.FormName(), filename: p.FileName(), body: string(b)})
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	t.Setenv("UPLOAD_TOKEN", token)
	project, err := model.Load(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{
		ExtraVars: map[string]string{"BASE_URL": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	step := res.Suites[0].Cases[0].Steps[0]
	if serverErr != "" || step.Status != spi.StatusPassed {
		t.Fatalf("status %s server %q error %+v", step.Status, serverErr, step.Error)
	}
	if len(parts) != 3 || parts[0].name != "note" || parts[1].name != "token" || parts[2].name != "file" {
		t.Fatalf("part order %+v", parts)
	}
	if parts[0].body != "hello-note" || parts[1].body != token || parts[2].body != sentinel || parts[2].filename != "my file.txt" {
		t.Fatal("server did not receive the interpolated multipart body")
	}
	captured := step.Request.Body
	if strings.Contains(captured, sentinel) || strings.Contains(captured, token) {
		t.Fatal("captured request contains file bytes or the upload token")
	}
	// Text values never reach captures, only their names and sizes.
	if strings.Contains(captured, "hello-note") || !strings.Contains(captured, "name=note (10 bytes)") ||
		!strings.Contains(captured, "my file.txt") || !strings.Contains(captured, fmt.Sprintf("size=%d", len(sentinel))) {
		t.Fatalf("capture %q", captured)
	}
	if !strings.Contains(step.Request.Headers["Content-Type"], "multipart/form-data") ||
		!strings.Contains(step.Request.Headers["Content-Type"], "boundary=") {
		t.Fatalf("captured content-type %q", step.Request.Headers["Content-Type"])
	}
}

func TestRunMultipartMissingFile(t *testing.T) {
	dir := t.TempDir()
	project := &model.Project{
		SchemaVersion: "1.0",
		Name:          "missing",
		SourceDir:     dir,
		Environments:  []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
			Name: "up", Protocol: "rest",
			Request: map[string]any{
				"method": "POST",
				"url":    "/up",
				"multipart": []any{
					map[string]any{"name": "doc", "file": "missing.bin"},
				},
			},
		}}}}}},
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{})
	if err != nil {
		t.Fatal(err)
	}
	step := res.Suites[0].Cases[0].Steps[0]
	if step.Status != spi.StatusError || step.Error == nil || step.Error.Code != string(verr.CodeConfig) {
		t.Fatalf("status %s error %+v", step.Status, step.Error)
	}
	if !strings.Contains(step.Error.Message, "missing.bin") || !strings.Contains(step.Error.Message, "not found") {
		t.Fatalf("message %q", step.Error.Message)
	}
	if strings.Contains(step.Error.Message, dir) {
		t.Fatalf("error leaked the project directory: %s", step.Error.Message)
	}
}
