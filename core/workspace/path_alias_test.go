package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/vcs"
	"github.com/pgodlews/verity-api/core/verr"
)

func aliasRepository(t *testing.T) (string, string) {
	t.Helper()
	if !vcs.Available() {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitInit(t, dir)
	link := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(dir, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks are unavailable")
		}
		t.Fatal(err)
	}
	return dir, link
}

func TestAliasedCommitScansRootAndFragments(t *testing.T) {
	for _, file := range []string{"project.yaml", "parts/api.yaml"} {
		t.Run(file, func(t *testing.T) {
			dir, alias := aliasRepository(t)
			useCommitIdentity(t)
			project := filepath.Join(alias, "project.yaml")
			writeRepoFile(t, dir, "project.yaml", cleanProject+"include: [parts/api.yaml]\n")
			writeRepoFile(t, dir, "parts/api.yaml", "name: fragment\n")
			writeRepoFile(t, dir, "notes.txt", "initial\n")
			gitAddCommit(t, dir, "initial")
			// Preserve a different already-staged project blob and an unrelated entry.
			data, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			writeRepoFile(t, dir, file, string(data)+"# staged first\n")
			writeRepoFile(t, dir, "notes.txt", "already staged\n")
			gitRun(t, dir, "add", "--", file, "notes.txt")
			before, err := vcs.SnapshotIndex(context.Background(), dir, []string{file, "notes.txt"})
			if err != nil {
				t.Fatal(err)
			}
			// Generated at runtime; never use a real credential in a reproduction.
			credential := make([]byte, 24)
			if _, err := rand.Read(credential); err != nil {
				t.Fatal(err)
			}
			literal := hex.EncodeToString(credential)
			writeRepoFile(t, dir, file, string(data)+"auth:\n  - name: test\n    type: bearer\n    config:\n      token: "+literal+"\n")
			_, err = Commit(context.Background(), CommitRequest{Path: project, Message: "must refuse", Paths: []string{file}})
			var ve *verr.Error
			if !errors.As(err, &ve) || ve.Code != verr.CodeValidation {
				t.Fatalf("expected validation refusal, got %v", err)
			}
			if strings.Contains(err.Error(), literal) {
				t.Fatal("refusal exposed credential")
			}
			if gitSubject(t, dir) != "initial" {
				t.Fatal("HEAD changed")
			}
			after, err := vcs.SnapshotIndex(context.Background(), dir, []string{file, "notes.txt"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed the previous index")
			}
			// Selecting only an unrelated file must not commit the staged project edit.
			if _, err := Commit(context.Background(), CommitRequest{Path: project, Message: "notes only", Paths: []string{"notes.txt"}}); err != nil {
				t.Fatal(err)
			}
			committed, err := vcs.ReadAt(dir, "HEAD", file)
			if err != nil {
				t.Fatal(err)
			}
			if string(committed) != string(data) {
				t.Fatal("unselected project content was committed")
			}
		})
	}
}

func TestAliasedStatusHooksAndDirtySwitch(t *testing.T) {
	dir, alias := aliasRepository(t)
	project := filepath.Join(alias, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject+"include: [parts/api.yaml]\n")
	writeRepoFile(t, dir, "parts/api.yaml", "name: fragment\n")
	gitAddCommit(t, dir, "initial")
	gitRun(t, dir, "branch", "other")
	writeRepoFile(t, dir, "project.yaml", cleanProject+"include: [parts/api.yaml]\n# root edit\n")
	writeRepoFile(t, dir, "parts/api.yaml", "name: fragment\n# fragment edit\n")
	st, err := GitStatus(project)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 2 {
		t.Fatalf("files: %+v", st.Files)
	}
	for _, f := range st.Files {
		if !f.ProjectFile || strings.HasPrefix(f.Path, "../") {
			t.Fatalf("unrecognized project file: %+v", f)
		}
	}
	result, err := Switch(context.Background(), SwitchRequest{Path: project, Branch: "other"})
	if err != nil || result.Status != "dirty" || len(result.Files) != 2 {
		t.Fatalf("switch: %+v, %v", result, err)
	}
	if currentBranchName(t, dir) != "main" {
		t.Fatal("dirty switch changed branch")
	}
	first, err := InstallHook(HookOptions{Project: project, Fmt: true})
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "--staged 'project.yaml'") || strings.Contains(string(script), alias) {
		t.Fatal("hook did not use repository-relative project")
	}
	if _, err := InstallHook(HookOptions{Project: project, Fmt: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallHook(project); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if _, _, err := repoPaths(project, []string{outside}); err == nil {
		t.Fatal("outside path was accepted")
	}
}

func TestAliasResolutionFailsClosed(t *testing.T) {
	dir, alias := aliasRepository(t)
	useCommitIdentity(t)
	project := filepath.Join(alias, "project.yaml")
	writeRepoFile(t, dir, "project.yaml", cleanProject)
	gitAddCommit(t, dir, "initial")
	writeRepoFile(t, dir, "project.yaml", cleanProject+"include: [missing.yaml]\n")
	_, err := Commit(context.Background(), CommitRequest{Path: project, Message: "must refuse", Paths: []string{"project.yaml"}})
	var ve *verr.Error
	if !errors.As(err, &ve) || ve.Code != verr.CodeConfig {
		t.Fatalf("expected config refusal, got %v", err)
	}
	if gitSubject(t, dir) != "initial" || gitNameOnly(t, dir, "diff", "--cached", "--name-only") != "" {
		t.Fatal("failed scan changed HEAD or the index")
	}
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if _, _, err := projectDir(filepath.Join(loop, "project.yaml")); err == nil {
		t.Fatal("unresolvable alias was accepted")
	}
}
