package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestDiffRedactsPhase1Secrets(t *testing.T) {
	head := &model.Project{Suites: []model.Suite{{Name: "Smoke", Cases: []model.TestCase{{
		Name: "widgets",
		Steps: []model.Step{
			restStep("list", "GET", "https://example.test/widgets", nil),
		},
	}}}}}
	work := &model.Project{
		Auth: []model.AuthDef{{
			Name: "custom", Type: "custom",
			Config: map[string]any{"extra": map[string]any{"password": "hunter2"}},
		}},
		Suites: []model.Suite{{Name: "Smoke", Cases: []model.TestCase{{
			Name: "widgets",
			Steps: []model.Step{{
				Name: "list", Protocol: "rest",
				Request: map[string]any{
					"method": "GET",
					"url":    "https://user:hunter2@example.test/path?api_key=abc",
					"body":   "password=hunter2&user=ada",
				},
			}},
		}}}},
	}
	ch := diffProjects(head, work)
	if len(ch.Steps) != 1 {
		t.Fatalf("steps: %+v", ch.Steps)
	}
	url := part(&ch.Steps[0], "url", "")
	if url.After != redactedValue || strings.Contains(url.After, "hunter2") || strings.Contains(url.After, "abc") {
		t.Fatalf("url: %+v", url)
	}
	body := part(&ch.Steps[0], "body", "")
	if body.After != redactedValue || strings.Contains(body.After, "hunter2") {
		t.Fatalf("form body: %+v", body)
	}
	if len(ch.Auth) != 1 {
		t.Fatalf("auth: %+v", ch.Auth)
	}
	extra := partChange(ch.Auth[0].Parts, "config", "extra")
	if extra.After != redactedValue || strings.Contains(extra.After, "hunter2") {
		t.Fatalf("nested auth: %+v", extra)
	}

	bodies := []string{
		"<Password>hunter2</Password>",
		`{"password":1234}`,
		`{"password":"hunter2","password":"${env:PW}"}`,
	}
	for _, raw := range bodies {
		step := restStep("list", "GET", "https://example.test/widgets", nil)
		step.Request["body"] = raw
		work.Suites[0].Cases[0].Steps[0] = step
		ch = diffProjects(head, work)
		got := part(&ch.Steps[0], "body", "")
		if got.After != redactedValue || strings.Contains(got.After, "hunter2") || strings.Contains(got.After, "1234") {
			t.Fatalf("body %s: %+v", raw, got)
		}
	}
}

func TestDiffOrderAndProjectFields(t *testing.T) {
	no := false
	head := &model.Project{
		SchemaVersion: "1.0", Name: "Demo", Description: "one",
		Environments: []model.Environment{{Name: "local", Variables: map[string]string{"A": "1"}}},
		Suites: []model.Suite{{
			Name: "Smoke", Parallel: 1,
			Vars: map[string]string{"S": "1"},
			Setup: &model.Hook{Name: "boot", Steps: []model.Step{
				restStep("warm", "GET", "https://example.test/warm", nil),
			}},
			Cases: []model.TestCase{{
				Name: "widgets",
				Vars: map[string]string{"C": "1"},
				Steps: []model.Step{
					restStep("login", "POST", "https://example.test/login", nil),
					restStep("list", "GET", "https://example.test/widgets", nil),
				},
			}},
		}},
	}
	work := &model.Project{
		SchemaVersion: "1.1", Name: "Demo 2", Description: "two",
		Environments: []model.Environment{{
			Name: "local", Variables: map[string]string{"A": "1"},
			Auth: &model.AuthRef{Use: []string{"token"}},
		}},
		Suites: []model.Suite{{
			Name: "Smoke", Parallel: 3,
			Vars: map[string]string{"S": "2"},
			Auth: &model.AuthRef{Use: []string{"token"}},
			Setup: &model.Hook{Name: "boot", Steps: []model.Step{
				restStep("warm", "GET", "https://example.test/warmed", nil),
			}},
			Cases: []model.TestCase{{
				Name:            "widgets",
				Vars:            map[string]string{"C": "2"},
				ContinueOnError: &no,
				Feed:            &model.Feed{Type: "csv", Path: "rows.csv"},
				Steps: []model.Step{
					restStep("list", "GET", "https://example.test/widgets", nil),
					restStep("login", "POST", "https://example.test/login", nil),
				},
			}},
		}},
	}
	ch := diffProjects(head, work)
	var login, list, warm *StepChange
	for i := range ch.Steps {
		s := &ch.Steps[i]
		switch s.Name {
		case "login":
			login = s
		case "list":
			list = s
		case "warm":
			warm = s
		}
	}
	if login == nil || list == nil || warm == nil {
		t.Fatalf("steps: %+v", ch.Steps)
	}
	if part(login, "order", "").Before != "0" || part(login, "order", "").After != "1" {
		t.Fatalf("login order: %+v", login.Parts)
	}
	if part(list, "order", "").Before != "1" || part(list, "order", "").After != "0" {
		t.Fatalf("list order: %+v", list.Parts)
	}
	if warm.Case != "(setup)" || part(warm, "url", "").After != "https://example.test/warmed" {
		t.Fatalf("setup: %+v", warm)
	}
	if partChange(ch.Meta, "name", "").After != "Demo 2" || partChange(ch.Meta, "schemaVersion", "").After != "1.1" || partChange(ch.Meta, "description", "").After != "two" {
		t.Fatalf("meta: %+v", ch.Meta)
	}
	if partChange(ch.Environments[0].Parts, "auth", "").After == "" {
		t.Fatalf("env auth: %+v", ch.Environments[0].Parts)
	}
	var suite, kase *ScopeChange
	for i := range ch.Suites {
		if ch.Suites[i].State == changeModified {
			suite = &ch.Suites[i]
		}
	}
	for i := range ch.Cases {
		if ch.Cases[i].State == changeModified {
			kase = &ch.Cases[i]
		}
	}
	if suite == nil || partChange(suite.Parts, "vars", "S").After != "2" || partChange(suite.Parts, "parallel", "").After != "3" {
		t.Fatalf("suite: %+v", ch.Suites)
	}
	if kase == nil || partChange(kase.Parts, "vars", "C").After != "2" || partChange(kase.Parts, "continueOnError", "").After != "false" || partChange(kase.Parts, "feed", "").After == "" {
		t.Fatalf("case: %+v", ch.Cases)
	}
}

func TestDiffMoveNeedsRequestIdentity(t *testing.T) {
	head := &model.Project{Suites: []model.Suite{{
		Name: "Smoke", Cases: []model.TestCase{{Name: "a", Steps: []model.Step{
			restStep("login", "POST", "https://example.test/login", nil),
		}}},
	}}}
	work := &model.Project{Suites: []model.Suite{{
		Name: "Other", Cases: []model.TestCase{{Name: "b", Steps: []model.Step{
			restStep("login", "GET", "https://example.test/other", nil),
		}}},
	}}}
	ch := diffProjects(head, work)
	var added, removed, moved int
	for _, s := range ch.Steps {
		switch s.State {
		case changeAdded:
			added++
		case changeRemoved:
			removed++
		case changeMoved:
			moved++
		}
	}
	if moved != 0 || added != 1 || removed != 1 {
		t.Fatalf("steps: %+v", ch.Steps)
	}
}

func TestDiffMoveMatchesBodyNotZip(t *testing.T) {
	users := restStep("list", "POST", "https://example.test/graphql", nil)
	users.Request["body"] = "{ users }"
	orders := restStep("list", "POST", "https://example.test/graphql", nil)
	orders.Request["body"] = "{ orders }"
	head := &model.Project{Suites: []model.Suite{{
		Name: "A", Cases: []model.TestCase{{Name: "a", Steps: []model.Step{users}}},
	}}}
	work := &model.Project{Suites: []model.Suite{{
		Name: "B", Cases: []model.TestCase{{Name: "b", Steps: []model.Step{orders}}},
	}}}
	ch := diffProjects(head, work)
	for _, s := range ch.Steps {
		if s.State == changeMoved {
			t.Fatalf("different bodies were paired: %+v", ch.Steps)
		}
	}

	left := restStep("list", "POST", "https://example.test/left", nil)
	right := restStep("list", "POST", "https://example.test/right", nil)
	head = &model.Project{Suites: []model.Suite{{
		Name: "A", Cases: []model.TestCase{{Name: "a", Steps: []model.Step{left, right}}},
	}}}
	work = &model.Project{Suites: []model.Suite{{
		Name: "B", Cases: []model.TestCase{{Name: "b", Steps: []model.Step{right, left}}},
	}}}
	ch = diffProjects(head, work)
	var moved int
	for _, s := range ch.Steps {
		if s.State == changeMoved {
			moved++
		}
		if s.State == changeAdded || s.State == changeRemoved {
			t.Fatalf("crossed URLs were not paired: %+v", ch.Steps)
		}
	}
	if moved != 2 {
		t.Fatalf("moves %d steps %+v", moved, ch.Steps)
	}
}

func TestGenericModifiedWhenNothingSpecific(t *testing.T) {
	head := &model.Project{Name: "A"}
	work := &model.Project{Name: "B"}
	ch := blankChanges(true, true)
	ensureReported(ch, head, work)
	if len(ch.Meta) != 1 || ch.Meta[0].Part != "project" || ch.Meta[0].After != "modified" {
		t.Fatalf("fallback: %+v", ch.Meta)
	}
	full := diffProjects(head, work)
	if partChange(full.Meta, "project", "").After == "modified" {
		t.Fatalf("specific name change should not also be generic: %+v", full.Meta)
	}
	if partChange(full.Meta, "name", "").After != "B" {
		t.Fatalf("name: %+v", full.Meta)
	}
	same := diffProjects(head, head)
	if len(same.Steps)+len(same.Meta)+len(same.Environments) != 0 {
		t.Fatalf("equal projects: %+v", same)
	}
}

func TestRetainSourcesOnLoadFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project.yaml")
	part := filepath.Join(filepath.Dir(root), "parts", "api.yaml")
	prev := []string{root, part}
	got := RetainSources(prev, []string{root}, root, false)
	if len(got) != 2 {
		t.Fatalf("retained: %v", got)
	}
	fresh := RetainSources(nil, []string{root, part}, root, true)
	if len(fresh) != 2 || fresh[0] != root {
		t.Fatalf("loaded: %v", fresh)
	}
}

func TestWatchPathsKeepsRefsAndSurvivesBrokenFragment(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	writeRepoFile(t, dir, "project.yaml", ""+
		"schemaVersion: \"1.0\"\nname: Demo\ninclude:\n  - parts/api.yaml\n")
	writeRepoFile(t, dir, "parts/api.yaml", ""+
		"suites:\n  - name: Smoke\n    cases:\n      - name: widgets\n        steps:\n"+
		"          - name: list\n            protocol: rest\n            request:\n              method: GET\n              url: https://example.test\n")
	gitAddCommit(t, dir, "initial")
	gitRun(t, dir, "branch", "side")
	gitRun(t, dir, "branch", "-u", "side")

	root := filepath.Join(dir, "project.yaml")
	sources, meta, loaded, err := WatchPaths(root)
	if err != nil || !loaded {
		t.Fatalf("loaded %v err %v", loaded, err)
	}
	if !containsPath(sources, filepath.Join(dir, "parts", "api.yaml")) {
		t.Fatalf("sources: %v", sources)
	}
	branch, err := gitOutput(dir, "rev-parse", "--git-path", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	branch = filepath.Clean(absGitPath(dir, branch))
	up, err := gitOutput(dir, "rev-parse", "--git-path", "refs/heads/side")
	if err != nil {
		t.Fatal(err)
	}
	up = filepath.Clean(absGitPath(dir, up))
	if !containsPath(meta, branch) || !containsPath(meta, up) {
		t.Fatalf("meta missing branch refs: %v", meta)
	}
	var head, packed bool
	for _, p := range meta {
		switch filepath.Base(p) {
		case "HEAD":
			head = true
		case "packed-refs":
			packed = true
		}
	}
	if !head || !packed {
		t.Fatalf("meta: %v", meta)
	}

	writeRepoFile(t, dir, "parts/api.yaml", ":\n  [\n")
	broken, _, loaded, err := WatchPaths(root)
	if err != nil || loaded {
		t.Fatalf("broken load: loaded %v err %v sources %v", loaded, err, broken)
	}
	kept := RetainSources(sources, broken, root, loaded)
	if !containsPath(kept, filepath.Join(dir, "parts", "api.yaml")) || !containsPath(kept, root) {
		t.Fatalf("kept: %v", kept)
	}
}

func TestGitStatusDoesNotRunFsmonitor(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	writeRepoFile(t, dir, "project.yaml", "schemaVersion: \"1.0\"\nname: Demo\n")
	gitAddCommit(t, dir, "initial")
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
	if _, err := GitStatus(filepath.Join(dir, "project.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("GitStatus ran the fsmonitor hook: %v", err)
	}
}

func TestGitChangesFollowsRootRename(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	writeRepoFile(t, dir, "old.yaml", ""+
		"schemaVersion: \"1.0\"\nname: Demo\nsuites:\n  - name: Smoke\n    cases:\n"+
		"      - name: widgets\n        steps:\n          - name: list\n            protocol: rest\n"+
		"            request:\n              method: GET\n              url: https://example.test/widgets\n")
	gitAddCommit(t, dir, "initial")
	gitRun(t, dir, "mv", "--", "old.yaml", "new.yaml")
	writeRepoFile(t, dir, "new.yaml", ""+
		"schemaVersion: \"1.0\"\nname: Demo\nsuites:\n  - name: Smoke\n    cases:\n"+
		"      - name: widgets\n        steps:\n          - name: list\n            protocol: rest\n"+
		"            request:\n              method: POST\n              url: https://example.test/widgets\n")
	ch, err := GitChanges(filepath.Join(dir, "new.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Steps) != 1 || ch.Steps[0].State != changeModified || ch.Steps[0].Name != "list" {
		t.Fatalf("steps: %+v", ch.Steps)
	}
	if part(&ch.Steps[0], "method", "").Before != "GET" || part(&ch.Steps[0], "method", "").After != "POST" {
		t.Fatalf("method: %+v", ch.Steps[0].Parts)
	}
}

func TestGitChangesFollowsMovedDirectory(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	body := "" +
		"schemaVersion: \"1.0\"\nname: Demo\nsuites:\n  - name: Smoke\n    cases:\n" +
		"      - name: widgets\n        steps:\n          - name: list\n            protocol: rest\n" +
		"            request:\n              method: GET\n              url: https://example.test/widgets\n"
	writeRepoFile(t, dir, "apis/project.yaml", body)
	gitAddCommit(t, dir, "initial")
	gitRun(t, dir, "config", "diff.renames", "false")
	gitRun(t, dir, "mv", "--", "apis", "service")
	edited := strings.Replace(body, "method: GET", "method: POST", 1)
	writeRepoFile(t, dir, "service/project.yaml", edited)
	ch, err := GitChanges(filepath.Join(dir, "service", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Steps) != 1 || ch.Steps[0].State != changeModified {
		t.Fatalf("steps: %+v", ch.Steps)
	}
}

func TestCommentOnlyProjectFileReported(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	body := "schemaVersion: \"1.0\"\nname: Demo\n"
	writeRepoFile(t, dir, "project.yaml", body)
	gitAddCommit(t, dir, "initial")
	writeRepoFile(t, dir, "project.yaml", "schemaVersion: \"1.0\"\nname: Demo\n# note\n")
	ch, err := GitChanges(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if partChange(ch.Meta, "file", "").After != "changed" {
		t.Fatalf("meta: %+v", ch.Meta)
	}
}

func TestRepoLockSerializesSaves(t *testing.T) {
	path := writeProject(t)
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, s := range info.StepFiles {
		if s.Suite == "Smoke" && s.Case == "login" && s.Index == 0 {
			hash = s.Hash
		}
	}
	if hash == "" {
		t.Fatal("missing step hash")
	}
	step := info.Project.Suites[0].Cases[0].Steps[0]
	unlock := lockProject(path)
	done := make(chan error, 1)
	go func() {
		_, err := SaveStep(SaveStepRequest{
			Path: path, Suite: "Smoke", Case: "login", Index: 0, Step: step, Baseline: hash,
		})
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("save did not wait for the repository lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("save did not finish after the lock was released")
	}
}

func TestSaveConflictKeepsDisk(t *testing.T) {
	path := writeProject(t)
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, s := range info.StepFiles {
		if s.Suite == "Smoke" && s.Case == "login" && s.Index == 0 {
			hash = s.Hash
			if s.Occurrence != 0 {
				t.Fatalf("occurrence %d", s.Occurrence)
			}
		}
	}
	if hash == "" {
		t.Fatal("missing step hash")
	}
	current := info.Project.Suites[0].Cases[0].Steps[0]
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "login", Index: 0, Step: current, Baseline: hash}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), `${BASE_URL}/login`, `${BASE_URL}/login-ext`, 1)
	if edited == string(data) {
		t.Fatal("fixture url not found")
	}
	if err := os.WriteFile(path, []byte(edited), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	step := model.Step{Name: "Login", Protocol: "rest", Request: map[string]any{"method": "POST", "url": "${BASE_URL}/elsewhere"}}
	_, err = SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "login", Index: 0, Step: step, Baseline: hash})
	var ve *verr.Error
	if !errors.As(err, &ve) || ve.Code != verr.CodeConflict {
		t.Fatalf("err: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("conflict wrote the file")
	}
	res, err := SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "login", Index: 0, Step: step})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != path || len(res.Files) != 1 || res.Hashes[path] == "" {
		t.Fatalf("result: %+v", res)
	}

	info, err = Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]string{}
	for _, f := range info.FileHashes {
		base[f.Path] = f.Hash
	}
	envBefore, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(envBefore, []byte("\n# outside\n")...), 0o640); err != nil {
		t.Fatal(err)
	}
	disk, _ := os.ReadFile(path)
	_, err = SaveEnvironment(SaveEnvironmentRequest{
		Path: path, Name: "local", Baseline: base,
		Variables: map[string]string{"BASE_URL": "https://example.test"},
	})
	ve = nil
	if !errors.As(err, &ve) || ve.Code != verr.CodeConflict {
		t.Fatalf("env err: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(disk) {
		t.Fatal("environment conflict wrote the file")
	}
}

func TestSaveStepFileBaseline(t *testing.T) {
	path := writeProject(t)
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	var fileHash string
	for _, row := range info.FileHashes {
		if row.Path == path {
			fileHash = row.Hash
		}
	}
	if fileHash == "" {
		t.Fatal("missing file hash")
	}
	step := model.Step{Name: "Login", Protocol: "rest", Request: map[string]any{"method": "POST", "url": "${BASE_URL}/login-kept"}}
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "Smoke", Case: "login", Index: 0, Step: step, FileBaseline: fileHash}); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "/login-kept") {
		t.Fatal("matching fileBaseline did not save")
	}
	before := string(saved)
	_, err = SaveStep(SaveStepRequest{
		Path: path, Suite: "Smoke", Case: "login", Index: 0, FileBaseline: fileHash,
		Step: model.Step{Name: "Login", Protocol: "rest", Request: map[string]any{"method": "POST", "url": "${BASE_URL}/nope"}},
	})
	var ve *verr.Error
	if !errors.As(err, &ve) || ve.Code != verr.CodeConflict {
		t.Fatalf("err: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatal("fileBaseline mismatch wrote the file")
	}
}

func TestSaveEnvironmentReturnsEveryFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "project.yaml")
	frag := filepath.Join(dir, "env.yaml")
	if err := os.WriteFile(root, []byte(""+
		"schemaVersion: \"1.0\"\nname: Demo\ninclude:\n  - env.yaml\n"+
		"environments:\n  - name: local\n    default: true\n    variables:\n      A: \"1\"\n"+
		"suites:\n  - name: Smoke\n    cases:\n      - name: c\n        steps:\n"+
		"          - name: list\n            protocol: rest\n            request:\n              method: GET\n              url: https://example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frag, []byte("environments:\n  - name: staging\n    variables:\n      A: \"2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var noted []string
	yes := true
	res, err := SaveEnvironment(SaveEnvironmentRequest{
		Path: root, Name: "staging", Default: &yes,
		Variables:   map[string]string{"A": "2"},
		BeforeWrite: func(path string, data []byte) { noted = append(noted, path) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != frag {
		t.Fatalf("target %s", res.File)
	}
	if len(res.Files) != 2 || len(noted) != 2 {
		t.Fatalf("files %v noted %v", res.Files, noted)
	}
	saw := map[string]bool{}
	for _, f := range res.Files {
		saw[f] = res.Hashes[f] != ""
	}
	if !saw[root] || !saw[frag] {
		t.Fatalf("hashes: %+v", res.Hashes)
	}
}

func containsPath(list []string, want string) bool {
	want = filepath.Clean(want)
	for _, p := range list {
		if filepath.Clean(p) == want {
			return true
		}
	}
	return false
}

func absGitPath(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

func shQuote(s string) string {
	s = filepath.ToSlash(s)
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
