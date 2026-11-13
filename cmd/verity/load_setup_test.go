package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func loadSetupProject(t *testing.T, url, setupStep string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "project.yaml")
	writeCommandFile(t, path, `schemaVersion: "1.0"
name: setup
environments:
  - name: local
    default: true
suites:
  - name: s
    setup:
      name: login
      steps:
`+setupStep+`
    teardown:
      name: cleanup
      steps:
        - name: logout
          protocol: rest
          request: { url: "`+url+`/teardown" }
    cases:
      - name: c
        steps:
          - name: traffic
            protocol: rest
            request: { url: "`+url+`/traffic" }
`)
	return path
}

// A failing setup aborts before any virtual user starts; teardown still
// runs; the report says setup failed; the exit code is 1 for a failed check
// and 3 for a config problem.
func TestLoadSetupFailure(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/setup" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name  string
		step  string
		want  int
		setup int
	}{
		{"failed check", `        - name: login
          protocol: rest
          request: { url: "` + srv.URL + `/setup" }
          assertions: [{ type: status, value: 200 }]`, exitTestFailed, 1},
		{"config error", `        - name: login
          protocol: rest
          request:
            url: "` + srv.URL + `/setup"
            method: POST
            bodyFile: missing-upload.bin`, exitConfig, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			hits = map[string]int{}
			mu.Unlock()
			report := filepath.Join(t.TempDir(), "load.json")
			code, _, stderr := captureCommand(t, "", func() int {
				return run([]string{"load", loadSetupProject(t, srv.URL, tc.step), "--iterations", "2", "--vus", "2", "--json", report})
			})
			if code != tc.want {
				data, _ := os.ReadFile(report)
				t.Fatalf("exit %d, want %d: %s\n%s", code, tc.want, stderr, data)
			}
			mu.Lock()
			defer mu.Unlock()
			if hits["/traffic"] != 0 || hits["/teardown"] != 1 || hits["/setup"] != tc.setup {
				t.Fatalf("hits %v", hits)
			}
			data, err := os.ReadFile(report)
			if err != nil {
				t.Fatal(err)
			}
			var rep struct {
				SetupFailed bool              `json:"setupFailed"`
				Passed      bool              `json:"passed"`
				Teardown    []json.RawMessage `json:"teardown"`
			}
			if err := json.Unmarshal(data, &rep); err != nil {
				t.Fatal(err)
			}
			if !rep.SetupFailed || rep.Passed || len(rep.Teardown) != 1 {
				t.Fatalf("report %s", data)
			}
		})
	}
}
