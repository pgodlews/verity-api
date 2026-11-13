package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
)

// TestReporterPluginHelper is a reporter plugin: on report it writes the
// run's project name to the requested file.
func TestReporterPluginHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "reporter_plugin" {
		return
	}
	defer os.Exit(0)
	_ = json.NewEncoder(os.Stdout).Encode(plugin.Handshake{Magic: plugin.MagicCookie, Version: plugin.ProtocolVersion, Kind: "reporter", Type: "summary"})
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req plugin.Request
		if err := dec.Decode(&req); err != nil || req.Method == plugin.MethodShutdown {
			return
		}
		if req.Method == plugin.MethodReport {
			out, _ := req.Params["out"].(string)
			result, _ := req.Params["result"].(map[string]any)
			name, _ := result["project"].(string)
			_ = os.WriteFile(out, []byte("project="+name), 0o644)
		}
		_ = enc.Encode(plugin.Response{ID: req.ID, Result: map[string]any{"ok": true}})
	}
}

func TestRunReporterFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	dir := t.TempDir()
	project := filepath.Join(dir, "p.yaml")
	if err := os.WriteFile(project, []byte(projectYAML(srv.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	pluginsFile := filepath.Join(dir, "plugins.json")
	body, _ := json.Marshal(plugin.PluginsFile{Plugins: []plugin.Manifest{{
		Name: "summary", Path: os.Args[0], Args: []string{"-test.run=^TestReporterPluginHelper$", "--", "reporter_plugin"},
	}}})
	if err := os.WriteFile(pluginsFile, body, 0o644); err != nil {
		t.Fatal(err)
	}

	pluginOut := filepath.Join(dir, "summary.txt")
	jsonOut := filepath.Join(dir, "result.json")
	var code int
	withStderr(t, func() {
		code = run([]string{"run", project, "--quiet", "--plugins", pluginsFile,
			"--reporter", "summary=" + pluginOut, "--reporter", "json=" + jsonOut})
	})
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if got, err := os.ReadFile(pluginOut); err != nil || string(got) != "project=load" {
		t.Fatalf("reporter plugin output = %q, %v", got, err)
	}
	if _, err := os.Stat(jsonOut); err != nil {
		t.Fatalf("json reporter: %v", err)
	}

	withStderr(t, func() { code = run([]string{"run", project, "--reporter", "nope"}) })
	if code != exitUsage {
		t.Fatalf("malformed --reporter exit %d, want %d", code, exitUsage)
	}
	withStderr(t, func() { code = run([]string{"run", project, "--quiet", "--reporter", "missing=" + pluginOut}) })
	if code != exitConfig {
		t.Fatalf("unknown reporter exit %d, want %d", code, exitConfig)
	}
}
