package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Installing the hook for a second project keeps the first; uninstalling
// one project leaves the other; the hook is removed with the last project.
func TestHookChecksSeveralProjects(t *testing.T) {
	repo := newGitRepo(t)
	write := func(rel string) string {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(starterProject), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a/project.yaml")
	b := write("b/it's.yaml") // a quote must survive the shell quoting round trip

	hook, err := InstallHook(HookOptions{Project: a, Fmt: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(HookOptions{Project: b}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(hook)
	got := parseHookProjects(data)
	if len(got) != 2 || got[0] != (hookProject{Path: "a/project.yaml", Fmt: true}) || got[1] != (hookProject{Path: "b/it's.yaml"}) {
		t.Fatalf("projects = %+v\n%s", got, data)
	}

	if _, err := UninstallHook(a); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(hook)
	if err != nil {
		t.Fatalf("hook removed while a project remains: %v", err)
	}
	if strings.Contains(string(data), "a/project.yaml") || len(parseHookProjects(data)) != 1 {
		t.Fatalf("after uninstall:\n%s", data)
	}
	if _, err := UninstallHook(b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatalf("hook kept after the last project: %v", err)
	}
}
