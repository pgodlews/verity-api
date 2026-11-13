//go:build devbridge

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestGUICallProtocolAndErrors(t *testing.T) {
	t.Run("catalog", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "[]", func() int { return run([]string{"gui-call", "Catalog"}) })
		got := decodeCommandJSON(t, stdout)
		if code != exitOK || got["ok"] != true || got["value"] == nil {
			t.Fatalf("exit = %d, JSON = %#v", code, got)
		}
	})
	for _, method := range []string{"OAuthLogin", "OAuthLogout", "OAuthStatus", "PickFile", "OpenFile", "CancelGit", "WatchProject", "UnwatchProject", "Unknown"} {
		t.Run(method, func(t *testing.T) {
			code, stdout, _ := captureCommand(t, "[]", func() int { return run([]string{"gui-call", method}) })
			got := decodeCommandJSON(t, stdout)
			if code != exitOK || got["ok"] != false || got["error"] == "" {
				t.Fatalf("exit = %d, JSON = %#v", code, got)
			}
		})
	}
	for _, tt := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{name: "missing method"},
		{name: "too many args", args: []string{"gui-call", "Catalog", "extra"}},
		{name: "malformed arguments", args: []string{"gui-call", "Catalog"}, stdin: "not-json"},
		{name: "missing method argument", args: []string{"gui-call", "InspectProject"}, stdin: "[]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := captureCommand(t, tt.stdin, func() int { return run(tt.args) })
			if tt.name == "missing method" || tt.name == "too many args" {
				if code != exitUsage || !strings.Contains(stderr, "usage") {
					t.Fatalf("exit = %d, stderr = %q", code, stderr)
				}
				return
			}
			got := decodeCommandJSON(t, stdout)
			if code != exitOK || got["ok"] != false {
				t.Fatalf("exit = %d, JSON = %#v", code, got)
			}
		})
	}
}

func TestGUICallWorkspaceMethods(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project.yaml")
	writeCommandFile(t, project, "schemaVersion: \"1.0\"\nname: bridge\nenvironments:\n  - name: local\n    default: true\nsuites: []\n")
	schema := filepath.Join(dir, "schema.graphql")
	writeCommandFile(t, schema, "type Query { hello: String }\n")

	tests := []struct {
		name   string
		method string
		params string
	}{
		{name: "validate", method: "ValidateProject", params: `["` + project + `","local"]`},
		{name: "templates", method: "GraphQLTemplates", params: `[{"schema":"` + schema + `","operation":"Query.hello"}]`},
		{name: "inspect", method: "InspectProject", params: `["` + project + `"]`},
		{name: "run", method: "RunProject", params: `[{"projectPath":"` + project + `"}]`},
		{name: "save step", method: "SaveStep", params: `[{}]`},
		{name: "check step", method: "CheckStep", params: `[{}]`},
		{name: "save environment", method: "SaveEnvironment", params: `[{}]`},
		{name: "import preview", method: "ImportPreview", params: `["openapi","missing.yaml"]`},
		{name: "import save", method: "ImportSave", params: `[{}]`},
		{name: "git status", method: "GitStatus", params: `["` + dir + `"]`},
		{name: "git changes", method: "GitChanges", params: `["` + dir + `"]`},
		{name: "git commit", method: "GitCommit", params: `[{}]`},
		{name: "git pull", method: "GitPull", params: `[{}]`},
		{name: "git fetch", method: "GitFetch", params: `["` + dir + `"]`},
		{name: "git push", method: "GitPush", params: `["` + dir + `"]`},
		{name: "git push upstream", method: "GitPushUpstream", params: `[{}]`},
		{name: "git branches", method: "GitBranches", params: `["` + dir + `"]`},
		{name: "git switch", method: "GitSwitch", params: `[{}]`},
		{name: "git create branch", method: "GitCreateBranch", params: `[{}]`},
		{name: "git merge abort", method: "GitMergeAbort", params: `["` + dir + `"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, _ := captureCommand(t, tt.params, func() int {
				return run([]string{"gui-call", tt.method})
			})
			got := decodeCommandJSON(t, stdout)
			if code != exitOK || got["ok"] == nil {
				t.Fatalf("exit = %d, JSON = %#v", code, got)
			}
		})
	}
}

// Exercise the browser dev bridge's keychain methods against go-keyring's mock.
func TestDevBridgeKeychain(t *testing.T) {
	keyring.MockInit()
	for _, tc := range []struct {
		args  []string
		input string
		ok    bool
	}{
		{[]string{"gui-call", "StoreKeychainSecret"}, `["project/BRIDGE_TOKEN","test-sensitive-value"]`, true},
		{[]string{"gui-call", "StoreKeychainSecret"}, `["bad:name","test-sensitive-value"]`, false},
		{[]string{"gui-call", "KeychainSecretExists"}, `["project/BRIDGE_TOKEN"]`, true},
		{[]string{"gui-call", "KeychainSecretExists"}, `["MISSING_BRIDGE_TOKEN"]`, true},
		{[]string{"gui-call", "KeychainSecretExists"}, `["bad:name"]`, false},
	} {
		code, output := secretCLIOutput(t, tc.args, tc.input)
		// gui-call reports operation errors in its envelope and always exits 0.
		if code != exitOK {
			t.Fatalf("%v: exit %d, want 0", tc.args, code)
		}
		if strings.Contains(output, "test-sensitive-value") {
			t.Fatal("CLI/bridge output leaked the value")
		}
		var result struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil || result.OK != tc.ok {
			t.Fatal("CLI/bridge returned an unexpected envelope")
		}
		if len(tc.args) > 1 && tc.args[1] == "KeychainSecretExists" && tc.ok {
			var exists bool
			if err := json.Unmarshal(result.Value, &exists); err != nil || exists != strings.Contains(tc.input, "project/BRIDGE_TOKEN") {
				t.Fatal("existence binding did not return the expected boolean")
			}
		}
	}
	if value, err := keyring.Get("verity", "project/BRIDGE_TOKEN"); err != nil || value != "test-sensitive-value" {
		t.Fatal("browser bridge did not store the mock credential")
	}
}
