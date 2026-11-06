package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHookLifecycle(t *testing.T) {
	repo := newGitRepo(t)
	projectDir := filepath.Join(repo, "apis")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(projectDir, "project.yaml")
	if err := os.WriteFile(project, []byte(starterProject), 0o644); err != nil {
		t.Fatal(err)
	}

	hook, err := InstallHook(HookOptions{Project: project, Fmt: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "verity scan-secrets --staged 'apis/project.yaml'") ||
		!strings.Contains(string(first), "verity fmt --check --staged 'apis/project.yaml'") {
		t.Fatalf("hook commands:\n%s", first)
	}
	if st, _ := os.Stat(hook); runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("hook is not executable: %v", st.Mode())
	}
	if _, err := InstallHook(HookOptions{Project: project, Fmt: true}); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(hook)
	if string(first) != string(second) {
		t.Fatal("idempotent reinstall changed the hook")
	}
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(HookOptions{Project: project, Fmt: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(hook); runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Fatal("reinstall did not restore the executable bit")
	}

	if _, err := UninstallHook(project); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("#!/bin/sh\n# local hook\nexit 0\n")
	if err := os.WriteFile(hook, foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(HookOptions{Project: project}); err == nil {
		t.Fatal("foreign hook was overwritten without force")
	}
	if _, err := UninstallHook(project); err == nil {
		t.Fatal("uninstall removed a foreign hook")
	}
	if _, err := InstallHook(HookOptions{Project: project, Force: true}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(hook + ".verity-backup")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(foreign) {
		t.Fatal("force did not preserve the foreign hook backup")
	}
	if _, err := UninstallHook(project); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("marked hook still exists after uninstall")
	}
	if _, err := UninstallHook(project); err != nil {
		t.Fatalf("uninstall with no hook should be a no-op: %v", err)
	}
}

func TestHookHonoursCoreHooksPath(t *testing.T) {
	repo := newGitRepo(t)
	project := filepath.Join(repo, "project.yaml")
	if err := os.WriteFile(project, []byte(starterProject), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "core.hooksPath", ".githooks")
	hook, err := InstallHook(HookOptions{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repo, ".githooks", "pre-commit")
	if hook != want {
		t.Fatalf("hook path %s, want %s", hook, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestHookSetupErrors(t *testing.T) {
	t.Run("not a repository", func(t *testing.T) {
		requireGit(t)
		dir := t.TempDir()
		project := filepath.Join(dir, "project.yaml")
		if err := os.WriteFile(project, []byte(starterProject), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallHook(HookOptions{Project: project}); err == nil || !strings.Contains(err.Error(), "git repository") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("git missing", func(t *testing.T) {
		requireGit(t)
		dir := t.TempDir()
		project := filepath.Join(dir, "project.yaml")
		if err := os.WriteFile(project, []byte(starterProject), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		if _, err := InstallHook(HookOptions{Project: project}); err == nil || !strings.Contains(err.Error(), "git was not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestHookBlocksAndAllowsRealCommit(t *testing.T) {
	tests := []struct {
		name       string
		verityExit int
		wantCommit bool
	}{
		{name: "scanner failure blocks commit", verityExit: 1, wantCommit: false},
		{name: "scanner success allows commit", verityExit: 0, wantCommit: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newGitRepo(t)
			project := filepath.Join(repo, "project.yaml")
			if err := os.WriteFile(project, []byte(starterProject), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := InstallHook(HookOptions{Project: project, Fmt: tc.verityExit == 0}); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(repo, "bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			fake := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> \"$VERITY_TEST_HOOK_LOG\"\nexit %d\n", tc.verityExit)
			if err := os.WriteFile(filepath.Join(bin, "verity"), []byte(fake), 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "project.yaml")
			logPath := filepath.Join(repo, "calls.log")
			cmd := exec.Command("git", "commit", "-m", "test commit")
			cmd.Dir = repo
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"VERITY_TEST_HOOK_LOG="+logPath,
			)
			output, err := cmd.CombinedOutput()
			if tc.wantCommit && err != nil {
				t.Fatalf("commit was blocked:\n%s", output)
			}
			if !tc.wantCommit {
				if err == nil {
					t.Fatal("commit passed despite scanner failure")
				}
				lines := strings.Split(strings.TrimSpace(string(output)), "\n")
				if lines[len(lines)-1] != "Bypass with: git commit --no-verify" {
					t.Fatalf("bypass hint is not the last line:\n%s", output)
				}
			}
			calls, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "scan-secrets --staged project.yaml") {
				t.Fatalf("scanner was not called: %s", calls)
			}
			if tc.verityExit == 0 && !strings.Contains(string(calls), "fmt --check --staged project.yaml") {
				t.Fatalf("fmt was not called: %s", calls)
			}
		})
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Verity Test")
	runGit(t, repo, "config", "user.email", "verity")
	return repo
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable; skipping git hook integration test")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
