package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/verr"
)

func writePluginMarkerProject(t *testing.T) (project, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("marker plugin is a POSIX shell script")
	}
	dir := t.TempDir()
	marker = filepath.Join(dir, "plugin-started")
	if err := os.MkdirAll(filepath.Join(dir, ".verity"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".verity", "p.sh"), []byte("#!/bin/sh\n: > \""+marker+"\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".verity", "plugins.json"), []byte(`{"plugins":[{"name":"p","path":"p.sh"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project = filepath.Join(dir, "project.yaml")
	body := "schemaVersion: \"1.0\"\nname: p\nenvironments:\n  - name: local\n    default: true\nsuites:\n  - name: s\n    cases:\n      - name: c\n        steps:\n          - name: get\n            protocol: rest\n            request:\n              method: GET\n              url: \"http://127.0.0.1:1/\"\n"
	if err := os.WriteFile(project, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return project, marker
}

func TestWorkspaceRefusesUntrustedProjectPlugins(t *testing.T) {
	t.Setenv(plugin.TrustEnv, "")
	project, marker := writePluginMarkerProject(t)
	calls := map[string]func() error{
		"Run": func() error { _, err := Run(context.Background(), RunOptions{Path: project}); return err },
		"Session.Run": func() error {
			var s Session
			_, err := s.Run(context.Background(), RunOptions{Path: project}, nil, nil)
			return err
		},
		"Load": func() error {
			_, err := Load(context.Background(), LoadOptions{Path: project, VUs: 1, Iterations: 1})
			return err
		},
		"LoadTargets": func() error {
			_, err := LoadTargets(context.Background(), LoadOptions{Path: project, VUs: 1, Iterations: 1}, nil, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
				t.Fatalf("error = %v, want %s", err, verr.CodeConfig)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("untrusted plugin was started (stat: %v)", statErr)
			}
		})
	}
}

func TestSessionTrustPluginsStartsProjectPlugins(t *testing.T) {
	t.Setenv(plugin.TrustEnv, "")
	project, marker := writePluginMarkerProject(t)
	var asked plugin.Fingerprint
	s := Session{TrustPlugins: func(fp plugin.Fingerprint) (bool, error) { asked = fp; return true, nil }}
	_, err := s.Run(context.Background(), RunOptions{Path: project}, nil, nil)
	if got, ok := verr.As(err); !ok || got.Code != verr.CodePlugin {
		t.Fatalf("error = %v, want %s from the handshake-less plugin", err, verr.CodePlugin)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("trusted plugin did not start: %v", statErr)
	}
	if filepath.Base(asked.Path) != "plugins.json" || len(asked.Binaries) != 1 {
		t.Fatalf("fingerprint = %#v", asked)
	}
}
