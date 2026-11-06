package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

const cleanProject = `schemaVersion: "1.0"
name: Demo
suites:
  - name: Smoke
    cases:
      - name: widgets
        steps:
          - name: list
            protocol: rest
            request:
              method: GET
              url: "${BASE_URL}/health"
`

const leakyProjectBody = `schemaVersion: "1.0"
name: Demo
suites:
  - name: Smoke
    cases:
      - name: widgets
        steps:
          - name: list
            protocol: rest
            request:
              method: GET
              url: "${BASE_URL}/health"
              headers:
                X-Api-Key: literal-key
`

func TestCommitScansStagedSecrets(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "init")

	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Renamed", 1))
	ok, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "rename", Paths: []string{"project.yaml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ok.Output, "rename") {
		t.Fatalf("output %q", ok.Output)
	}

	writeRepoFile(t, dir, "project.yaml", leakyProjectBody)
	_, err = Commit(context.Background(), CommitRequest{
		Path: project, Message: "leak", Paths: []string{"project.yaml"},
	})
	if err == nil {
		t.Fatal("staged secret was committed")
	}
	var ve *verr.Error
	if !errors.As(err, &ve) || ve.Code != verr.CodeValidation {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "literal-key") {
		t.Fatalf("secret value leaked in the refusal: %s", err)
	}
	if !strings.Contains(err.Error(), "Nothing was committed") {
		t.Fatalf("refusal: %s", err)
	}
	if subject := gitSubject(t, dir); subject != "rename" {
		t.Fatalf("head is %q", subject)
	}

	writeRepoFile(t, dir, ".env", "API_TOKEN=literal-key\n")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Again", 1))
	_, err = Commit(context.Background(), CommitRequest{
		Path: project, Message: "dotenv", Paths: []string{"project.yaml", ".env"},
	})
	if err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("dotenv was not refused: %v", err)
	}
	if gitSubject(t, dir) != "rename" {
		t.Fatal("dotenv commit was recorded")
	}
}

func TestCommitOnlySelectedPaths(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	writeRepoFile(t, dir, "notes.txt", "clean\n")
	gitAddCommit(t, dir, "init")

	writeRepoFile(t, dir, "notes.txt", "token hunter2 stays staged\n")
	gitRun(t, dir, "add", "--", "notes.txt")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Selected", 1))
	if _, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "selected", Paths: []string{"project.yaml"},
	}); err != nil {
		t.Fatal(err)
	}
	names := gitNameOnly(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if strings.Contains(names, "notes.txt") || !strings.Contains(names, "project.yaml") {
		t.Fatalf("commit recorded %q", names)
	}
	cached := gitNameOnly(t, dir, "diff", "--cached", "--name-only")
	if !strings.Contains(cached, "notes.txt") {
		t.Fatalf("pre-staged file left the index: %q", cached)
	}
}

func TestSecretRefusalRestoresIndex(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	writeRepoFile(t, dir, "notes.txt", "already staged\n")
	gitAddCommit(t, dir, "init")
	writeRepoFile(t, dir, "notes.txt", "still staged\n")
	gitRun(t, dir, "add", "--", "notes.txt")
	writeRepoFile(t, dir, "project.yaml", leakyProjectBody)
	_, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "leak", Paths: []string{"project.yaml"},
	})
	if err == nil {
		t.Fatal("secret was committed")
	}
	cached := gitNameOnly(t, dir, "diff", "--cached", "--name-only")
	if strings.Contains(cached, "project.yaml") {
		t.Fatalf("refused file stayed staged:\n%s", cached)
	}
	if !strings.Contains(cached, "notes.txt") {
		t.Fatalf("unrelated index entry was cleared:\n%s", cached)
	}
	if gitSubject(t, dir) != "init" {
		t.Fatal("a commit was recorded")
	}
}

func TestHookFailureRestoresDifferentBlob(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "init")
	before := indexBlob(t, dir, "project.yaml")
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	script := "#!/bin/sh\necho hook-says-no\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(cleanProject, "name: Demo", "name: Other", 1)
	writeRepoFile(t, dir, "project.yaml", edited)
	_, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "other", Paths: []string{"project.yaml"},
	})
	if err == nil || !strings.Contains(err.Error(), "hook-says-no") {
		t.Fatalf("hook: %v", err)
	}
	if gitSubject(t, dir) != "init" {
		t.Fatal("hook-refused commit was recorded")
	}
	if got := indexBlob(t, dir, "project.yaml"); got != before {
		t.Fatalf("index blob %s, staged blob was %s", got, before)
	}
}

func TestCancelDuringStageRestoresIndex(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "init")
	before := indexBlob(t, dir, "project.yaml")
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
	edited := strings.Replace(cleanProject, "name: Demo", "name: Other", 1)
	writeRepoFile(t, dir, "project.yaml", edited)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Commit(ctx, CommitRequest{
			Path: project, Message: "other", Paths: []string{"project.yaml"},
		})
		done <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("stage did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled commit succeeded")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelled commit did not return")
	}
	if got := indexBlob(t, dir, "project.yaml"); got != before {
		t.Fatalf("index blob %s, original %s", got, before)
	}
	if gitSubject(t, dir) != "init" {
		t.Fatal("cancelled commit was recorded")
	}
}

func TestUnmergedSecretRestoresStages(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "init")
	gitRun(t, dir, "checkout", "-b", "side")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Side", 1))
	gitAddCommit(t, dir, "side")
	gitRun(t, dir, "checkout", "main")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Main", 1))
	gitAddCommit(t, dir, "main")
	merge := exec.Command("git", "merge", "--no-commit", "--no-ff", "side")
	merge.Dir = dir
	merge.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("expected a conflict:\n%s", out)
	}
	writeRepoFile(t, dir, "project.yaml", leakyProjectBody)
	_, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "leak", Paths: []string{"project.yaml"},
	})
	if err == nil {
		t.Fatal("secret in a resolved conflict was committed")
	}
	listed := gitNameOnly(t, dir, "ls-files", "-s", "--", "project.yaml")
	if strings.Contains(listed, " 0\t") {
		t.Fatalf("stage 0 remained:\n%s", listed)
	}
	if !strings.Contains(listed, " 1\t") || !strings.Contains(listed, " 2\t") || !strings.Contains(listed, " 3\t") {
		t.Fatalf("unmerged stages were not restored:\n%s", listed)
	}
	if gitSubject(t, dir) != "main" {
		t.Fatalf("head is %q", gitSubject(t, dir))
	}
}

func indexBlob(t *testing.T, dir, path string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ":"+path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestDotenvRenameRefused(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	writeRepoFile(t, dir, ".env", "API_TOKEN=from-env\n")
	gitAddCommit(t, dir, "init")
	gitRun(t, dir, "mv", "--", ".env", "notes.txt")
	_, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "rename env", Paths: []string{"notes.txt"},
	})
	if err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("renamed dotenv was not refused: %v", err)
	}
	if gitSubject(t, dir) != "init" {
		t.Fatal("dotenv rename was committed")
	}
}

func TestSwitchRefusesDirtyProjectFiles(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "init")
	gitRun(t, dir, "checkout", "-b", "other")
	gitRun(t, dir, "checkout", "main")
	writeRepoFile(t, dir, "notes.txt", "not a project file\n")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Dirty", 1))

	res, err := Switch(context.Background(), SwitchRequest{Path: project, Branch: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "dirty" || len(res.Files) != 1 || res.Files[0] != "project.yaml" {
		t.Fatalf("dirty: %+v", res)
	}
	if branch := currentBranchName(t, dir); branch != "main" {
		t.Fatalf("branch changed to %s", branch)
	}

	res, err = Switch(context.Background(), SwitchRequest{Path: project, Branch: "other", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "switched" || currentBranchName(t, dir) != "other" {
		t.Fatalf("force: %+v branch %s", res, currentBranchName(t, dir))
	}

	gitRun(t, dir, "checkout", "--", "project.yaml")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: OnOther", 1))
	gitAddCommit(t, dir, "on-other")
	gitRun(t, dir, "checkout", "main")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: OnMain", 1))
	_, err = Switch(context.Background(), SwitchRequest{Path: project, Branch: "other", Force: true})
	if err == nil {
		t.Fatal("git allowed a switch that would overwrite project.yaml")
	}
	if currentBranchName(t, dir) != "main" {
		t.Fatalf("branch is %s after a refused switch", currentBranchName(t, dir))
	}
}

func TestFileURLStaysInWorkTree(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	name := "project #ą.yaml"
	project := filepath.Join(dir, name)
	writeRepoFile(t, dir, name, cleanProject)
	gitAddCommit(t, dir, "init")
	u, err := FileURL(project, name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "file:///") || !strings.Contains(u, "project%20%23") {
		t.Fatalf("url %s", u)
	}
	if _, err := FileURL(project, "../outside"); err == nil {
		t.Fatal("path outside the repository was accepted")
	}
}

func TestGitOpsOneAtATime(t *testing.T) {
	var g GitOps
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- g.Run(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	err := g.Run(context.Background(), func(context.Context) error { return nil })
	var ve *verr.Error
	if !errors.As(err, &ve) || ve.Code != verr.CodeBusy {
		t.Fatalf("second run: %v", err)
	}
	g.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if err := g.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteHookSkipsDirtySwitch(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	project := filepath.Join(dir, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "init")
	gitRun(t, dir, "checkout", "-b", "other")
	gitRun(t, dir, "checkout", "main")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "name: Demo", "name: Dirty", 1))

	var calls int
	res, err := Switch(context.Background(), SwitchRequest{
		Path: project, Branch: "other", BeforeRewrite: func() { calls++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "dirty" || calls != 0 {
		t.Fatalf("dirty switch ran the rewrite hook: status %s calls %d", res.Status, calls)
	}
	res, err = Switch(context.Background(), SwitchRequest{
		Path: project, Branch: "other", Force: true, BeforeRewrite: func() { calls++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "switched" || calls != 1 {
		t.Fatalf("checkout hook: status %s calls %d", res.Status, calls)
	}
}

func TestSaveCommitPushPullRoundTrip(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "--bare", "-b", "main")
	origin := t.TempDir()
	gitInit(t, origin)
	project := filepath.Join(origin, "project.yaml")
	writeRepoFile(t, origin, "project.yaml", cleanProject)
	gitAddCommit(t, origin, "init")
	gitRun(t, origin, "remote", "add", "origin", bare)
	gitRun(t, origin, "push", "-u", "origin", "HEAD")

	clone := t.TempDir()
	gitRun(t, "", "clone", bare, clone)

	hookMark := filepath.Join(origin, ".git", "hook-ran")
	hook := filepath.Join(origin, ".git", "hooks", "pre-commit")
	script := "#!/bin/sh\ntouch " + shQuote(hookMark) + "\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := Inspect(project)
	if err != nil {
		t.Fatal(err)
	}
	step := info.Project.Suites[0].Cases[0].Steps[0]
	const edited = "https://example.test/widgets"
	step.Request["url"] = edited
	var baseline string
	for _, originStep := range info.StepFiles {
		if originStep.Suite == "Smoke" && originStep.Case == "widgets" && originStep.Index == 0 {
			baseline = originStep.Hash
		}
	}
	if baseline == "" {
		t.Fatal("missing step baseline")
	}
	if _, err := SaveStep(SaveStepRequest{
		Path: project, Suite: "Smoke", Case: "widgets", Index: 0, Step: step, Baseline: baseline,
	}); err != nil {
		t.Fatal(err)
	}
	beforeCommit, err := GitChanges(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeCommit.Steps) != 1 || beforeCommit.Steps[0].State != changeModified {
		t.Fatalf("changes before commit: %+v", beforeCommit.Steps)
	}

	committed, err := Commit(context.Background(), CommitRequest{
		Path: project, Message: "edit request", Paths: []string{"project.yaml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Output == "" {
		t.Fatal("commit returned no output")
	}
	if _, err := os.Stat(hookMark); err != nil {
		t.Fatalf("commit did not run the pre-commit hook: %v", err)
	}
	if _, err := Push(context.Background(), project, nil); err != nil {
		t.Fatal(err)
	}

	cloneProject := filepath.Join(clone, "project.yaml")
	stale, err := Inspect(cloneProject)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Project.Suites[0].Cases[0].Steps[0].Request["url"] == edited {
		t.Fatal("clone already had the edit before pull")
	}
	pulled, err := Pull(context.Background(), PullRequest{Path: cloneProject, Mode: vcs.PullFFOnly}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.Status != "updated" {
		t.Fatalf("pull: %+v", pulled)
	}
	fresh, err := Inspect(cloneProject)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Project.Suites[0].Cases[0].Steps[0].Request["url"] != edited {
		t.Fatalf("inspect after pull: %v", fresh.Project.Suites[0].Cases[0].Steps[0].Request["url"])
	}
	ch, err := GitChanges(cloneProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Steps) != 0 || len(ch.Meta) != 0 {
		t.Fatalf("working tree differs from the pulled commit: steps %+v meta %+v", ch.Steps, ch.Meta)
	}
}

func useCommitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func gitNameOnly(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func gitSubject(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%s")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func currentBranchName(t *testing.T, dir string) string {
	t.Helper()
	st, err := vcs.Status(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Branch
}
