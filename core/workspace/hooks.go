package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/vcs"
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
	existing, readErr := os.ReadFile(hookPath)
	// A Verity hook checks every project installed into it: keep the other
	// projects already in a marked hook and add or update this one.
	projects := []hookProject{}
	if readErr == nil && markedHook(existing) {
		projects = parseHookProjects(existing)
	}
	projects = upsertHookProject(projects, hookProject{Path: filepath.ToSlash(relProject), Fmt: opts.Fmt})
	script := []byte(preCommitScript(projects))
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

// UninstallHook removes project from the Verity pre-commit hook. The hook
// file is deleted when no project is left; a hook without Verity's marker
// is never touched.
func UninstallHook(project string) (string, error) {
	if project == "" {
		project = "project.yaml"
	}
	absProject, err := filepath.Abs(project)
	if err != nil {
		return "", verr.Wrap(verr.CodeConfig, err, "resolve project path")
	}
	root, hooksDir, err := gitHookPaths(filepath.Dir(absProject))
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
	rest := []hookProject{}
	if relProject, rerr := repoRelative(root, absProject); rerr == nil {
		for _, p := range parseHookProjects(data) {
			if p.Path != filepath.ToSlash(relProject) {
				rest = append(rest, p)
			}
		}
	}
	if len(rest) > 0 {
		if err := fsutil.WriteKeepingMode(hookPath, []byte(preCommitScript(rest))); err != nil {
			return "", verr.Wrap(verr.CodeInternal, err, "rewrite pre-commit hook")
		}
		return hookPath, nil
	}
	if err := os.Remove(hookPath); err != nil {
		return "", verr.Wrap(verr.CodeInternal, err, "remove pre-commit hook")
	}
	return hookPath, nil
}

// hookProject is one project the pre-commit hook checks.
type hookProject struct {
	Path string // repository-relative, slash-separated
	Fmt  bool   // also run fmt --check --staged
}

func upsertHookProject(list []hookProject, p hookProject) []hookProject {
	for i := range list {
		if list[i].Path == p.Path {
			list[i] = p
			return list
		}
	}
	return append(list, p)
}

const (
	scanPrefix = "verity scan-secrets --staged "
	fmtPrefix  = "verity fmt --check --staged "
)

// parseHookProjects reads the projects back from a Verity hook script (this
// version's or the earlier single-project one) by its scan-secrets and fmt
// command lines.
func parseHookProjects(data []byte) []hookProject {
	var out []hookProject
	fmtFor := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if rest, ok := strings.CutPrefix(line, fmtPrefix); ok {
			if p, ok := shellUnquote(rest); ok {
				fmtFor[p] = true
			}
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if rest, ok := strings.CutPrefix(line, scanPrefix); ok {
			if p, ok := shellUnquote(rest); ok {
				out = upsertHookProject(out, hookProject{Path: p, Fmt: fmtFor[p]})
			}
		}
	}
	return out
}

// shellQuote single-quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// shellUnquote reverses shellQuote: a sequence of '...' and "'" pieces.
func shellUnquote(s string) (string, bool) {
	var b strings.Builder
	for s != "" {
		switch s[0] {
		case '\'':
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(s[1 : 1+end])
			s = s[end+2:]
		case '"':
			if !strings.HasPrefix(s, `"'"`) {
				return "", false
			}
			b.WriteByte('\'')
			s = s[3:]
		default:
			return "", false
		}
	}
	return b.String(), true
}

// gitHookPaths returns the repository root and the hooks directory (which
// honours worktrees and core.hooksPath) through core/vcs.
func gitHookPaths(dir string) (string, string, error) {
	if !vcs.Available() {
		return "", "", verr.New(verr.CodeConfig, "git was not found on PATH; install Git to manage hooks")
	}
	root, err := vcs.RepoRoot(dir)
	if err != nil {
		return "", "", verr.Newf(verr.CodeConfig, "%s is not in a usable git repository: %s", dir, secrets.RedactGitOutput(err.Error()))
	}
	hooks, err := vcs.GitPath(dir, "hooks")
	if err != nil {
		return "", "", verr.Newf(verr.CodeConfig, "%s is not in a usable git repository: %s", dir, secrets.RedactGitOutput(err.Error()))
	}
	return root, hooks, nil
}

func markedHook(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSuffix(line, "\r") == hookMarker {
			return true
		}
	}
	return false
}

func preCommitScript(projects []hookProject) string {
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
	for _, p := range projects {
		quoted := shellQuote(p.Path)
		out.WriteString(scanPrefix + quoted + "\n")
		out.WriteString("status=$?\n")
		out.WriteString("if [ \"$status\" -ne 0 ]; then\n")
		out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
		out.WriteString("  exit \"$status\"\n")
		out.WriteString("fi\n")
		if p.Fmt {
			out.WriteString(fmtPrefix + quoted + "\n")
			out.WriteString("status=$?\n")
			out.WriteString("if [ \"$status\" -ne 0 ]; then\n")
			out.WriteString("  printf '%s\\n' 'Bypass with: git commit --no-verify' >&2\n")
			out.WriteString("  exit \"$status\"\n")
			out.WriteString("fi\n")
		}
	}
	return out.String()
}
