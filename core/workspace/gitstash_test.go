package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

func workspaceStashRepo(t *testing.T) (string, string) {
	t.Helper()
	if !vcs.Available() {
		t.Skip("git is not installed; skipping repository tests")
	}
	useCommitIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "base")
	return dir, filepath.Join(dir, "project.yaml")
}

func TestStashChangesRedactsBothSidesAndUsesSavedFragments(t *testing.T) {
	dir, project := workspaceStashRepo(t)
	ctx := context.Background()
	writeRepoFile(t, dir, "project.yaml", leakyProjectBody)
	gitAddCommit(t, dir, "old literal")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(leakyProjectBody, "literal-key", "different-literal", 1)+"include: [parts/new.yaml]\n")
	writeRepoFile(t, dir, "parts/new.yaml", "schemaVersion: \"1.0\"\nenvironments:\n  - name: saved\n    variables:\n      TOKEN: literal-fragment\n")
	writeRepoFile(t, dir, "notes.txt", "password=other-file-literal\n")
	calls := 0
	if _, err := Stash(ctx, StashRequest{Path: project, IncludeUntracked: true, Message: "token=message-literal", BeforeRewrite: func() { calls++ }}); err != nil {
		t.Fatal(err)
	}
	list, err := Stashes(ctx, project)
	if err != nil || len(list) != 1 || calls != 1 {
		t.Fatalf("list = %+v, calls %d, %v", list, calls, err)
	}
	// A later worktree edit must not be used to fill a missing stash fragment.
	writeRepoFile(t, dir, "parts/new.yaml", "schemaVersion: \"1.0\"\nenvironments:\n  - name: outside\n")
	ch, err := StashChanges(ctx, StashActionRequest{Path: project, ID: list[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		List    []vcs.Stash
		Changes *ProjectChanges
	}{list, ch})
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{"literal-key", "different-literal", "literal-fragment", "other-file-literal", "message-literal", "outside"} {
		if strings.Contains(string(data), literal) {
			t.Fatalf("stash diff leaked %q: %s", literal, data)
		}
	}
	if len(ch.Steps) != 1 || len(ch.Steps[0].Parts) != 1 || ch.Steps[0].Parts[0].Before != redactedValue || ch.Steps[0].Parts[0].After != redactedValue {
		t.Fatalf("redacted header diff: %+v", ch.Steps)
	}
	if len(ch.Environments) != 1 || ch.Environments[0].Name != "saved" || ch.OtherFiles != 1 {
		t.Fatalf("saved fragment / other file count: %+v", ch)
	}
}

func TestStashRewritePreservesSaveBaseline(t *testing.T) {
	for _, operation := range []string{"stash", "apply", "pop"} {
		t.Run(operation, func(t *testing.T) {
			dir, project := workspaceStashRepo(t)
			ctx := context.Background()
			changed := strings.Replace(cleanProject, "/health", "/changed", 1)
			writeRepoFile(t, dir, "project.yaml", changed)
			if operation != "stash" {
				if _, err := Stash(ctx, StashRequest{Path: project}); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := Inspect(project)
			if err != nil {
				t.Fatal(err)
			}
			step := loaded.Project.Suites[0].Cases[0].Steps[0]
			baseline := loaded.StepFiles[0].Hash
			step.Request["url"] = "${BASE_URL}/draft"
			calls := 0
			before := func() { calls++ }
			if operation == "stash" {
				_, err = Stash(ctx, StashRequest{Path: project, BeforeRewrite: before})
			} else {
				list, lerr := Stashes(ctx, project)
				if lerr != nil || len(list) != 1 {
					t.Fatalf("list: %+v, %v", list, lerr)
				}
				_, err = StashAction(ctx, StashActionRequest{Path: project, ID: list[0].ID, Action: operation, BeforeRewrite: before})
			}
			if err != nil || calls != 1 {
				t.Fatalf("rewrite = %d, %v", calls, err)
			}
			disk, err := os.ReadFile(project)
			if err != nil {
				t.Fatal(err)
			}
			_, err = SaveStep(SaveStepRequest{Path: project, Suite: "Smoke", Case: "widgets", Index: 0, Step: step, Baseline: baseline})
			var ve *verr.Error
			if !errors.As(err, &ve) || ve.Code != verr.CodeConflict {
				t.Fatalf("stale draft save: %v", err)
			}
			after, err := os.ReadFile(project)
			if err != nil || string(after) != string(disk) {
				t.Fatal("stale save replaced rewritten bytes")
			}
		})
	}
}

func TestStashSelectedPathsRelativeToProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses newline filename")
	}
	dir, _ := workspaceStashRepo(t)
	writeRepoFile(t, dir, "nested/project.yaml", cleanProject)
	writeRepoFile(t, dir, "nested/with space\nand newline.txt", "base\n")
	gitAddCommit(t, dir, "nested")
	project := filepath.Join(dir, "nested", "project.yaml")
	writeRepoFile(t, dir, "nested/with space\nand newline.txt", "saved\n")
	writeRepoFile(t, dir, "project.yaml", strings.Replace(cleanProject, "Demo", "Kept", 1))
	ctx := context.Background()
	if _, err := Stash(ctx, StashRequest{Path: project, Paths: []string{"with space\nand newline.txt"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "nested", "with space\nand newline.txt"))
	if err != nil || string(data) != "base\n" {
		t.Fatalf("selected path: %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "project.yaml"))
	if err != nil || !strings.Contains(string(data), "Kept") {
		t.Fatal("unselected root project was stashed")
	}
	if _, err := Stash(ctx, StashRequest{Path: project, Paths: []string{"../../outside"}}); err == nil {
		t.Fatal("path outside repository accepted")
	}
}

func TestStashCommentDiffAndMetadataWatch(t *testing.T) {
	dir, project := workspaceStashRepo(t)
	writeRepoFile(t, dir, "project.yaml", cleanProject+"# comment\n")
	ctx := context.Background()
	if _, err := Stash(ctx, StashRequest{Path: project}); err != nil {
		t.Fatal(err)
	}
	list, err := Stashes(ctx, project)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	ch, err := StashChanges(ctx, StashActionRequest{Path: project, ID: list[0].ID})
	if err != nil || len(ch.Meta) != 1 || ch.Meta[0].Part != "file" {
		t.Fatalf("comment diff: %+v, %v", ch, err)
	}
	_, meta, _, err := WatchPaths(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"refs/stash", "logs/refs/stash"} {
		want, err := vcs.GitPath(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range meta {
			found = found || p == want
		}
		if !found {
			t.Fatalf("watch missing %s: %v", name, meta)
		}
	}
	calls := 0
	if _, err := StashAction(ctx, StashActionRequest{Path: project, ID: list[0].ID, Action: "drop", BeforeRewrite: func() { calls++ }}); err != nil || calls != 0 {
		t.Fatalf("drop rewrote files: calls %d, %v", calls, err)
	}
	if _, err := StashChanges(ctx, StashActionRequest{Path: project, ID: list[0].ID}); err == nil {
		t.Fatal("stale diff accepted")
	}
}

func TestStashDecodeErrorDoesNotExposeSavedSource(t *testing.T) {
	dir, project := workspaceStashRepo(t)
	writeRepoFile(t, dir, "project.yaml", "schemaVersion: \"1.0\"\nname: [\npassword: decode-literal\n")
	ctx := context.Background()
	if _, err := Stash(ctx, StashRequest{Path: project}); err != nil {
		t.Fatal(err)
	}
	list, err := Stashes(ctx, project)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	_, err = StashChanges(ctx, StashActionRequest{Path: project, ID: list[0].ID})
	var ve *verr.Error
	if !errors.As(err, &ve) || ve.Code != verr.CodeConfig || strings.Contains(err.Error(), "decode-literal") {
		t.Fatalf("decode error: %v", err)
	}
}

func TestStashMetadataSecrets(t *testing.T) {
	dir, project := workspaceStashRepo(t)
	vendor := "ghp_1234567890abcdef1234567890abcdef1234"
	opaque := "MySecretTokenValue1234567890abcd"
	gitRun(t, dir, "switch", "-c", "fix/"+vendor)
	writeRepoFile(t, dir, "project.yaml", cleanProject+"# changed\n")
	if _, err := Stash(context.Background(), StashRequest{Path: project, Message: opaque}); err != nil {
		t.Fatal(err)
	}
	list, err := Stashes(context.Background(), project)
	if err != nil || len(list) != 1 || list[0].Branch != secrets.Mask || list[0].Message != secrets.Mask {
		t.Fatalf("branch and opaque token not masked: %+v, %v", list, err)
	}
	id := list[0].ID
	for _, subject := range []string{vendor + ": debug", "api_key: supersecretvalue123"} {
		gitRun(t, dir, "update-ref", "-d", "refs/stash")
		gitRun(t, dir, "stash", "store", "-m", subject, id)
		list, err = Stashes(context.Background(), project)
		if err != nil || len(list) != 1 || list[0].Branch != "" || list[0].Message != secrets.Mask {
			t.Fatalf("custom subject not masked: %+v, %v", list, err)
		}
	}
	if got := redactStashText("debug: normal message"); got != "debug: normal message" {
		t.Fatalf("ordinary message masked: %q", got)
	}
}

func TestStashChangesClassifiesReferencedFiles(t *testing.T) {
	dir, project := workspaceStashRepo(t)
	p, err := model.Decode([]byte(cleanProject), project)
	if err != nil {
		t.Fatal(err)
	}
	p.Schema = &model.GraphQLSchema{File: "root.graphql"}
	suite := &p.Suites[0]
	suite.Schema = &model.GraphQLSchema{File: "suite.graphql"}
	suite.Setup = &model.Hook{Steps: []model.Step{{Request: map[string]any{"bodyFile": "setup.bin"}, Schema: &model.GraphQLSchema{File: "hook.graphql"}}}}
	suite.Teardown = &model.Hook{Steps: []model.Step{{Request: map[string]any{"multipart": []map[string]any{{"file": "teardown.bin"}}}}}}
	suite.Cases[0].Feed = &model.Feed{Type: "csv", Path: "feed.csv"}
	step := &suite.Cases[0].Steps[0]
	step.Schema = &model.GraphQLSchema{File: "step.graphql"}
	step.Request["bodyFile"] = "body.bin"
	step.Request["multipart"] = []map[string]any{{"file": "upload.bin"}}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, dir, "project.yaml", string(data))
	paths := []string{"root.graphql", "suite.graphql", "hook.graphql", "step.graphql", "setup.bin", "teardown.bin", "feed.csv", "body.bin", "upload.bin"}
	for _, name := range paths {
		writeRepoFile(t, dir, name, "base\n")
	}
	gitAddCommit(t, dir, "references")
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			writeRepoFile(t, dir, name, "changed\n")
			if _, err := Stash(context.Background(), StashRequest{Path: project}); err != nil {
				t.Fatal(err)
			}
			list, err := Stashes(context.Background(), project)
			if err != nil || len(list) != 1 {
				t.Fatalf("list: %+v, %v", list, err)
			}
			ch, err := StashChanges(context.Background(), StashActionRequest{Path: project, ID: list[0].ID})
			if err != nil || ch.OtherFiles != 0 || len(ch.Meta) != 1 || ch.Meta[0].Part != "file" {
				t.Fatalf("referenced file classification: %+v, %v", ch, err)
			}
			if _, err := StashAction(context.Background(), StashActionRequest{Path: project, ID: list[0].ID, Action: "drop"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStashFragmentsKeepMocksAndSchema(t *testing.T) {
	dir, project := workspaceStashRepo(t)
	writeRepoFile(t, dir, "project.yaml", cleanProject+"include: [parts/frag.yaml]\n")
	fragment := "schema: {file: old.graphql}\nmocks:\n  - method: GET\n    path: /old\n    body: old\n"
	writeRepoFile(t, dir, "parts/frag.yaml", fragment)
	gitAddCommit(t, dir, "fragment")
	writeRepoFile(t, dir, "parts/frag.yaml", strings.ReplaceAll(fragment, "old", "new"))
	if _, err := Stash(context.Background(), StashRequest{Path: project}); err != nil {
		t.Fatal(err)
	}
	list, err := Stashes(context.Background(), project)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	for _, side := range []struct{ rev, value string }{{list[0].ID + "^1", "old"}, {list[0].ID, "new"}} {
		p, files, err := loadStashProject(project, dir, side.rev, "")
		if err != nil || p.Schema == nil || p.Schema.File != side.value+".graphql" || len(p.Mocks) != 1 || p.Mocks[0].Path != "/"+side.value {
			t.Fatalf("fragment merge: %+v, %v", p, err)
		}
		if !files[side.value+".graphql"] {
			t.Fatalf("fragment schema not classified: %v", files)
		}
	}
	ch, err := StashChanges(context.Background(), StashActionRequest{Path: project, ID: list[0].ID})
	if err != nil || !hasReported(ch) || ch.OtherFiles != 0 {
		t.Fatalf("fragment change omitted: %+v, %v", ch, err)
	}
	// A root schema retains precedence over a fragment's schema.
	base := &model.Project{Schema: &model.GraphQLSchema{File: "root.graphql"}}
	frag := &model.Project{Schema: &model.GraphQLSchema{File: "fragment.graphql"}, Mocks: []model.Mock{{Path: "/fragment"}}}
	merged := mergeProjects([]*model.Project{base, frag})
	if merged.Schema.File != "root.graphql" || len(merged.Mocks) != 1 {
		t.Fatalf("root precedence: %+v", merged)
	}
}

func TestStashThroughSymlinkWithMissingDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges differ on Windows")
	}
	dir, _ := workspaceStashRepo(t)
	link := filepath.Join(t.TempDir(), "linked-repo")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(link, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject+"include: [sub/deep/frag.yaml]\n")
	writeRepoFile(t, dir, "sub/deep/frag.yaml", "name: Fragment\n")
	if _, err := Stash(context.Background(), StashRequest{Path: project, IncludeUntracked: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(link, "sub")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stash left fragment directory: %v", err)
	}
	list, err := Stashes(context.Background(), project)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if _, err := StashChanges(context.Background(), StashActionRequest{Path: project, ID: list[0].ID}); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, dir, "sub/deep/a.txt", "base\n")
	gitAddCommit(t, dir, "nested tracked file")
	gitRun(t, dir, "rm", "sub/deep/a.txt")
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if _, err := Stash(context.Background(), StashRequest{Path: project, Paths: []string{"sub/deep/a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(link, "sub/deep/a.txt")); err != nil || string(data) != "base\n" {
		t.Fatalf("selected deletion through symlink: %q, %v", data, err)
	}
}
