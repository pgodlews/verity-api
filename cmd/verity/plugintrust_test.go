package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
)

// writeUntrustedPluginProject writes a project whose .verity/plugins.json
// lists a shell script that creates a marker file when it is started.
func writeUntrustedPluginProject(t *testing.T) (project, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("marker plugin is a POSIX shell script")
	}
	dir := t.TempDir()
	marker = filepath.Join(dir, "plugin-started")
	if err := os.MkdirAll(filepath.Join(dir, ".verity"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, ".verity", "evil.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > \""+marker+"\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".verity", "plugins.json"), []byte(`{"plugins":[{"name":"evil","path":"evil.sh"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project = filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(project, []byte(projectYAML("http://127.0.0.1:1/unused")), 0o644); err != nil {
		t.Fatal(err)
	}
	return project, marker
}

func TestProjectPluginsNeedTrust(t *testing.T) {
	commands := map[string][]string{
		"run":  {"run"},
		"load": {"load", "--iterations=1"},
	}
	for name, base := range commands {
		t.Run(name+"/untrusted", func(t *testing.T) {
			t.Setenv(plugin.TrustEnv, "")
			project, marker := writeUntrustedPluginProject(t)
			args := append(append([]string{}, base...), project)
			var code int
			withStderr(t, func() { code = run(args) })
			if code != exitConfig {
				t.Fatalf("exit %d, want %d", code, exitConfig)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("untrusted plugin was started (stat: %v)", err)
			}
		})
		for _, trust := range []string{"flag", "env"} {
			t.Run(name+"/trusted-"+trust, func(t *testing.T) {
				t.Setenv(plugin.TrustEnv, "")
				project, marker := writeUntrustedPluginProject(t)
				args := append([]string{}, base...)
				if trust == "flag" {
					args = append(args, "--trust-plugins")
				} else {
					t.Setenv(plugin.TrustEnv, "1")
				}
				args = append(args, project)
				withStderr(t, func() { _ = run(args) })
				// The script exits without a handshake, so the run fails, but
				// it must have been started.
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("trusted plugin did not start: %v", err)
				}
			})
		}
	}
}

func TestWatchRunOnceRefusesUntrustedPlugins(t *testing.T) {
	t.Setenv(plugin.TrustEnv, "")
	project, marker := writeUntrustedPluginProject(t)
	var code int
	withStderr(t, func() {
		code, _, _ = runOnce(context.Background(), watchConfig{ProjectPath: project})
	})
	if code != exitConfig {
		t.Fatalf("exit %d, want %d", code, exitConfig)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("untrusted plugin was started (stat: %v)", err)
	}
}
