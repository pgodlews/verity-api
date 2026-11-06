package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/vcs"
)

func TestDiffProjects(t *testing.T) {
	head := &model.Project{
		Environments: []model.Environment{{
			Name: "local", Default: true,
			Variables: map[string]string{"BASE_URL": "https://example.test", "token": "literal-token-value"},
		}},
		Auth: []model.AuthDef{{Name: "token", Type: "bearer", Config: map[string]any{"token": "${env:TOKEN}"}}},
		Suites: []model.Suite{{
			Name: "Smoke",
			Cases: []model.TestCase{{
				Name: "widgets",
				Steps: []model.Step{
					restStep("list", "GET", "https://example.test/widgets", map[string]any{"Accept": "application/json", "Authorization": "Bearer literal-token-value"}),
					restStep("list", "GET", "https://example.test/widgets", nil),
					restStep("gone", "DELETE", "https://example.test/gone", nil),
				},
			}},
		}, {
			Name:  "Old",
			Cases: []model.TestCase{{Name: "move", Steps: []model.Step{restStep("login", "POST", "https://example.test/login", nil)}}},
		}},
	}
	work := &model.Project{
		Environments: []model.Environment{{
			Name: "local", Default: false,
			Variables: map[string]string{"BASE_URL": "https://example.test/v2", "token": "${env:TOKEN}"},
		}},
		Auth: []model.AuthDef{{Name: "token", Type: "bearer", Config: map[string]any{"token": "${env:OTHER}"}}},
		Suites: []model.Suite{{
			Name: "Smoke",
			Cases: []model.TestCase{{
				Name: "widgets",
				Steps: []model.Step{
					restStep("list", "POST", "https://example.test/widgets", map[string]any{"Accept": "text/plain", "Authorization": "${env:TOKEN}"}),
					restStep("list", "GET", "https://example.test/widgets", nil),
					restStep("created", "PUT", "https://example.test/new", nil),
				},
			}},
		}, {
			Name:  "New",
			Cases: []model.TestCase{{Name: "elsewhere", Steps: []model.Step{restStep("login", "POST", "https://example.test/login", nil)}}},
		}},
	}
	ch := diffProjects(head, work)

	var modified, moved, added, removed *StepChange
	for i := range ch.Steps {
		s := &ch.Steps[i]
		switch {
		case s.State == changeModified && s.Name == "list" && s.Index == 0:
			modified = s
		case s.State == changeModified && s.Index == 1:
			t.Fatalf("duplicate name at index 1 should be unchanged, got %+v", s)
		case s.State == changeMoved && s.Name == "login":
			moved = s
		case s.State == changeAdded && s.Name == "created":
			added = s
		case s.State == changeRemoved && s.Name == "gone":
			removed = s
		}
	}
	if modified == nil || moved == nil || added == nil || removed == nil {
		t.Fatalf("steps: %+v", ch.Steps)
	}
	if moved.FromSuite != "Old" || moved.Suite != "New" || moved.FromCase != "move" || moved.Case != "elsewhere" {
		t.Fatalf("move: %+v", moved)
	}
	if part(modified, "method", "").Before != "GET" || part(modified, "method", "").After != "POST" {
		t.Fatalf("method: %+v", modified.Parts)
	}
	accept := part(modified, "headers", "Accept")
	if accept.Before != "application/json" || accept.After != "text/plain" {
		t.Fatalf("header: %+v", accept)
	}
	authz := part(modified, "headers", "Authorization")
	if authz.Before != redactedValue || authz.After != "${env:TOKEN}" {
		t.Fatalf("authorization: %+v", authz)
	}
	if len(ch.Environments) != 1 || ch.Environments[0].State != changeModified {
		t.Fatalf("envs: %+v", ch.Environments)
	}
	tok := partChange(ch.Environments[0].Parts, "variables", "token")
	if tok.Before != redactedValue || tok.After != "${env:TOKEN}" {
		t.Fatalf("env token: %+v", tok)
	}
	base := partChange(ch.Environments[0].Parts, "variables", "BASE_URL")
	if base.Before != "https://example.test" || base.After != "https://example.test/v2" {
		t.Fatalf("base: %+v", base)
	}
	def := partChange(ch.Environments[0].Parts, "default", "")
	if def.Before != "true" || def.After != "false" {
		t.Fatalf("default: %+v", def)
	}
	if len(ch.Auth) != 1 || partChange(ch.Auth[0].Parts, "config", "token").After != "${env:OTHER}" {
		t.Fatalf("auth: %+v", ch.Auth)
	}
	if !hasScope(ch.Suites, "", "Old", changeRemoved) || !hasScope(ch.Suites, "", "New", changeAdded) {
		t.Fatalf("suites: %+v", ch.Suites)
	}
	if !hasScope(ch.Cases, "Old", "move", changeRemoved) || !hasScope(ch.Cases, "New", "elsewhere", changeAdded) {
		t.Fatalf("cases: %+v", ch.Cases)
	}
}

func TestDiffUncommittedIsAdded(t *testing.T) {
	work := &model.Project{Suites: []model.Suite{{Name: "Smoke", Cases: []model.TestCase{{
		Name: "widgets", Steps: []model.Step{restStep("list", "GET", "https://example.test", nil)},
	}}}}}
	ch := diffProjects(&model.Project{}, work)
	if len(ch.Steps) != 1 || ch.Steps[0].State != changeAdded || ch.Steps[0].Name != "list" {
		t.Fatalf("%+v", ch.Steps)
	}
	if !hasScope(ch.Suites, "", "Smoke", changeAdded) {
		t.Fatalf("suites: %+v", ch.Suites)
	}
}

func TestGitStatusNotARepository(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	info, err := GitStatus(filepath.Join(t.TempDir(), "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Available || info.Repo || len(info.Files) != 0 {
		t.Fatalf("%+v", info)
	}
	ch, err := GitChanges(filepath.Join(t.TempDir(), "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Available || ch.Repo {
		t.Fatalf("%+v", ch)
	}
}

func TestGitChangesInRepo(t *testing.T) {
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	t.Run("fragment and secret", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		writeRepoFile(t, dir, "project.yaml", ""+
			"schemaVersion: \"1.0\"\nname: Demo\ninclude:\n  - parts/api.yaml\n"+
			"environments:\n  - name: local\n    variables:\n      token: literal-token-value\n")
		writeRepoFile(t, dir, "parts/api.yaml", ""+
			"suites:\n  - name: Smoke\n    cases:\n      - name: widgets\n        steps:\n"+
			"          - name: list\n            protocol: rest\n            request:\n"+
			"              method: GET\n              url: https://example.test/widgets\n"+
			"              headers:\n                Accept: application/json\n")
		gitAddCommit(t, dir, "initial")
		writeRepoFile(t, dir, "project.yaml", ""+
			"schemaVersion: \"1.0\"\nname: Demo\ninclude:\n  - parts/api.yaml\n"+
			"environments:\n  - name: local\n    variables:\n      token: \"${env:TOKEN}\"\n")
		writeRepoFile(t, dir, "parts/api.yaml", ""+
			"suites:\n  - name: Smoke\n    cases:\n      - name: widgets\n        steps:\n"+
			"          - name: list\n            protocol: rest\n            request:\n"+
			"              method: POST\n              url: https://example.test/widgets\n"+
			"              headers:\n                Accept: application/json\n              body: |\n                {\"id\":43}\n")
		writeRepoFile(t, dir, "notes.txt", "outside the project\n")

		info, err := GitStatus(filepath.Join(dir, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Repo || info.Detached || info.Branch == "" {
			t.Fatalf("status: %+v", info)
		}
		var sawProject, sawOther bool
		for _, f := range info.Files {
			if f.Path == "project.yaml" || f.Path == "parts/api.yaml" {
				if !f.ProjectFile {
					t.Fatalf("project file not marked: %+v", f)
				}
				sawProject = true
			}
			if f.Path == "notes.txt" {
				if f.ProjectFile || f.State != "untracked" {
					t.Fatalf("other file: %+v", f)
				}
				sawOther = true
			}
		}
		if !sawProject || !sawOther {
			t.Fatalf("files: %+v", info.Files)
		}
		var marked bool
		for _, s := range info.ChangedSteps {
			if s.Suite == "Smoke" && s.Case == "widgets" && s.Name == "list" && s.State == changeModified {
				marked = true
			}
		}
		if !marked {
			t.Fatalf("changed steps: %+v", info.ChangedSteps)
		}

		ch, err := GitChanges(filepath.Join(dir, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if ch.OtherFiles < 1 {
			t.Fatalf("other files %d", ch.OtherFiles)
		}
		if len(ch.Steps) != 1 || ch.Steps[0].State != changeModified {
			t.Fatalf("steps: %+v", ch.Steps)
		}
		method := part(&ch.Steps[0], "method", "")
		if method.Before != "GET" || method.After != "POST" {
			t.Fatalf("method came from the wrong side: %+v", method)
		}
		if len(ch.Environments) != 1 {
			t.Fatalf("envs: %+v", ch.Environments)
		}
		tok := partChange(ch.Environments[0].Parts, "variables", "token")
		if tok.Before != redactedValue || tok.After != "${env:TOKEN}" {
			t.Fatalf("HEAD secret was not redacted: %+v", tok)
		}
		if strings.Contains(tok.Before, "literal-token-value") {
			t.Fatal("literal secret leaked into the diff")
		}
		ins, err := Inspect(filepath.Join(dir, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var origin string
		for _, s := range ins.StepFiles {
			if s.Suite == "Smoke" && s.Case == "widgets" && s.Index == 0 {
				origin = s.File
			}
		}
		if origin != filepath.Join(dir, "parts", "api.yaml") {
			t.Fatalf("step file %s", origin)
		}
	})

	t.Run("uncommitted project", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		writeRepoFile(t, dir, "project.yaml", ""+
			"schemaVersion: \"1.0\"\nname: Demo\nsuites:\n  - name: Smoke\n    cases:\n"+
			"      - name: widgets\n        steps:\n          - name: list\n            protocol: rest\n"+
			"            request:\n              method: GET\n              url: https://example.test\n")
		ch, err := GitChanges(filepath.Join(dir, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ch.Steps) != 1 || ch.Steps[0].State != changeAdded {
			t.Fatalf("%+v", ch.Steps)
		}
		info, err := GitStatus(filepath.Join(dir, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Branch == "" || info.Detached {
			t.Fatalf("initial branch: %+v", info)
		}
	})

	t.Run("invalid project still reports files", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		writeRepoFile(t, dir, "README.txt", "ok\n")
		gitAddCommit(t, dir, "readme")
		writeRepoFile(t, dir, "project.yaml", ": [\n")
		info, err := GitStatus(filepath.Join(dir, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Repo || len(info.ChangedSteps) != 0 {
			t.Fatalf("%+v", info)
		}
	})
}

func restStep(name, method, url string, headers map[string]any) model.Step {
	req := map[string]any{"method": method, "url": url}
	if headers != nil {
		req["headers"] = headers
	}
	return model.Step{Name: name, Protocol: "rest", Request: req}
}

func part(step *StepChange, name, key string) PartChange {
	return partChange(step.Parts, name, key)
}

func partChange(parts []PartChange, name, key string) PartChange {
	for _, p := range parts {
		if p.Part == name && p.Key == key {
			return p
		}
	}
	return PartChange{}
}

func hasScope(list []ScopeChange, suite, name, state string) bool {
	for _, s := range list {
		if s.Suite == suite && s.Name == name && s.State == state {
			return true
		}
	}
	return false
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "-c", "init.defaultBranch=main", "init")
	gitRun(t, dir, "config", "core.autocrlf", "false")
}

func gitAddCommit(t *testing.T, dir, message string) {
	t.Helper()
	gitRun(t, dir, "add", "--", ".")
	gitRun(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", message)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
