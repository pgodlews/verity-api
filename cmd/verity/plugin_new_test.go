package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginNewNamesAndIdentifiers(t *testing.T) {
	if got := exportedIdent("my-plugin_v2"); got != "MyPluginV2" {
		t.Fatalf("exportedIdent = %q", got)
	}
	for _, bad := range []string{"../x", "My", "1abc", "a b", "a/b", ""} {
		var code int
		withStderr(t, func() {
			code = run([]string{"plugin", "new", "assertion", bad, "--out", filepath.Join(t.TempDir(), "p")})
		})
		if code != exitUsage {
			t.Errorf("name %q: exit %d, want %d", bad, code, exitUsage)
		}
	}
}

func TestPluginNewGeneratesValidGoAndRefusesOverwrite(t *testing.T) {
	for _, typ := range []string{"protocol", "assertion", "reporter", "auth", "datasource"} {
		t.Run(typ, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "out")
			var code int
			out := withStdout(t, func() {
				code = run([]string{"plugin", "new", typ, "my-check", "--out", dir, "--json"})
			})
			if code != exitOK {
				t.Fatalf("exit %d: %s", code, out)
			}
			var res struct {
				OK    bool     `json:"ok"`
				Files []string `json:"files"`
			}
			if err := json.Unmarshal([]byte(out), &res); err != nil || !res.OK || len(res.Files) == 0 {
				t.Fatalf("json = %s (%v)", out, err)
			}
			for _, f := range res.Files {
				if !strings.HasSuffix(f, ".go") {
					continue
				}
				if _, err := parser.ParseFile(token.NewFileSet(), f, nil, 0); err != nil {
					t.Fatalf("generated %s is not valid Go: %v", f, err)
				}
			}
			main, err := os.ReadFile(filepath.Join(dir, "main.go"))
			if err != nil || !strings.Contains(string(main), "MyCheck") {
				t.Fatalf("main.go identifiers: %v\n%s", err, main)
			}

			// A second run must not overwrite without --force.
			marker := filepath.Join(dir, "main.go")
			if err := os.WriteFile(marker, []byte("// edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			withStdout(t, func() {
				code = run([]string{"plugin", "new", typ, "my-check", "--out", dir, "--json"})
			})
			if code != exitConfig {
				t.Fatalf("overwrite exit %d, want %d", code, exitConfig)
			}
			if b, _ := os.ReadFile(marker); string(b) != "// edited\n" {
				t.Fatal("existing file was overwritten")
			}
			withStdout(t, func() {
				code = run([]string{"plugin", "new", typ, "my-check", "--out", dir, "--force", "--json"})
			})
			if code != exitOK {
				t.Fatalf("--force exit %d", code)
			}
		})
	}
}
