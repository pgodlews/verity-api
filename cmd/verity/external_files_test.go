package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// An include outside the project directory needs --allow-external-files on
// every command that loads the project.
func TestIncludeOutsideProjectNeedsTheFlag(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project", "project.yaml")
	writeCommandFile(t, project, "schemaVersion: \"1.0\"\nname: ext\ninclude: [../shared/suites.yaml]\nenvironments:\n  - name: local\n    default: true\nsuites: []\n")
	writeCommandFile(t, filepath.Join(base, "shared", "suites.yaml"), "suites:\n  - name: s\n    cases:\n      - name: c\n        steps:\n          - name: get\n            protocol: rest\n            request: { url: \"http://127.0.0.1:1/\" }\n")

	for _, args := range [][]string{
		{"validate", project},
		{"fmt", "--check", project},
		{"graphql", "schema", project, "--step", "get"},
	} {
		code, _, stderr := captureCommand(t, "", func() int { return run(args) })
		if code != exitConfig || !strings.Contains(stderr, "outside the project directory") {
			t.Fatalf("%v without the flag: exit %d, stderr %q", args, code, stderr)
		}
	}
	for _, args := range [][]string{
		{"validate", project, "--allow-external-files"},
		{"fmt", "--check", "--allow-external-files", project}, // exit 1: needs formatting
	} {
		code, _, stderr := captureCommand(t, "", func() int { return run(args) })
		if code == exitConfig || code == exitUsage {
			t.Fatalf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

// verity load accepts --allow-external-files, so ${secret:file:...} outside
// the project works there too.
func TestLoadReadsExternalSecretFileOnlyWithTheFlag(t *testing.T) {
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("X-Key"))
	}))
	defer srv.Close()
	base := t.TempDir()
	secret := filepath.Join(base, "outside", "key.txt")
	writeCommandFile(t, secret, "load-secret-value-123")
	project := filepath.Join(base, "project", "project.yaml")
	writeCommandFile(t, project, "schemaVersion: \"1.0\"\nname: ext\nenvironments:\n  - name: local\n    default: true\nsuites:\n  - name: s\n    cases:\n      - name: c\n        steps:\n          - name: get\n            protocol: rest\n            request:\n              url: \""+srv.URL+"\"\n              headers: { X-Key: \"${secret:file:"+filepath.ToSlash(secret)+"}\" }\n")

	captureCommand(t, "", func() int {
		return run([]string{"load", project, "--iterations", "1", "--vus", "1"})
	})
	if got, _ := auth.Load().(string); got == "load-secret-value-123" {
		t.Fatal("the secret was sent")
	}
	code, _, stderr := captureCommand(t, "", func() int {
		return run([]string{"load", project, "--iterations", "1", "--vus", "1", "--allow-external-files"})
	})
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got, _ := auth.Load().(string); got != "load-secret-value-123" {
		t.Fatalf("header %q", got)
	}
}
