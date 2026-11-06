package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPickGitLocale(t *testing.T) {
	none := func(string) bool { return false }
	if pickGitLocale(none, false) != "C" {
		t.Fatal("missing locale did not fall back to C")
	}
	if pickGitLocale(none, true) != "C.UTF-8" {
		t.Fatal("windows without locale(1) did not use C.UTF-8")
	}
	utf8 := func(name string) bool { return name == "C.utf8" }
	if pickGitLocale(utf8, false) != "C.UTF-8" {
		t.Fatal("C.utf8 was not selected")
	}
}

func TestCancelSucceededIgnoresInterrupt(t *testing.T) {
	if cancelSucceeded("os.Interrupt", nil) {
		t.Fatal("os.Interrupt nil error was treated as a delivered cancel")
	}
	if cancelSucceeded("os.Interrupt", errors.New("unsupported signal")) {
		t.Fatal("os.Interrupt error was treated as a delivered cancel")
	}
	if cancelSucceeded("ctrl-break", errors.New("not delivered")) {
		t.Fatal("failed ctrl-break was success")
	}
	if !cancelSucceeded("ctrl-break", nil) {
		t.Fatal("delivered ctrl-break was not success")
	}
}

func TestFallbackToPidKill(t *testing.T) {
	if fallbackToPidKill(true, false) {
		t.Fatal("missing process group fell back to killing the pid")
	}
	if fallbackToPidKill(false, true) {
		t.Fatal("exited process fell back to killing the pid")
	}
	if !fallbackToPidKill(false, false) {
		t.Fatal("live process whose group signal failed was not retried")
	}
}

func TestFilterDriverAndSafety(t *testing.T) {
	name, ok := filterDriver("filter.foo.bar.clean")
	if !ok || name != "foo.bar" {
		t.Fatalf("dotted driver %q %v", name, ok)
	}
	name, ok = filterDriver("filter.foo bar.smudge")
	if !ok || name != "foo bar" || !safeFilterName(name) {
		t.Fatalf("spaced driver %q %v", name, ok)
	}
	name, ok = filterDriver("filter.evil.clean")
	if !ok || name != "evil" {
		t.Fatalf("simple driver %q", name)
	}
	if _, ok := filterDriver("filter..clean"); ok {
		t.Fatal("empty driver was accepted")
	}
	names, err := parseFilterDrivers("filter.a=b.clean\n")
	if err == nil || names != nil {
		t.Fatalf("equals in the name was accepted: %v %v", names, err)
	}
	names, err = parseFilterDrivers("filter.foo.bar.clean\nfilter.foo.bar.smudge\nfilter.foo bar.process\n")
	if err != nil || strings.Join(names, ",") != "foo bar,foo.bar" {
		t.Fatalf("drivers %v %v", names, err)
	}
}

func TestFastForwardRefusalText(t *testing.T) {
	if !fastForwardRefusal("fatal: Not possible to fast-forward, aborting.") {
		t.Fatal("fast-forward refusal was missed")
	}
	if fastForwardRefusal("fatal: unable to access 'https://example.test/': Could not resolve host") {
		t.Fatal("network error was treated as divergence")
	}
	if fastForwardRefusal("error: Your local changes to the following files would be overwritten by merge:") {
		t.Fatal("local changes were treated as divergence")
	}
}

func TestDottedFilterIsBlanked(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	isolateGitConfig(t)
	dir := committedRepo(t)
	marker := filepath.Join(dir, "dotted-ran")
	script := filepath.Join(dir, "dotted.sh")
	writeFilterScript(t, script, marker)
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=foo.bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "filter.foo.bar.clean", shQuote(script))
	writeFile(t, dir, "f.txt", "two\n")
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("status ran filter.foo.bar")
	}
	args, err := filterOverrideArgs(dir, gitOpt{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "filter.foo.bar.clean=") {
		t.Fatalf("override missed the dotted driver:\n%s", joined)
	}
	if strings.Contains(joined, "filter.foo.clean=") && !strings.Contains(joined, "filter.foo.bar.clean=") {
		t.Fatal("override used only the text before the first dot")
	}
}

func TestGlobalFilterIsNotBlanked(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	global := isolateGitConfig(t)
	dir := committedRepo(t)
	localMark := filepath.Join(dir, "local-ran")
	globalMark := filepath.Join(dir, "global-ran")
	localScript := filepath.Join(dir, "local.sh")
	globalScript := filepath.Join(dir, "global.sh")
	writeFilterScript(t, localScript, localMark)
	writeFilterScript(t, globalScript, globalMark)
	gitRun(t, dir, "config", "--file", global, "filter.lfs.clean", shQuote(globalScript))
	writeFile(t, dir, "g.txt", "global\n")
	gitAddCommit(t, dir, "tracked")
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("f.txt filter=evil\ng.txt filter=lfs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "filter.evil.clean", shQuote(localScript))
	writeFile(t, dir, "f.txt", "two\n")
	writeFile(t, dir, "g.txt", "global-two\n")
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(localMark); !os.IsNotExist(err) {
		t.Fatal("status ran the repository-local filter")
	}
	kept, _, err := runGit(context.Background(), dir, gitOpt{readOnly: true}, "config", "--get", "filter.lfs.clean")
	if err != nil || strings.TrimSpace(string(kept)) != shQuote(globalScript) {
		t.Fatalf("global filter was blanked: %q %v", kept, err)
	}
	blanked, _, err := runGit(context.Background(), dir, gitOpt{readOnly: true}, "config", "--get", "filter.evil.clean")
	if err == nil && strings.TrimSpace(string(blanked)) != "" {
		t.Fatalf("local filter was not blanked: %q", blanked)
	}
	names, err := filterNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "lfs" {
			t.Fatalf("global filter was blanked: %v", names)
		}
	}
	if len(names) != 1 || names[0] != "evil" {
		t.Fatalf("local names %v", names)
	}
}

func TestSpaceFilterIsBlanked(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	isolateGitConfig(t)
	dir := committedRepo(t)
	script := filepath.Join(dir, "space.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "filter.foo bar.clean", shQuote(script))
	args, err := filterOverrideArgs(dir, gitOpt{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, key := range []string{"filter.foo bar.clean=", "filter.foo bar.smudge=", "filter.foo bar.process="} {
		if !strings.Contains(joined, key) {
			t.Fatalf("missing %s in\n%s", key, joined)
		}
	}
	out, _, err := runGit(context.Background(), dir, gitOpt{readOnly: true}, "config", "--get", "filter.foo bar.clean")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("space filter was not blanked: %q", out)
	}
}

func TestUnsafeFilterFailsClosed(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	isolateGitConfig(t)
	dir := committedRepo(t)
	marker := filepath.Join(dir, "unsafe-ran")
	script := filepath.Join(dir, "unsafe.sh")
	writeFilterScript(t, script, marker)
	gitRun(t, dir, "config", "filter.a=b.clean", shQuote(script))
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=a=b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f.txt", "two\n")
	if _, err := Status(dir); err == nil {
		t.Fatal("status ran with a filter name that cannot be disabled")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsafe filter ran")
	}
}

func TestWorktreeFilterIsBlanked(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	isolateGitConfig(t)
	dir := committedRepo(t)
	marker := filepath.Join(dir, "worktree-ran")
	script := filepath.Join(dir, "worktree.sh")
	writeFilterScript(t, script, marker)
	gitRun(t, dir, "config", "extensions.worktreeConfig", "true")
	gitRun(t, dir, "config", "--worktree", "filter.onlywt.clean", script)
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=onlywt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f.txt", "two\n")
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("status ran the worktree filter")
	}
	names, err := filterNames(dir)
	if err != nil || len(names) != 1 || names[0] != "onlywt" {
		t.Fatalf("names %v %v", names, err)
	}
}

func TestCommitRecordsIndexBlobNotWorktree(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	writeFile(t, dir, "notes.txt", "staged aside\n")
	gitRun(t, dir, "add", "--", "notes.txt")
	writeFile(t, dir, "f.txt", "scanned\n")
	if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f.txt", "token literal-after-scan\n")
	if _, err := Commit(context.Background(), dir, "keep-scanned", []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	got := gitRun(t, dir, "show", "HEAD:f.txt")
	if got != "scanned\n" {
		t.Fatalf("commit recorded %q", got)
	}
	cached := gitRun(t, dir, "diff", "--cached", "--name-only")
	if strings.Contains(cached, "f.txt") {
		t.Fatalf("committed path stayed staged:\n%s", cached)
	}
	if !strings.Contains(cached, "notes.txt") {
		t.Fatalf("other staged path was dropped:\n%s", cached)
	}
	body := string(mustRead(t, filepath.Join(dir, "f.txt")))
	if !strings.Contains(body, "literal-after-scan") {
		t.Fatalf("worktree was rewritten: %q", body)
	}
}

func TestRestoreIndexIgnoresCancel(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	before, err := SnapshotIndex(context.Background(), dir, []string{"f.txt"})
	if err != nil || len(before) != 1 {
		t.Fatalf("snapshot %v %v", before, err)
	}
	writeFile(t, dir, "f.txt", "replacement\n")
	if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RestoreIndex(ctx, dir, []string{"f.txt"}, before); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(gitRun(t, dir, "rev-parse", ":f.txt"))
	want := oidFromIndexLine(t, before[0].Line)
	if got != want {
		t.Fatalf("index oid %s, snapshot %s", got, want)
	}
}

func TestRestoreIndexRetriesIndexLock(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	before, err := SnapshotIndex(context.Background(), dir, []string{"f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f.txt", "replacement\n")
	if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	lock := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--git-path", "index.lock"))
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(dir, lock)
	}
	if err := os.WriteFile(lock, []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RestoreIndex(context.Background(), dir, []string{"f.txt"}, before)
	}()
	time.Sleep(150 * time.Millisecond)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("restore did not retry past index.lock")
	}
	got := strings.TrimSpace(gitRun(t, dir, "rev-parse", ":f.txt"))
	if got != oidFromIndexLine(t, before[0].Line) {
		t.Fatalf("index oid %s", got)
	}
}

func TestRestoreUnmergedClearsStage0(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	gitRun(t, dir, "checkout", "-b", "side")
	writeFile(t, dir, "f.txt", "side\n")
	gitAddCommit(t, dir, "side")
	gitRun(t, dir, "checkout", "main")
	writeFile(t, dir, "f.txt", "main\n")
	gitAddCommit(t, dir, "main")
	merge := exec.Command("git", "merge", "--no-commit", "--no-ff", "side")
	merge.Dir = dir
	merge.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com", "LC_ALL=C")
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("expected a conflict:\n%s", out)
	}
	before, err := SnapshotIndex(context.Background(), dir, []string{"f.txt"})
	if err != nil || len(before) < 2 {
		t.Fatalf("unmerged snapshot %v %v", before, err)
	}
	writeFile(t, dir, "f.txt", "token literal-key\n")
	if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := RestoreIndex(context.Background(), dir, []string{"f.txt"}, before); err != nil {
		t.Fatal(err)
	}
	listed := gitRun(t, dir, "ls-files", "-s", "--", "f.txt")
	if strings.Contains(listed, " 0\t") {
		t.Fatalf("stage 0 remained:\n%s", listed)
	}
	if !strings.Contains(listed, " 1\t") || !strings.Contains(listed, " 2\t") || !strings.Contains(listed, " 3\t") {
		t.Fatalf("stages were not restored:\n%s", listed)
	}
}

func TestPushUsesConfiguredRemoteNotShortName(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	real := t.TempDir()
	decoy := t.TempDir()
	gitRun(t, real, "init", "--bare", "-b", "main")
	gitRun(t, decoy, "init", "--bare", "-b", "main")
	local := committedRepo(t)
	gitRun(t, local, "remote", "add", "forks/upstream", fileURL(real))
	// Newer git (2.56 on CI) refuses a remote "forks" next to
	// "forks/upstream", so the ambiguous short name cannot arise there.
	// Check the decoy only where git still allows it.
	withDecoy := exec.Command("git", "-C", local, "remote", "add", "forks", fileURL(decoy)).Run() == nil
	gitRun(t, local, "push", "-u", "forks/upstream", "HEAD")
	beforeDecoy := ""
	if withDecoy {
		gitRun(t, local, "push", "forks", "HEAD:refs/heads/upstream/main")
		beforeDecoy = strings.TrimSpace(gitRun(t, decoy, "rev-parse", "refs/heads/upstream/main"))
	}
	beforeReal := strings.TrimSpace(gitRun(t, real, "rev-parse", "refs/heads/main"))
	writeFile(t, local, "f.txt", "tracked\n")
	gitAddCommit(t, local, "tracked")
	remote, branch, upstream, err := tracking(context.Background(), local)
	if err != nil || remote != "forks/upstream" || branch != "main" || upstream != "main" {
		t.Fatalf("tracking remote=%s branch=%s upstream=%s err=%v", remote, branch, upstream, err)
	}
	if _, err := Push(context.Background(), local, nil); err != nil {
		t.Fatal(err)
	}
	gotReal := strings.TrimSpace(gitRun(t, real, "rev-parse", "refs/heads/main"))
	if gotReal == beforeReal {
		t.Fatal("push did not update the remote named forks/upstream")
	}
	if withDecoy {
		if gotDecoy := strings.TrimSpace(gitRun(t, decoy, "rev-parse", "refs/heads/upstream/main")); gotDecoy != beforeDecoy {
			t.Fatal("push updated forks's branch upstream/main")
		}
	}
}

func TestPullNetworkErrorIsNotDiverged(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	_, a, b := twoClones(t)
	writeFile(t, a, "f.txt", "from-a\n")
	gitAddCommit(t, a, "from-a")
	if _, err := Push(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(context.Background(), b, nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "f.txt", "from-b\n")
	gitAddCommit(t, b, "from-b")
	missing := filepath.Join(t.TempDir(), "missing.git")
	gitRun(t, b, "remote", "set-url", "origin", fileURL(missing))
	out, err := Pull(context.Background(), b, PullFFOnly, nil)
	if err == nil {
		t.Fatalf("unreachable pull returned status %s", out.Status)
	}
	if out != nil && out.Status == "diverged" {
		t.Fatal("network failure was reported as diverged")
	}
}

func TestPullDirtyWorktreeIsNotDiverged(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	_, a, b := twoClones(t)
	writeFile(t, a, "f.txt", "from-a\n")
	gitAddCommit(t, a, "from-a")
	if _, err := Push(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "f.txt", "dirty local\n")
	out, err := Pull(context.Background(), b, PullFFOnly, nil)
	if err == nil {
		t.Fatalf("dirty pull returned %+v", out)
	}
	if out != nil && out.Status == "diverged" {
		t.Fatal("local changes were reported as diverged")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "local changes") && !strings.Contains(strings.ToLower(err.Error()), "overwritten") {
		t.Fatalf("expected a local-changes error, got %v", err)
	}
}

func writeFilterScript(t *testing.T, script, marker string) {
	t.Helper()
	body := "#!/bin/sh\ntouch " + shQuote(marker) + "\ncat\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func oidFromIndexLine(t *testing.T, line string) string {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("index line %q", line)
	}
	return fields[1]
}

func TestPullDisablesRepositoryMergeDriver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell payload fixture")
	}
	skipNoGit(t)
	useIdentity(t)
	_, a, b := twoClones(t)
	writeFile(t, a, ".gitattributes", "f.txt merge=hostile\n")
	gitAddCommit(t, a, "attributes")
	gitRun(t, a, "push")
	gitRun(t, b, "pull", "--ff-only")
	writeFile(t, a, "f.txt", "remote side\n")
	gitAddCommit(t, a, "remote")
	gitRun(t, a, "push")
	writeFile(t, b, "f.txt", "local side\n")
	gitAddCommit(t, b, "local")
	marker := filepath.Join(b, ".git", "merge-ran")
	script := filepath.Join(b, ".git", "merge-payload")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+shQuote(marker)+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, b, "config", "merge.hostile.driver", script+" %O %A %B")
	out, err := Pull(context.Background(), b, PullFFOnly, nil)
	if err != nil || out.Status != "diverged" {
		t.Fatalf("ff refusal: %+v, %v", out, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ff-only pull ran payload: %v", err)
	}
	out, err = Pull(context.Background(), b, PullMerge, nil)
	if err != nil || out.Status != "conflict" {
		t.Fatalf("merge: %+v, %v", out, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pull merge ran payload: %v", err)
	}
	if got := string(mustRead(t, filepath.Join(b, "f.txt"))); !strings.Contains(got, "<<<<<<<") {
		t.Fatalf("text conflict markers missing: %q", got)
	}
}

func TestUnsafeMergeDriverIsRefused(t *testing.T) {
	skipNoGit(t)
	dir := committedRepo(t)
	gitRun(t, dir, "config", "merge.a=b.driver", "false")
	if _, err := mergeDriverNames(dir); err == nil {
		t.Fatal("unsafe merge subsection accepted")
	}
	if _, err := Status(dir); err == nil {
		t.Fatal("status ran without disabling unsafe driver")
	}
}
