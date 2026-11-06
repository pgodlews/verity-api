package vcs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

func TestParseTrack(t *testing.T) {
	cases := []struct {
		in            string
		ahead, behind int
	}{
		{"", 0, 0},
		{"[gone]", 0, 0},
		{"[ahead 2]", 2, 0},
		{"[behind 3]", 0, 3},
		{"[ahead 1, behind 4]", 1, 4},
	}
	for _, tc := range cases {
		ahead, behind, gone := parseTrack(tc.in)
		if ahead != tc.ahead || behind != tc.behind {
			t.Fatalf("%q: ahead %d behind %d", tc.in, ahead, behind)
		}
		if gone != (strings.Trim(tc.in, "[] ") == "gone") {
			t.Fatalf("%q: gone %v", tc.in, gone)
		}
	}
}

func TestRedactGitText(t *testing.T) {
	got := redactGit("fatal: https://user:s3cret@h/r.git")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "user:") {
		t.Fatalf("leaked: %q", got)
	}
	if !strings.Contains(got, secrets.Mask) {
		t.Fatalf("mask missing: %q", got)
	}
	query := redactGit("fatal: https://h/r.git?token=querysecret")
	if strings.Contains(query, "querysecret") {
		t.Fatalf("query token leaked: %q", query)
	}
	header := redactGit("http.extraheader=Authorization: Bearer headersecret")
	if strings.Contains(header, "headersecret") {
		t.Fatalf("extraHeader leaked: %q", header)
	}
}

func TestConfigArgsLeavesUserSSHAndCredentials(t *testing.T) {
	raw := configArgs(t.TempDir())
	var flags []string
	for i := 0; i < len(raw)-1; i++ {
		if raw[i] == "-c" {
			flags = append(flags, raw[i+1])
		}
	}
	if strings.Join(flags, ",") != "core.quotepath=false,core.fsmonitor=false,log.showSignature=false" {
		t.Fatalf("config flags %v", flags)
	}
	t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
	env := gitEnv(t.TempDir(), gitOpt{terminalPrompt: true, skipFilters: true})
	found := false
	for _, e := range env {
		if e == "GIT_SSH_COMMAND=ssh -o BatchMode=yes" {
			found = true
		}
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") && e != "GIT_SSH_COMMAND=ssh -o BatchMode=yes" {
			t.Fatalf("ssh command replaced: %s", e)
		}
	}
	if !found {
		t.Fatal("GIT_SSH_COMMAND was dropped")
	}
}

func TestCommitDoesNotRunFsmonitor(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "f.txt", "one\n")
	gitAddCommit(t, dir, "init")
	marker := filepath.Join(dir, "fsmonitor-ran")
	script := filepath.Join(dir, "fsmonitor.sh")
	body := "#!/bin/sh\ntouch " + shQuote(marker) + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "core.fsmonitor", shQuote(script))
	plain := exec.Command("git", "-C", dir, "status", "--porcelain=v2")
	plain.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	_, _ = plain.CombinedOutput()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture hook did not run: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f.txt", "two\n")
	if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stage ran the fsmonitor hook: %v", err)
	}
	if _, err := Commit(context.Background(), dir, "second", []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("commit ran the fsmonitor hook: %v", err)
	}
}

func TestWriteEnvDisablesTerminalPrompt(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	env := gitEnv(t.TempDir(), gitOpt{terminalPrompt: true, skipFilters: true, skipSSH: true})
	saw := ""
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") {
			if saw != "" {
				t.Fatalf("duplicate prompt env: %s and %s", saw, e)
			}
			saw = e
		}
	}
	if saw != "GIT_TERMINAL_PROMPT=0" {
		t.Fatalf("prompt env %q", saw)
	}
	if got := envValue(env, "LC_ALL"); got != gitLocale() {
		t.Fatalf("LC_ALL %q", got)
	}
	if got := envValue(env, "LC_ALL"); got != "C" && got != "C.UTF-8" {
		t.Fatalf("LC_ALL %q", got)
	}
}

func TestStageUnstageDoesNotAddEverything(t *testing.T) {
	skipNoGit(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "keep.txt", "a\n")
	gitAddCommit(t, dir, "init")
	writeFile(t, dir, "keep.txt", "b\n")
	writeFile(t, dir, "other.txt", "no\n")
	writeFile(t, dir, "-dash.txt", "d\n")

	if err := Stage(context.Background(), dir, []string{"keep.txt", "-dash.txt"}); err != nil {
		t.Fatal(err)
	}
	st, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range st.Entries {
		got[e.Path] = e
	}
	if got["keep.txt"].Index != "M" {
		t.Fatalf("keep not staged: %+v", got["keep.txt"])
	}
	if got["-dash.txt"].Index != "A" {
		t.Fatalf("dash path not staged: %+v", got["-dash.txt"])
	}
	if !got["other.txt"].Untracked {
		t.Fatalf("other file was staged: %+v", got["other.txt"])
	}
	if err := Unstage(context.Background(), dir, []string{"keep.txt"}); err != nil {
		t.Fatal(err)
	}
	st, err = Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range st.Entries {
		if e.Path == "keep.txt" && e.Index == "M" {
			t.Fatalf("keep still staged: %+v", e)
		}
	}
	if err := Stage(context.Background(), dir, nil); err == nil {
		t.Fatal("empty path list was accepted")
	}
}

func TestCommitIdentityAndHook(t *testing.T) {
	skipNoGit(t)
	t.Run("empty message", func(t *testing.T) {
		dir := committedRepo(t)
		if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := Commit(context.Background(), dir, "  \n", nil); !IsEmptyMessage(err) {
			t.Fatalf("got %v", err)
		}
		if head(t, dir) != "init" {
			t.Fatal("empty message created a commit")
		}
	})
	t.Run("with identity", func(t *testing.T) {
		useIdentity(t)
		dir := committedRepo(t)
		writeFile(t, dir, "f.txt", "two\n")
		if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
			t.Fatal(err)
		}
		out, err := Commit(context.Background(), dir, "second", []string{"f.txt"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "second") {
			t.Fatalf("output %q", out)
		}
		if head(t, dir) != "second" {
			t.Fatalf("head %q", head(t, dir))
		}
	})
	t.Run("without identity", func(t *testing.T) {
		clearIdentity(t)
		dir := committedRepo(t)
		writeFile(t, dir, "f.txt", "two\n")
		if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
			t.Fatal(err)
		}
		_, err := Commit(context.Background(), dir, "nope", []string{"f.txt"})
		if !IsIdentity(err) {
			t.Fatalf("got %v", err)
		}
		if strings.Contains(err.Error(), "@") || strings.Contains(err.Error(), "auto-detect") {
			t.Fatalf("identity error passed git's address through: %s", err)
		}
		if !strings.Contains(err.Error(), "git config user.name") {
			t.Fatalf("hint missing: %s", err)
		}
		if head(t, dir) != "init" {
			t.Fatal("commit recorded without an identity")
		}
	})
	t.Run("hook output", func(t *testing.T) {
		useIdentity(t)
		dir := committedRepo(t)
		hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
		if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\necho hook-says-no\nexit 1\n"
		if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "config", "core.hooksPath", filepath.Join(dir, ".git", "hooks"))
		writeFile(t, dir, "f.txt", "two\n")
		if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
			t.Fatal(err)
		}
		_, err := Commit(context.Background(), dir, "blocked", []string{"f.txt"})
		if err == nil || !strings.Contains(err.Error(), "hook-says-no") {
			t.Fatalf("hook output missing: %v", err)
		}
		if head(t, dir) != "init" {
			t.Fatal("hook-refused commit was recorded")
		}
	})
}

func TestFetchPullPushAndBranches(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	bare, a, b := twoClones(t)

	writeFile(t, a, "f.txt", "from-a\n")
	gitAddCommit(t, a, "from-a")
	if _, err := Push(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	var lines []string
	if _, err := Fetch(context.Background(), b, func(line string) { lines = append(lines, line) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("fetch reported no progress")
	}
	out, err := Pull(context.Background(), b, PullFFOnly, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "updated" {
		t.Fatalf("ff pull: %+v", out)
	}
	body, err := os.ReadFile(filepath.Join(b, "f.txt"))
	if err != nil || string(body) != "from-a\n" {
		t.Fatalf("ff body %q %v", body, err)
	}

	writeFile(t, a, "f.txt", "a-side\n")
	gitAddCommit(t, a, "a-side")
	if _, err := Push(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "f.txt", "b-side\n")
	gitAddCommit(t, b, "b-side")
	diverged, err := Pull(context.Background(), b, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if diverged.Status != "diverged" {
		t.Fatalf("diverged: %+v", diverged)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(b, "f.txt"))), "<<<<<<<") {
		t.Fatal("ff-only pull changed the file into a conflict")
	}
	merged, err := Pull(context.Background(), b, PullMerge, nil)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Status != "conflict" || len(merged.Conflicted) != 1 || merged.Conflicted[0] != "f.txt" {
		t.Fatalf("conflict: %+v", merged)
	}
	files, err := ConflictedFiles(context.Background(), b)
	if err != nil || len(files) != 1 || files[0] != "f.txt" {
		t.Fatalf("conflicted files: %v %v", files, err)
	}
	if _, err := MergeAbort(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	files, err = ConflictedFiles(context.Background(), b)
	if err != nil || len(files) != 0 {
		t.Fatalf("after abort: %v %v", files, err)
	}
	again, err := Pull(context.Background(), b, PullFFOnly, nil)
	if err != nil || again.Status != "diverged" {
		t.Fatalf("still diverged: %+v %v", again, err)
	}

	feature := t.TempDir()
	gitInit(t, feature)
	writeFile(t, feature, "f.txt", "feat\n")
	gitAddCommit(t, feature, "feat")
	fresh := t.TempDir()
	gitRun(t, fresh, "init", "--bare", "-b", "main")
	gitRun(t, feature, "remote", "add", "origin", fileURL(fresh))
	pushed, err := Push(context.Background(), feature, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pushed.Status != "no-upstream" || pushed.Remote != "origin" || pushed.Branch == "" {
		t.Fatalf("no upstream: %+v", pushed)
	}
	if _, err := PushSetUpstream(context.Background(), feature, pushed.Remote, pushed.Branch, nil); err != nil {
		t.Fatal(err)
	}
	st, err := Status(feature)
	if err != nil || !st.Tracking || st.Upstream == "" {
		t.Fatalf("upstream not set: %+v %v", st, err)
	}
	writeFile(t, feature, "g.txt", "more\n")
	gitAddCommit(t, feature, "more")
	againPush, err := Push(context.Background(), feature, nil)
	if err != nil || againPush.Status != "pushed" {
		t.Fatalf("second push: %+v %v", againPush, err)
	}

	if _, err := CreateBranch(context.Background(), a, "bad..name", false); err == nil {
		t.Fatal("invalid branch was created")
	}
	if _, err := CreateBranch(context.Background(), a, "feature", false); err != nil {
		t.Fatal(err)
	}
	list, err := Branches(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	var found, current string
	for _, b := range list {
		if b.Name == "feature" {
			found = b.Name
			if b.Current {
				t.Fatal("new branch should not be current")
			}
		}
		if b.Current {
			current = b.Name
		}
	}
	if found != "feature" || current == "" || current == "feature" {
		t.Fatalf("branches %+v", list)
	}
	if _, err := Switch(context.Background(), a, "feature"); err != nil {
		t.Fatal(err)
	}
	st, err = Status(a)
	if err != nil || st.Branch != "feature" {
		t.Fatalf("switch: %+v %v", st, err)
	}
	if _, err := CreateBranch(context.Background(), a, "topic", true); err != nil {
		t.Fatal(err)
	}
	st, err = Status(a)
	if err != nil || st.Branch != "topic" {
		t.Fatalf("create and switch: %+v %v", st, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, b, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled fetch: %v", err)
	}
	_ = bare
}

func TestFetchAuthFailsFast(t *testing.T) {
	skipNoGit(t)
	clearIdentity(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "auth", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "f.txt", "one\n")
	gitAddCommit(t, dir, "init")
	trackFileRemote(t, dir)
	remote := srv.URL + "/repo.git"
	gitRun(t, dir, "remote", "set-url", "origin", remote)
	gitRun(t, dir, "config", "http.sslVerify", "false")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Fetch(ctx, dir, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("fetch succeeded against an auth challenge")
	}
	if errors.Is(err, context.DeadlineExceeded) || elapsed > 4*time.Second {
		t.Fatalf("fetch hung (%s): %v", elapsed, err)
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "terminal prompts disabled") && !strings.Contains(msg, "could not read") && !strings.Contains(msg, "authentication") {
		t.Fatalf("expected a fast auth failure, got %v", err)
	}
	if strings.Contains(err.Error(), "user:s3cret") {
		t.Fatal("redaction check fixture leaked")
	}
}

func TestFetchRedactsQueryTokenAndExtraHeader(t *testing.T) {
	skipNoGit(t)
	clearIdentity(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	t.Setenv("GIT_CURL_VERBOSE", "1")
	const querySecret = "querysecret"
	const headerSecret = "headersecret"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "f.txt", "one\n")
	gitAddCommit(t, dir, "init")
	trackFileRemote(t, dir)
	remote := srv.URL + "/repo.git?token=" + querySecret
	gitRun(t, dir, "remote", "set-url", "origin", remote)
	gitRun(t, dir, "config", "http.sslVerify", "false")
	gitRun(t, dir, "config", "http.extraHeader", "Authorization: Bearer "+headerSecret)

	var lines []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Fetch(ctx, dir, func(line string) { lines = append(lines, line) })
	if err == nil {
		t.Fatal("fetch succeeded")
	}
	blob := err.Error() + "\n" + strings.Join(lines, "\n")
	if strings.Contains(blob, querySecret) || strings.Contains(blob, headerSecret) {
		t.Fatalf("credential leaked in git text:\n%s", blob)
	}
}

func committedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "f.txt", "one\n")
	gitAddCommit(t, dir, "init")
	return dir
}

func twoClones(t *testing.T) (bare, a, b string) {
	t.Helper()
	bare = t.TempDir()
	gitRun(t, bare, "init", "--bare", "-b", "main")
	a = t.TempDir()
	gitInit(t, a)
	writeFile(t, a, "f.txt", "one\n")
	gitAddCommit(t, a, "init")
	gitRun(t, a, "remote", "add", "origin", fileURL(bare))
	gitRun(t, a, "push", "-u", "origin", "main")
	b = t.TempDir()
	gitRun(t, "", "clone", fileURL(bare), b)
	return bare, a, b
}

func shQuote(s string) string {
	s = filepath.ToSlash(s)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// trackFileRemote pushes to a local bare remote and sets upstream, so
// @{upstream} resolves. Callers can then point origin at another URL.
func trackFileRemote(t *testing.T, dir string) {
	t.Helper()
	bare := t.TempDir()
	gitRun(t, bare, "init", "--bare", "-b", "main")
	gitRun(t, dir, "remote", "add", "origin", fileURL(bare))
	gitRun(t, dir, "push", "-u", "origin", "HEAD")
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitRun(t, dir, "log", "-1", "--format=%s"))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func useIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func clearIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "no-global-gitconfig"))
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
	t.Setenv("EMAIL", "")
}

func isolateGitConfig(t *testing.T) string {
	t.Helper()
	global := filepath.Join(t.TempDir(), "global.gitconfig")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	return global
}

func TestStatusSkipsCleanFilterButAddRunsIt(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	isolateGitConfig(t)
	dir := committedRepo(t)
	marker := filepath.Join(dir, "filter-ran")
	script := filepath.Join(dir, "filter.sh")
	body := "#!/bin/sh\ntouch " + shQuote(marker) + "\ncat\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "filter.evil.clean", shQuote(script))
	writeFile(t, dir, "f.txt", "two\n")
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("status ran the clean filter: %v", err)
	}
	plain := exec.Command("git", "-C", dir, "status", "--porcelain=v2")
	plain.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	_, _ = plain.CombinedOutput()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture filter did not run for plain git status: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := Stage(context.Background(), dir, []string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("add did not run the user's clean filter")
	}
	names, err := filterNames(dir)
	if err != nil || len(names) != 1 || names[0] != "evil" {
		t.Fatalf("filter names %v %v", names, err)
	}
	args, err := filterOverrideArgs(dir, gitOpt{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "filter.evil.clean=") || !strings.Contains(joined, "filter.evil.smudge=") || !strings.Contains(joined, "filter.evil.process=") {
		t.Fatalf("overrides %s", joined)
	}
}

func TestStageLiteralBracketPath(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	writeFile(t, dir, "project[1].yaml", "one\n")
	writeFile(t, dir, "project1.yaml", "other\n")
	if err := Stage(context.Background(), dir, []string{"project[1].yaml"}); err != nil {
		t.Fatal(err)
	}
	listed := gitRun(t, dir, "ls-files")
	if strings.Contains(listed, "project1.yaml") {
		t.Fatalf("glob staged the other file:\n%s", listed)
	}
	if !strings.Contains(listed, "project[1].yaml") {
		t.Fatalf("literal path missing:\n%s", listed)
	}
}

func TestPlusBranchDoesNotForcePushMain(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	bare, local, _ := twoClones(t)
	before := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/main"))
	if _, err := CheckBranch(context.Background(), local, "+main"); err == nil {
		t.Fatal("+main was accepted as a branch name")
	}
	gitRun(t, local, "checkout", "-b", "+main")
	writeFile(t, local, "f.txt", "plus\n")
	gitAddCommit(t, local, "plus")
	if _, err := PushSetUpstream(context.Background(), local, "origin", "+main", nil); err == nil {
		t.Fatal("push of +main was accepted")
	}
	after := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/main"))
	if after != before {
		t.Fatalf("main moved from %s to %s", before, after)
	}
	gitRun(t, local, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	gitRun(t, local, "checkout", "main")
	writeFile(t, local, "f.txt", "local-main\n")
	gitAddCommit(t, local, "local-main")
	if _, err := Push(context.Background(), local, nil); err != nil {
		t.Fatal(err)
	}
	featureTip := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/main"))
	if featureTip == before {
		t.Fatal("the current branch was not pushed")
	}
	if _, err := gitCmd(bare, "rev-parse", "--verify", "refs/heads/+main"); err == nil {
		t.Fatal("rejected branch was created on the remote")
	}
}

func TestPushIgnoresRemotePushConfig(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	bare, local, _ := twoClones(t)
	main := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/main"))
	gitRun(t, local, "checkout", "-b", "feature")
	writeFile(t, local, "f.txt", "feature\n")
	gitAddCommit(t, local, "feature")
	if _, err := PushSetUpstream(context.Background(), local, "origin", "feature", nil); err != nil {
		t.Fatal(err)
	}
	gitRun(t, local, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	gitRun(t, local, "config", "push.followTags", "true")
	writeFile(t, local, "f.txt", "feature-2\n")
	gitAddCommit(t, local, "feature-2")
	if _, err := Push(context.Background(), local, nil); err != nil {
		t.Fatal(err)
	}
	gotMain := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/main"))
	if gotMain != main {
		t.Fatal("push updated main through remote.origin.push")
	}
	gotFeature := strings.TrimSpace(gitRun(t, bare, "rev-parse", "refs/heads/feature"))
	localFeature := strings.TrimSpace(gitRun(t, local, "rev-parse", "HEAD"))
	if gotFeature != localFeature {
		t.Fatalf("feature %s remote %s", localFeature, gotFeature)
	}
}

func TestStatusReportsMergeAndRebase(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	head := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	mergeHead := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--git-path", "MERGE_HEAD"))
	if !filepath.IsAbs(mergeHead) {
		mergeHead = filepath.Join(dir, mergeHead)
	}
	if err := os.WriteFile(mergeHead, []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Merging || st.Rebasing {
		t.Fatalf("merging=%v rebasing=%v", st.Merging, st.Rebasing)
	}
	if err := os.Remove(mergeHead); err != nil {
		t.Fatal(err)
	}
	rebase := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--git-path", "rebase-merge"))
	if !filepath.IsAbs(rebase) {
		rebase = filepath.Join(dir, rebase)
	}
	if err := os.Mkdir(rebase, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err = Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Merging || !st.Rebasing {
		t.Fatalf("merging=%v rebasing=%v", st.Merging, st.Rebasing)
	}
}

func TestPullDoesNotOpenAnEditor(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	_, a, b := twoClones(t)
	marker := filepath.Join(b, "editor-ran")
	script := filepath.Join(b, "editor.sh")
	body := "#!/bin/sh\ntouch " + shQuote(marker) + "\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, b, "config", "core.editor", script)
	writeFile(t, a, "f.txt", "a-side\n")
	gitAddCommit(t, a, "a-side")
	if _, err := Push(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "f.txt", "b-side\n")
	gitAddCommit(t, b, "b-side")
	out, err := Pull(context.Background(), b, PullMerge, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "conflict" {
		t.Fatalf("status %s", out.Status)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("pull opened the editor")
	}
}

func TestCancelFetchLeavesNoIndexLock(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	trackFileRemote(t, dir)
	marker := filepath.Join(t.TempDir(), "fetch-started")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = os.WriteFile(marker, []byte("x"), 0o644)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	gitRun(t, dir, "remote", "set-url", "origin", srv.URL+"/repo.git")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Fetch(ctx, dir, nil)
		done <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("fetch did not reach the server")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("fetch: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled fetch did not return")
	}
	lock := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--git-path", "index.lock"))
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(dir, lock)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("index.lock left behind: %v", err)
	}
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
}

func TestCancelStageRemovesIndexLock(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	dir := committedRepo(t)
	marker := filepath.Join(dir, "stage-started")
	script := filepath.Join(dir, "slow-clean.sh")
	body := "#!/bin/sh\ntouch " + shQuote(marker) + "\nsleep 60\ncat\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("* filter=slow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "filter.slow.clean", shQuote(script))
	writeFile(t, dir, "f.txt", "two\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Stage(ctx, dir, []string{"f.txt"})
	}()
	deadline := time.Now().Add(8 * time.Second)
	lock := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--git-path", "index.lock"))
	if !filepath.IsAbs(lock) {
		lock = filepath.Join(dir, lock)
	}
	for {
		_, started := os.Stat(marker)
		_, locked := os.Stat(lock)
		if started == nil && locked == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("stage did not take index.lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled stage did not return")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("index.lock left behind: %v", err)
	}
	if _, err := Status(dir); err != nil {
		t.Fatal(err)
	}
}

func TestSSHBatchModeWhenUnset(t *testing.T) {
	skipNoGit(t)
	t.Setenv("GIT_SSH_COMMAND", "")
	os.Unsetenv("GIT_SSH_COMMAND")
	dir := committedRepo(t)
	env := gitEnv(dir, gitOpt{terminalPrompt: true, skipFilters: true})
	got := envValue(env, "GIT_SSH_COMMAND")
	if got != "ssh -o BatchMode=yes" {
		t.Fatalf("ssh command %q", got)
	}
	if strings.Contains(got, "accept-new") {
		t.Fatal("accept-new was set")
	}
	gitRun(t, dir, "config", "core.sshCommand", "ssh -o BatchMode=no")
	env = gitEnv(dir, gitOpt{terminalPrompt: true, skipFilters: true})
	if envValue(env, "GIT_SSH_COMMAND") != "" {
		t.Fatalf("core.sshCommand was replaced: %q", envValue(env, "GIT_SSH_COMMAND"))
	}
	t.Setenv("GIT_SSH_COMMAND", "ssh -o IdentityFile=key")
	env = gitEnv(dir, gitOpt{terminalPrompt: true, skipFilters: true})
	if envValue(env, "GIT_SSH_COMMAND") != "ssh -o IdentityFile=key" {
		t.Fatalf("user command replaced: %q", envValue(env, "GIT_SSH_COMMAND"))
	}
}

func TestUpstreamGoneNoPush(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	bare, local, _ := twoClones(t)
	gitRun(t, bare, "update-ref", "-d", "refs/heads/main")
	gitRun(t, local, "update-ref", "-d", "refs/remotes/origin/main")
	st, err := Status(local)
	if err != nil {
		t.Fatal(err)
	}
	if !st.UpstreamGone || st.Tracking || st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("status tracking=%v gone=%v ahead=%d behind=%d", st.Tracking, st.UpstreamGone, st.Ahead, st.Behind)
	}
	out, err := Push(context.Background(), local, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "no-upstream" {
		t.Fatalf("status %s", out.Status)
	}
	if _, err := gitCmd(bare, "rev-parse", "--verify", "refs/heads/main"); err == nil {
		t.Fatal("push recreated the deleted remote branch")
	}
	if _, err := Fetch(context.Background(), local, nil); !IsNoUpstream(err) {
		t.Fatalf("fetch: %v", err)
	}
}

func TestFetchDoesNotMoveLocalBranches(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	_, a, b := twoClones(t)
	base := strings.TrimSpace(gitRun(t, b, "rev-parse", "HEAD"))
	gitRun(t, b, "checkout", "-b", "other")
	gitRun(t, b, "checkout", "main")
	gitRun(t, a, "checkout", "-b", "other")
	writeFile(t, a, "f.txt", "other-moved\n")
	gitAddCommit(t, a, "other-moved")
	if _, err := PushSetUpstream(context.Background(), a, "origin", "other", nil); err != nil {
		t.Fatal(err)
	}
	gitRun(t, b, "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	if _, err := Fetch(context.Background(), b, nil); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(gitRun(t, b, "rev-parse", "other"))
	if got != base {
		t.Fatalf("fetch moved local other from %s to %s", base, got)
	}
}

func TestPullAlreadyMergingIsNotANewConflict(t *testing.T) {
	skipNoGit(t)
	useIdentity(t)
	_, a, b := twoClones(t)
	writeFile(t, a, "f.txt", "a-side\n")
	gitAddCommit(t, a, "a-side")
	if _, err := Push(context.Background(), a, nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b, "f.txt", "b-side\n")
	gitAddCommit(t, b, "b-side")
	gitRun(t, b, "fetch", "origin")
	merge := exec.Command("git", "merge", "--no-edit", "--no-commit", "origin/main")
	merge.Dir = b
	merge.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com", "LC_ALL=C")
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("expected a conflicted merge to stay open:\n%s", out)
	}
	_, err := Pull(context.Background(), b, PullMerge, nil)
	var pending *MergePendingError
	if !errors.As(err, &pending) {
		t.Fatalf("got %v", err)
	}
	if len(pending.Files) == 0 {
		t.Fatal("unmerged files were not listed")
	}
}
