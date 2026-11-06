package vcs

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSplitNULMeanings(t *testing.T) {
	in := []byte("a\x00\x00b\x00")
	status := splitStatusRecords(in)
	if len(status) != 3 || status[0] != "a" || status[1] != "" || status[2] != "b" {
		t.Fatalf("status records: %#v", status)
	}
	paths := splitNULPaths(in)
	if len(paths) != 2 || paths[0] != "a" || paths[1] != "b" {
		t.Fatalf("paths: %#v", paths)
	}
	if splitStatusRecords(nil) != nil || splitNULPaths(nil) != nil {
		t.Fatal("empty input")
	}
}

func TestParseStatusPorcelain(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ordinary := "1 .M N... 100644 100644 100644 " + hash + " " + hash + " file.txt"
	rename := "2 R. N... 100644 100644 100644 " + hash + " " + hash + " R100 new name.txt"
	unmerged := "u UU N... 100644 100644 100644 100644 " + hash + " " + hash + " " + hash + " conflict.txt"
	out := bytes.Join([][]byte{
		[]byte("# branch.oid abcdef1234567890abcdef1234567890abcdef12"),
		[]byte("# branch.head main"),
		[]byte("# branch.upstream origin/main"),
		[]byte("# branch.ab +2 -1"),
		[]byte(ordinary),
		[]byte(rename),
		[]byte("old name.txt"),
		[]byte(unmerged),
		[]byte("? new file.txt"),
		[]byte("! ignored.txt"),
	}, []byte{0})
	// Join puts NUL between parts and not after the last. Porcelain ends
	// every record with NUL, including the last.
	out = append(out, 0)

	st, err := parseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || st.Detached || st.Upstream != "origin/main" || !st.Tracking || st.Ahead != 2 || st.Behind != 1 {
		t.Fatalf("branch: %+v", st)
	}
	if st.Short != "abcdef1" {
		t.Fatalf("short %q", st.Short)
	}
	if len(st.Entries) != 4 {
		t.Fatalf("entries: %+v", st.Entries)
	}
	if st.Entries[0].Path != "file.txt" || st.Entries[0].Index != "." || st.Entries[0].Worktree != "M" || st.Entries[0].Untracked || st.Entries[0].Conflicted {
		t.Fatalf("ordinary: %+v", st.Entries[0])
	}
	if st.Entries[1].Path != "new name.txt" || st.Entries[1].RenamedFrom != "old name.txt" || st.Entries[1].Index != "R" {
		t.Fatalf("rename: %+v", st.Entries[1])
	}
	if !st.Entries[2].Conflicted || st.Entries[2].Path != "conflict.txt" {
		t.Fatalf("conflict: %+v", st.Entries[2])
	}
	if !st.Entries[3].Untracked || st.Entries[3].Path != "new file.txt" {
		t.Fatalf("untracked: %+v", st.Entries[3])
	}
}

func TestParseStatusKeepsTabCRLF(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	prefix := "1 .M N... 100644 100644 100644 " + hash + " " + hash + " "
	tabPath := "dir/a\tb.yaml"
	crPath := "trail.yaml\r"
	renamePath := "to\nfile.yaml"
	fromPath := "orig.yaml\r\n"
	untracked := "u\t\r\n.yaml"
	out := bytes.Join([][]byte{
		[]byte(prefix + tabPath),
		[]byte(prefix + crPath),
		[]byte("2 R. N... 100644 100644 100644 " + hash + " " + hash + " R100 " + renamePath),
		[]byte(fromPath),
		[]byte("? " + untracked),
	}, []byte{0})
	out = append(out, 0)
	st, err := parseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 4 {
		t.Fatalf("entries: %+v", st.Entries)
	}
	if st.Entries[0].Path != tabPath {
		t.Fatalf("tab path %q", st.Entries[0].Path)
	}
	if st.Entries[1].Path != crPath {
		t.Fatalf("cr path %q", st.Entries[1].Path)
	}
	if st.Entries[2].Path != renamePath || st.Entries[2].RenamedFrom != fromPath {
		t.Fatalf("rename path %q from %q", st.Entries[2].Path, st.Entries[2].RenamedFrom)
	}
	if !st.Entries[3].Untracked || st.Entries[3].Path != untracked {
		t.Fatalf("untracked %q", st.Entries[3].Path)
	}
}

func TestParseStatusDetached(t *testing.T) {
	out := []byte("# branch.oid 0123456789abcdef0123456789abcdef01234567\x00# branch.head (detached)\x00")
	st, err := parseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Detached || st.Branch != "" || st.Short != "0123456" || st.Tracking {
		t.Fatalf("%+v", st)
	}
}

func TestRepoRootNotARepository(t *testing.T) {
	skipNoGit(t)
	_, err := RepoRoot(t.TempDir())
	if !IsNotRepository(err) {
		t.Fatalf("got %v", err)
	}
}

func TestStatusRealTree(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "old.txt", "same\n")
	gitAddCommit(t, dir, "initial")
	gitRun(t, dir, "mv", "old.txt", "new file.txt")
	writeFile(t, dir, "extra file.txt", "x\n")
	writeFile(t, dir, "café.txt", "ok\n")

	st, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Root == "" || st.Branch == "" || st.Detached || st.Initial {
		t.Fatalf("header: %+v", st)
	}
	var renamed, untracked *Entry
	for i := range st.Entries {
		e := &st.Entries[i]
		switch e.Path {
		case "new file.txt":
			renamed = e
		case "extra file.txt":
			untracked = e
		}
		if strings.Contains(e.Path, "\\") {
			t.Fatalf("quotepath escaped a name: %q", e.Path)
		}
	}
	if renamed == nil || renamed.RenamedFrom != "old.txt" {
		t.Fatalf("rename entries: %+v", st.Entries)
	}
	if untracked == nil || !untracked.Untracked {
		t.Fatalf("untracked entries: %+v", st.Entries)
	}
	foundCafe := false
	for _, e := range st.Entries {
		if e.Path == "café.txt" && e.Untracked {
			foundCafe = true
		}
	}
	if !foundCafe {
		t.Fatalf("non-ascii path missing: %+v", st.Entries)
	}
}

func TestStatusConflicted(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "f.txt", "base\n")
	gitAddCommit(t, dir, "base")
	gitRun(t, dir, "checkout", "-b", "other")
	writeFile(t, dir, "f.txt", "other\n")
	gitAddCommit(t, dir, "other")
	gitRun(t, dir, "checkout", "main")
	writeFile(t, dir, "f.txt", "main\n")
	gitAddCommit(t, dir, "main side")
	if _, err := gitCmd(dir, "merge", "other"); err == nil {
		t.Fatal("merge unexpectedly succeeded")
	}
	st, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	var hit *Entry
	for i := range st.Entries {
		if st.Entries[i].Path == "f.txt" {
			hit = &st.Entries[i]
		}
	}
	if hit == nil || !hit.Conflicted {
		t.Fatalf("conflict entry: %+v", st.Entries)
	}
}

func TestStatusDetachedAndReadAt(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	body := []byte("hello\n")
	writeFile(t, dir, "note.txt", string(body))
	gitAddCommit(t, dir, "note")
	gitRun(t, dir, "checkout", "--detach")

	st, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Detached || st.Branch != "" || len(st.Short) != 7 {
		t.Fatalf("detached: %+v", st)
	}
	full := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	if !strings.HasPrefix(full, st.Short) {
		t.Fatalf("short %s is not a prefix of %s", st.Short, full)
	}
	got, err := ReadAt(st.Root, "HEAD", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("blob %q", got)
	}
	if _, err := ReadAt(st.Root, "HEAD", "missing.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}

func TestReadAtRefusesSymlink(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("secret-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A Git symlink blob does not require OS symlink privileges to create.
	stageBlob(t, dir, "120000", "link.txt", "target.txt")
	gitRun(t, dir, "commit", "-m", "link")
	got, err := ReadAt(dir, "HEAD", "link.txt")
	if err == nil {
		t.Fatalf("symlink was read: %q", got)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatal("symlink reported as missing")
	}
	if bytes.Contains(got, []byte("secret-body")) || strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("symlink target leaked: %v %q", err, got)
	}
}

func TestStatusDoesNotRewriteIndex(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "f.txt", "one\n")
	gitAddCommit(t, dir, "one")
	writeFile(t, dir, "f.txt", "two\n")
	index, err := GitPath(dir, "index")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("git status rewrote the index")
	}
}

func TestStatusAheadBehind(t *testing.T) {
	skipNoGit(t)
	a := t.TempDir()
	gitInit(t, a)
	writeFile(t, a, "f.txt", "one\n")
	gitAddCommit(t, a, "one")
	b := t.TempDir()
	gitRun(t, "", "clone", "--local", a, b)
	writeFile(t, a, "f.txt", "two\n")
	gitAddCommit(t, a, "two")
	gitRun(t, b, "fetch", "origin")
	behind, err := Status(b)
	if err != nil {
		t.Fatal(err)
	}
	if !behind.Tracking || behind.Ahead != 0 || behind.Behind != 1 || behind.Upstream == "" {
		t.Fatalf("behind: branch %s upstream %s ahead %d behind %d tracking %v", behind.Branch, behind.Upstream, behind.Ahead, behind.Behind, behind.Tracking)
	}
	writeFile(t, b, "g.txt", "local\n")
	gitAddCommit(t, b, "local")
	diverged, err := Status(b)
	if err != nil {
		t.Fatal(err)
	}
	if diverged.Ahead != 1 || diverged.Behind != 1 {
		t.Fatalf("diverged: ahead %d behind %d", diverged.Ahead, diverged.Behind)
	}
}

func TestReadAtLiteralBracketPath(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "project1.yaml", "wrong\n")
	writeFile(t, dir, "project[1].yaml", "right\n")
	gitAddCommit(t, dir, "initial")
	got, err := ReadAt(dir, "HEAD", "project[1].yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "right\n" {
		t.Fatalf("ReadAt = %q, want the literal path", got)
	}
}

func TestReadAtAllowsNewlineInPath(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	name := "project\nname.yaml"
	// Reading a tree must work even if its names cannot be checked out on
	// this platform. Construct the tree directly, without Windows file APIs.
	stageBlob(t, dir, "100644", name, "nl\n")
	gitRun(t, dir, "commit", "-m", "initial")
	if err := validateRelPath(name); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAt(dir, "HEAD", name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "nl\n" {
		t.Fatalf("ReadAt = %q", got)
	}
}

func TestStatusUnusualNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filesystems do not support control characters in filenames; porcelain parsing is covered by TestParseStatusKeepsTabCRLF")
	}
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "keep.txt", "ok\n")
	gitAddCommit(t, dir, "initial")
	// Staged, so porcelain lists the path itself. An untracked directory
	// would otherwise be reported as "dir/" and hide the tab in the name.
	tabName := "dir/a\tb.yaml"
	lfName := "notes\nfile.yaml"
	crName := "trail.yaml\r"
	writeFile(t, dir, tabName, "tab\n")
	writeFile(t, dir, lfName, "lf\n")
	writeFile(t, dir, crName, "cr\n")
	gitRun(t, dir, "add", "-A")
	st, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range st.Entries {
		got[e.Path] = true
	}
	if !got[tabName] || !got[lfName] || !got[crName] {
		t.Fatalf("entries: %+v", st.Entries)
	}
}

func TestReadAtNoCommit(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	_, err := ReadAt(dir, "HEAD", "project.yaml")
	if !IsRevision(err) {
		t.Fatalf("got %v", err)
	}
}

func skipNoGit(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "-c", "init.defaultBranch=main", "init")
	gitRun(t, dir, "config", "core.autocrlf", "false")
}

func stageBlob(t *testing.T, dir, mode, name, body string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hash fixture blob: %v\n%s", err, out)
	} else {
		gitRun(t, dir, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(string(out)), name)
	}
}

func gitAddCommit(t *testing.T, dir, message string) {
	t.Helper()
	gitRun(t, dir, "add", "--", ".")
	gitRun(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", message)
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = withIdentityEnv(os.Environ())
	return cmd.CombinedOutput()
}

// withIdentityEnv gives helper git commands an author even when a test has
// cleared the process identity to exercise Commit's refusal. The command
// under test still sees the process environment.
func withIdentityEnv(src []string) []string {
	drop := map[string]bool{
		"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
		"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
		"EMAIL": true,
	}
	out := make([]string, 0, len(src)+6)
	for _, e := range src {
		key, _, _ := strings.Cut(e, "=")
		if drop[key] {
			continue
		}
		out = append(out, e)
	}
	return append(out,
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
