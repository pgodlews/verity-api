package workspace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
)

func runOptionsProject(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	yaml := `schemaVersion: "1.0"
name: opts
environments: [{name: local, default: true}]
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: get
            protocol: rest
            request: {url: "` + url + `"}
            assertions: [{type: status, value: 200}]
`
	path := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunFollowsRedirectsLikeTheCLI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	path := runOptionsProject(t, srv.URL+"/old")

	res, err := Run(context.Background(), RunOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status != spi.StatusPassed {
		t.Fatalf("default run must follow the redirect: %+v", res.Summary)
	}
	res, err = Run(context.Background(), RunOptions{Path: path, NoRedirects: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status == spi.StatusPassed {
		t.Fatal("noRedirects run must see the 302")
	}
}

func TestRunTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	path := runOptionsProject(t, srv.URL)

	start := time.Now()
	res, err := Run(context.Background(), RunOptions{Path: path, TimeoutMs: 100})
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Status == spi.StatusPassed || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("timeoutMs not applied: %+v after %v", res.Summary, time.Since(start))
	}
}
