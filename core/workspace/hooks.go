package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/verr"
)

const hookMarker = "# verity: pre-commit"

// HookOptions identifies the project to scan and whether formatting is checked.
type HookOptions struct {
	Project string
	Force   bool
	Fmt     bool
}

// InstallHook installs Verity's pre-commit hook using Git's configured hooks
// path. The project path stored in the hook is relative to the repository root.
func InstallHook(opts HookOptions) (string, error) {
	project := opts.Project
	if project == "" {
		project = "project.yaml"
	}
	absProject, err := filepath.Abs(project)
	if err != nil {
		return "", verr.Wrap(verr.CodeConfig, err, "resolve project path")
	}
	if st, err := os.Stat(absProject); err != nil || !st.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return "", verr.Wrap(verr.CodeConfig, err, "hook project must be an existing file")
	}
	projectDir := filepath.Dir(absProject)
	root, hooksDir, err := gitHookPaths(projectDir)
	if err != nil {
		return "", err
	}
	relProject, err := repoRelative(root, absProject)
	if err != nil || relProject == ".." || strings.HasPrefix(relProject, ".."+string(filepath.Separator)) {
		return "", verr.New(verr.CodeConfig, "hook project must be inside the git repository")
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "create git hooks directory")
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	script := []byte(preCommitScript(filepath.ToSlash(relProject), opts.Fmt))
	existing, readErr := os.ReadFile(hookPath)
	if readErr == nil && !markedHook(existing) && !opts.Force {
		return "", verr.Newf(verr.CodeConfig, "%s already exists without the Verity marker; use --force to back it up and replace it", hookPath)
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", verr.Wrap(verr.CodeInternal, readErr, "read existing pre-commit hook")
	}
	if readErr == nil && !markedHook(existing) {
		backup := hookPath + ".verity-backup"
		if _, err := os.Stat(backup); err == nil {
			return "", verr.Newf(verr.CodeConfig, "backup %s already exists; move it away before using --force", backup)
		} else if !os.IsNotExist(err) {
			return "", verr.Wrap(verr.CodeInternal, err, "inspect hook backup")
		}
		if err := os.Rename(hookPath, backup); err != nil {
			return "", verr.Wrap(verr.CodeInternal, err, "back up pre-commit hook")
		}
		if err := fsutil.WriteKeepingMode(hookPath, script); err != nil {
			_ = os.Rename(backup, hookPath)
			return "", verr.Wrap(verr.CodeInternal, err, "write pre-commit hook")
		}
	} else if readErr == nil && string(existing) == string(script) {
		// Re-installation is idempotent apart from restoring the executable bit.
	} else if err := fsutil.WriteKeepingMode(hookPath, script); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "write pre-commit hook")
	}
	if err := os.Chmod(hookPath, 0o755); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "make pre-commit hook executable")
	}
	return hookPath, nil
}

// UninstallHook removes only a hook carrying Verity's marker.
func UninstallHook(project string) (string, error) {
	if project == "" {
		project = "project.yaml"
	}
	absProject, err := filepath.Abs(project)
	if err != nil {
		return "", verr.Wrap(verr.CodeConfig, err, "resolve project path")
	}
	_, hooksDir, err := gitHookPaths(filepath.Dir(absProject))
	if err != nil {
		return "", err
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	data, err := os.ReadFile(hookPath)
	if os.IsNotExist(err) {
		return hookPath, nil
	}
	if err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "read pre-commit hook")
	}
	if !markedHook(data) {
		return "", verr.New(verr.CodeConfig, "pre-commit hook is not managed by Verity; leaving it unchanged")
	}
	if err := os.Remove(hookPath); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "remove pre-commit hook")
	}
	return hookPath, nil
}

func gitHookPaths(dir string) (string, string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", verr.New(verr.CodeConfig, "git was not found on PATH; install Git to manage hooks")
	}
	root, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	hooks, err := gitOutput(dir, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(dir, hooks)
	}
	hooks, err = filepath.Abs(hooks)
	if err != nil {
		return "", "", verr.Wrap(verr.CodeConfig, err, "resolve git hooks path")
	}
	return root, hooks, nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", verr.Newf(verr.CodeConfig, "%s is not in a usable git repository: %s", dir, message)
	}
	return strings.TrimSpace(string(output)), nil
}

func markedHook(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSuffix(line, "\r") == hookMarker {
			return true
		}
	}
	return false
}

func preCommitScript(project string, checkFmt bool) string {
	quoted := "'" + strings.ReplaceAll(project, "'", "'\"'\"'") + "'"
	var out strings.Builder
	out.WriteString("#!/bin/sh\n")
	out.WriteString(hookMarker + "\n")
	out.WriteString("repo_root=$(git rev-parse --show-toplevel) || {\n")
	out.WriteString("  printf '%s\\n' 'Verity could not locate the repository root.' >&2\n")
	out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
	out.WriteString("  exit 1\n")
	out.WriteString("}\n")
	out.WriteString("cd \"$repo_root\" || {\n")
	out.WriteString("  printf '%s\\n' 'Verity could not enter the repository root.' >&2\n")
	out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
	out.WriteString("  exit 1\n")
	out.WriteString("}\n")
	out.WriteString("if ! command -v verity >/dev/null 2>&1; then\n")
	out.WriteString("  printf '%s\\n' 'verity was not found on PATH; install Verity and ensure verity is on PATH.' >&2\n")
	out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
	out.WriteString("  exit 1\n")
	out.WriteString("fi\n")
	out.WriteString("verity scan-secrets --staged " + quoted + "\n")
	out.WriteString("status=$?\n")
	out.WriteString("if [ \"$status\" -ne 0 ]; then\n")
	out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
	out.WriteString("  exit \"$status\"\n")
	out.WriteString("fi\n")
	if checkFmt {
		out.WriteString("verity fmt --check --staged " + quoted + "\n")
		out.WriteString("status=$?\n")
		out.WriteString("if [ \"$status\" -ne 0 ]; then\n")
		out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
		out.WriteString("  exit \"$status\"\n")
		out.WriteString("fi\n")
	}
	return out.String()
}
