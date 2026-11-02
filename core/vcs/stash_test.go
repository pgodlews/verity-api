package vcs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

func stashRepo(t *testing.T) string {
	t.Helper()
	skipNoGit(t)
	useIdentity(t)
	dir := t.TempDir()
	gitInit(t, dir)
	writeFile(t, dir, "tracked.txt", "base\n")
	writeFile(t, dir, ".gitignore", "ignored.txt\n")
	gitAddCommit(t, dir, "base")
	return dir
}

func newestStash(t *testing.T, dir string) Stash {
	t.Helper()
	list, err := Stashes(context.Background(), dir)
	if err != nil || len(list) == 0 {
		t.Fatalf("Stashes = %+v, %v", list, err)
	}
	return list[0]
}

func TestStashTrackedUntrackedAndIgnored(t *testing.T) {
	for _, untracked := range []bool{false, true} {
		t.Run(stashMode(untracked), func(t *testing.T) {
			dir := stashRepo(t)
			ctx := context.Background()
			writeFile(t, dir, "tracked.txt", "edited\n")
			gitRun(t, dir, "add", "--", "tracked.txt")
			writeFile(t, dir, "new file.txt", "new\n")
			writeFile(t, dir, "ignored.txt", "ignored\n")
			if err := StashPush(ctx, dir, nil, untracked, "-- keep work"); err != nil {
				t.Fatal(err)
			}
			entry := newestStash(t, dir)
			if entry.Index != 0 || entry.Branch != "main" || entry.Message != "-- keep work" || !fullObjectID(entry.ID) {
				t.Fatalf("metadata: %+v", entry)
			}
			if _, err := time.Parse(time.RFC3339, entry.Date); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
			if err != nil || string(data) != "base\n" {
				t.Fatalf("tracked = %q, %v", data, err)
			}
			_, err = os.Stat(filepath.Join(dir, "new file.txt"))
			if untracked != errors.Is(err, os.ErrNotExist) {
				t.Fatalf("untracked file: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "ignored.txt")); err != nil {
				t.Fatal("ignored file was removed")
			}
			out, err := StashApply(ctx, dir, entry.ID, true)
			if err != nil || out.Status != "applied" {
				t.Fatalf("pop: %+v, %v", out, err)
			}
			list, err := Stashes(ctx, dir)
			if err != nil || len(list) != 0 {
				t.Fatalf("pop kept stash: %+v, %v", list, err)
			}
			data, err = os.ReadFile(filepath.Join(dir, "tracked.txt"))
			if err != nil || string(data) != "edited\n" {
				t.Fatalf("restored = %q, %v", data, err)
			}
		})
	}
}

func stashMode(b bool) string {
	if b {
		return "untracked"
	}
	return "tracked"
}

func TestStashLiteralPaths(t *testing.T) {
	for _, name := range []string{"file with spaces.txt", "file[1].txt", "-u", ":(glob)*", "line\nbreak.txt", "trailing\n"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.ContainsAny(name, ":\n") {
				t.Skip("filename is not supported on Windows")
			}
			dir := stashRepo(t)
			writeFile(t, dir, name, "base\n")
			writeFile(t, dir, "file1.txt", "base\n")
			gitAddCommit(t, dir, "add paths")
			writeFile(t, dir, name, "chosen\n")
			writeFile(t, dir, "file1.txt", "keep\n")
			writeFile(t, dir, "tracked.txt", "keep\n")
			ctx := context.Background()
			if err := StashPush(ctx, dir, []string{name}, false, "selected"); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"file1.txt", "tracked.txt"} {
				data, err := os.ReadFile(filepath.Join(dir, p))
				if err != nil || string(data) != "keep\n" {
					t.Fatalf("unselected %q: %q, %v", p, data, err)
				}
			}
			id := newestStash(t, dir).ID
			files, _, err := StashFiles(ctx, dir, id)
			if err != nil || len(files) != 1 || files[0] != name {
				t.Fatalf("files: %#v, %v", files, err)
			}
			blob, err := ReadAt(dir, id, name)
			if err != nil || string(blob) != "chosen\n" {
				t.Fatalf("stash blob: %q, %v", blob, err)
			}
			if _, err := StashApply(ctx, dir, id, false); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || string(data) != "chosen\n" {
				t.Fatalf("apply: %q, %v", data, err)
			}
		})
	}
}

func TestStashEmptyAndInvalidSelections(t *testing.T) {
	dir := stashRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "new.txt", "new\n")
	writeFile(t, dir, "ignored.txt", "ignored\n")
	for _, tc := range []struct {
		paths     []string
		untracked bool
	}{
		{nil, false}, {[]string{"tracked.txt"}, true}, {[]string{"missing.txt"}, true},
		{[]string{"ignored.txt"}, true}, {[]string{"../outside"}, true},
		{[]string{"bad\x00name"}, true}, {[]string{"."}, false},
	} {
		err := StashPush(ctx, dir, tc.paths, tc.untracked, "")
		var ve *verr.Error
		if !errors.As(err, &ve) || ve.Code != verr.CodeValidation {
			t.Fatalf("%q: expected validation error, got %v", tc.paths, err)
		}
	}
	list, err := Stashes(ctx, dir)
	if err != nil || len(list) != 0 {
		t.Fatalf("empty created a stash: %+v, %v", list, err)
	}
	if err := StashPush(ctx, dir, []string{"new.txt"}, true, ""); err != nil {
		t.Fatal(err)
	}
	files, untracked, err := StashFiles(ctx, dir, newestStash(t, dir).ID)
	if err != nil || !untracked || len(files) != 1 || files[0] != "new.txt" {
		t.Fatalf("untracked tree: %#v, %v, %v", files, untracked, err)
	}
}

func TestStashIDSurvivesPositionChangesAndStaleIDIsRefused(t *testing.T) {
	dir := stashRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "tracked.txt", "first\n")
	if err := StashPush(ctx, dir, nil, false, "first"); err != nil {
		t.Fatal(err)
	}
	first := newestStash(t, dir)
	writeFile(t, dir, "tracked.txt", "second\n")
	if err := StashPush(ctx, dir, nil, false, "second"); err != nil {
		t.Fatal(err)
	}
	second := newestStash(t, dir)
	if _, err := StashApply(ctx, dir, first.ID, false); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(applied) != "first\n" {
		t.Fatalf("apply used a shifted position: %q, %v", applied, err)
	}
	gitRun(t, dir, "restore", "--worktree", "--", ":(literal)tracked.txt")
	if err := StashDrop(ctx, dir, first.ID); err != nil {
		t.Fatal(err)
	}
	if newestStash(t, dir).ID != second.ID {
		t.Fatal("drop used the old list position")
	}
	for _, action := range []string{"apply", "pop", "drop", "diff"} {
		var err error
		switch action {
		case "drop":
			err = StashDrop(ctx, dir, first.ID)
		case "diff":
			_, _, err = StashFiles(ctx, dir, first.ID)
		default:
			_, err = StashApply(ctx, dir, first.ID, action == "pop")
		}
		var ve *verr.Error
		if !errors.As(err, &ve) || ve.Code != verr.CodeConflict {
			t.Fatalf("stale %s: %v", action, err)
		}
	}
	if _, err := StashApply(ctx, dir, "stash@{0}", false); err == nil {
		t.Fatal("accepted positional selector")
	}
	if _, err := StashApply(ctx, dir, second.ID, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(data) != "second\n" {
		t.Fatalf("wrong stash applied: %q, %v", data, err)
	}
}

func TestStashConflictKeepsStash(t *testing.T) {
	dir := stashRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "tracked.txt", "stash side\n")
	if err := StashPush(ctx, dir, nil, false, "conflict"); err != nil {
		t.Fatal(err)
	}
	id := newestStash(t, dir).ID
	writeFile(t, dir, "tracked.txt", "committed side\n")
	gitAddCommit(t, dir, "other side")
	out, err := StashApply(ctx, dir, id, true)
	if err != nil || out.Status != "conflict" || len(out.Conflicted) != 1 || out.Conflicted[0] != "tracked.txt" {
		t.Fatalf("pop conflict: %+v, %v", out, err)
	}
	if newestStash(t, dir).ID != id {
		t.Fatal("conflicting pop dropped stash")
	}
	if _, err := StashApply(ctx, dir, id, false); err == nil {
		t.Fatal("accepted a second apply over unmerged files")
	}
	data, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || !strings.Contains(string(data), "<<<<<<<") {
		t.Fatalf("conflict markers absent: %q, %v", data, err)
	}
}

func TestStashDisablesRepoProgramsAndHonoursCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses shell hook fixtures")
	}
	dir := stashRepo(t)
	ctx := context.Background()
	marker := filepath.Join(dir, ".git", "program-ran")
	script := filepath.Join(dir, ".git", "hostile-program")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+shQuote(marker)+"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".gitattributes", "tracked.txt filter=hostile diff=hostile\n")
	gitAddCommit(t, dir, "attributes")
	gitRun(t, dir, "config", "core.fsmonitor", script)
	gitRun(t, dir, "config", "filter.hostile.clean", script)
	gitRun(t, dir, "config", "filter.hostile.smudge", script)
	gitRun(t, dir, "config", "diff.hostile.command", script)
	gitRun(t, dir, "config", "diff.hostile.textconv", script)
	gitRun(t, dir, "config", "diff.external", script)
	hooks := filepath.Join(dir, ".git", "hostile-hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte("#!/bin/sh\ntouch "+shQuote(marker)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "core.hooksPath", hooks)
	writeFile(t, dir, "tracked.txt", "changed\n")
	if err := StashPush(ctx, dir, nil, false, "safe"); err != nil {
		t.Fatal(err)
	}
	id := newestStash(t, dir).ID
	if _, _, err := StashFiles(ctx, dir, id); err != nil {
		t.Fatal(err)
	}
	if _, err := StashApply(ctx, dir, id, false); err != nil {
		t.Fatal(err)
	}
	if err := StashDrop(ctx, dir, id); err != nil {
		t.Fatal(err)
	}
	if err := StashPush(ctx, dir, nil, false, "pop hooks"); err != nil {
		t.Fatal(err)
	}
	if _, err := StashApply(ctx, dir, newestStash(t, dir).ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repo program ran: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := StashPush(cancelled, dir, nil, false, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestStashFailedUntrackedRestoreNeverDrops(t *testing.T) {
	dir := stashRepo(t)
	ctx := context.Background()
	writeFile(t, dir, "new.txt", "saved untracked\n")
	if err := StashPush(ctx, dir, nil, true, "untracked"); err != nil {
		t.Fatal(err)
	}
	id := newestStash(t, dir).ID
	writeFile(t, dir, "new.txt", "keep worktree\n")
	if _, err := StashApply(ctx, dir, id, true); err == nil {
		t.Fatal("pop overwrote an existing untracked file")
	}
	if newestStash(t, dir).ID != id {
		t.Fatal("failed pop dropped stash")
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil || string(data) != "keep worktree\n" {
		t.Fatalf("existing untracked file: %q, %v", data, err)
	}
}

// Each fixture also has unrelated partial staging, additions, deletions, and
// a rename. Neither their saved index blobs nor their working bytes may move.
func TestStashSelectedIndexIsolation(t *testing.T) {
	for _, kind := range []string{"modified", "deleted", "renamed", "failed"} {
		t.Run(kind, func(t *testing.T) {
			dir := stashRepo(t)
			for _, name := range []string{"partial", "deleted", "old"} {
				writeFile(t, dir, name, "original "+name+"\n")
			}
			gitAddCommit(t, dir, "unselected bases")
			writeFile(t, dir, "partial", "staged\n")
			writeFile(t, dir, "added", "staged addition\n")
			gitRun(t, dir, "add", "partial", "added")
			writeFile(t, dir, "partial", "unstaged on top\n")
			gitRun(t, dir, "rm", "deleted")
			gitRun(t, dir, "mv", "old", "new")
			writeFile(t, dir, "new", "unstaged rename edit\n")
			unselected := []string{"partial", "added", "deleted", "old", "new"}
			var specs []string
			for _, name := range unselected {
				specs = append(specs, ":(literal)"+name)
			}
			indexArgs := append([]string{"ls-files", "-s", "-z", "--"}, specs...)
			indexBefore := gitRun(t, dir, indexArgs...)
			workBefore := map[string]string{}
			for _, name := range []string{"partial", "added", "new"} {
				workBefore[name] = string(mustRead(t, filepath.Join(dir, name)))
			}
			selected := "tracked.txt"
			wantFiles := "tracked.txt"
			switch kind {
			case "deleted":
				gitRun(t, dir, "rm", "tracked.txt")
			case "renamed":
				gitRun(t, dir, "mv", "tracked.txt", "renamed.txt")
				selected, wantFiles = "renamed.txt", "renamed.txt,tracked.txt"
			case "failed":
				gitRun(t, dir, "rm", "tracked.txt")
				clearIdentity(t)
			default:
				writeFile(t, dir, selected, "selected\n")
			}
			allBefore := gitRun(t, dir, "ls-files", "-s", "-z")
			err := StashPush(context.Background(), dir, []string{selected}, false, kind)
			if kind == "failed" {
				var identity *IdentityError
				if !errors.As(err, &identity) {
					t.Fatalf("missing identity: %v", err)
				}
				if after := gitRun(t, dir, "ls-files", "-s", "-z"); after != allBefore {
					t.Fatal("failed push changed the index")
				}
				if _, err := os.Stat(filepath.Join(dir, selected)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed push restored selected deletion on disk")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				id := newestStash(t, dir).ID
				files, _, err := StashFiles(context.Background(), dir, id)
				sort.Strings(files)
				if err != nil || strings.Join(files, ",") != wantFiles {
					t.Fatalf("saved paths = %q, %v", files, err)
				}
				if diff := gitRun(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", id+"^1", id+"^2"); strings.Contains(diff, "partial") || strings.Contains(diff, "added") || strings.Contains(diff, "deleted") || strings.Contains(diff, "new") {
					t.Fatalf("unselected staged blobs in stash index: %q", diff)
				}
				if got := string(mustRead(t, filepath.Join(dir, "tracked.txt"))); got != "base\n" {
					t.Fatalf("selected base not restored: %q", got)
				}
				if kind == "renamed" {
					if _, err := os.Stat(filepath.Join(dir, selected)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("rename destination remains after stash")
					}
				}
			}
			if after := gitRun(t, dir, indexArgs...); after != indexBefore {
				t.Fatalf("unselected index changed:\nbefore %q\nafter  %q", indexBefore, after)
			}
			for name, before := range workBefore {
				if after := string(mustRead(t, filepath.Join(dir, name))); after != before {
					t.Fatalf("unselected working bytes changed: %s", name)
				}
			}
			for _, name := range []string{"deleted", "old"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unselected missing path restored: %s", name)
				}
			}
		})
	}
}

func TestStashSubjectParsing(t *testing.T) {
	dir := stashRepo(t)
	writeFile(t, dir, "tracked.txt", "save\n")
	if err := StashPush(context.Background(), dir, nil, false, "initial"); err != nil {
		t.Fatal(err)
	}
	id := newestStash(t, dir).ID
	for _, tc := range []struct{ subject, branch, message string }{
		{"api_key: supersecretvalue123", "", "api_key: supersecretvalue123"},
		{"custom: debug: detail", "", "custom: debug: detail"},
		{"On feature: debug: detail", "feature", "debug: detail"},
		{"WIP on feature: abc123 summary", "feature", "abc123 summary"},
		{"On no separator", "", "On no separator"},
	} {
		gitRun(t, dir, "update-ref", "-d", "refs/stash")
		gitRun(t, dir, "stash", "store", "-m", tc.subject, id)
		got := newestStash(t, dir)
		if got.Branch != tc.branch || got.Message != tc.message {
			t.Fatalf("%q: %+v", tc.subject, got)
		}
	}
}

func TestStashIgnoresNestedRepositoriesAndSubmodules(t *testing.T) {
	for _, kind := range []string{"nested", "dirty submodule", "staged submodule"} {
		t.Run(kind, func(t *testing.T) {
			dir := stashRepo(t)
			if kind == "nested" {
				nested := filepath.Join(dir, "nested")
				if err := os.Mkdir(nested, 0o755); err != nil {
					t.Fatal(err)
				}
				gitInit(t, nested)
				writeFile(t, nested, "file", "nested\n")
				gitAddCommit(t, nested, "nested")
			} else {
				source := stashRepo(t)
				gitRun(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", source, "sub")
				gitAddCommit(t, dir, "submodule")
				writeFile(t, filepath.Join(dir, "sub"), "tracked.txt", "sub edit\n")
				if kind == "staged submodule" {
					gitAddCommit(t, filepath.Join(dir, "sub"), "new sub commit")
					gitRun(t, dir, "add", "sub")
				}
			}
			before := gitRun(t, dir, "status", "--porcelain=v2", "-z", "--untracked-files=all")
			index := gitRun(t, dir, "ls-files", "-s", "-z")
			for _, paths := range [][]string{nil, {"sub"}, {"nested"}} {
				err := StashPush(context.Background(), dir, paths, true, "empty")
				var ve *verr.Error
				if !errors.As(err, &ve) || ve.Code != verr.CodeValidation {
					t.Fatalf("%v: expected nothing to stash, got %v", paths, err)
				}
			}
			list, err := Stashes(context.Background(), dir)
			if err != nil || len(list) != 0 {
				t.Fatalf("empty stash created: %+v, %v", list, err)
			}
			if after := gitRun(t, dir, "status", "--porcelain=v2", "-z", "--untracked-files=all"); before != after {
				t.Fatal("refused stash changed working state")
			}
			if after := gitRun(t, dir, "ls-files", "-s", "-z"); index != after {
				t.Fatal("refused stash changed index")
			}
		})
	}
}

func TestStashesDoNotVerifySignatures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell payload fixture")
	}
	dir := stashRepo(t)
	writeFile(t, dir, "tracked.txt", "signed stash\n")
	if err := StashPush(context.Background(), dir, nil, false, "signed"); err != nil {
		t.Fatal(err)
	}
	id := newestStash(t, dir).ID
	commit := gitRun(t, dir, "cat-file", "commit", id)
	commit = strings.Replace(commit, "\n\n", "\ngpgsig -----BEGIN PGP SIGNATURE-----\n fake\n -----END PGP SIGNATURE-----\n\n", 1)
	cmd := exec.Command("git", "-C", dir, "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(commit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("signed fixture: %s, %v", out, err)
	}
	signed := strings.TrimSpace(string(out))
	gitRun(t, dir, "stash", "store", "-m", "On main: signed", signed)
	marker := filepath.Join(dir, ".git", "gpg-ran")
	script := filepath.Join(dir, ".git", "gpg-payload")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+shQuote(marker)+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "log.showSignature", "true")
	gitRun(t, dir, "config", "gpg.program", script)
	entry := newestStash(t, dir)
	if entry.ID != signed || entry.Message != "signed" {
		t.Fatalf("signed list parsing: %+v", entry)
	}
	if _, _, err := StashFiles(context.Background(), dir, signed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stash list executed gpg: %v", err)
	}
}

func TestStashConflictDisablesMergeDrivers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell payload fixture")
	}
	for _, scope := range []string{"--local", "--worktree"} {
		for _, pop := range []bool{false, true} {
			t.Run(scope+stashMode(pop), func(t *testing.T) {
				dir := stashRepo(t)
				writeFile(t, dir, ".gitattributes", "tracked.txt merge=hostile.dotted\n")
				gitAddCommit(t, dir, "attributes")
				writeFile(t, dir, "tracked.txt", "stash side\n")
				if err := StashPush(context.Background(), dir, nil, false, "conflict"); err != nil {
					t.Fatal(err)
				}
				id := newestStash(t, dir).ID
				writeFile(t, dir, "tracked.txt", "other side\n")
				gitAddCommit(t, dir, "other")
				marker := filepath.Join(dir, ".git", "merge-ran")
				script := filepath.Join(dir, ".git", "merge-payload")
				if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+shQuote(marker)+"\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				if scope == "--worktree" {
					gitRun(t, dir, "config", "extensions.worktreeConfig", "true")
				}
				gitRun(t, dir, "config", scope, "merge.hostile.dotted.driver", script+" %O %A %B")
				out, err := StashApply(context.Background(), dir, id, pop)
				if err != nil || out.Status != "conflict" {
					t.Fatalf("apply: %+v, %v", out, err)
				}
				if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stash ran merge driver: %v", err)
				}
				if got := string(mustRead(t, filepath.Join(dir, "tracked.txt"))); !strings.Contains(got, "<<<<<<<") {
					t.Fatalf("text conflict markers missing: %q", got)
				}
				if newestStash(t, dir).ID != id {
					t.Fatal("conflict dropped stash")
				}
			})
		}
	}
}
