package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/workspace"
)

func cmdFmt(args []string) int {
	fs := flag.NewFlagSet("fmt", flag.ContinueOnError)
	check := fs.Bool("check", false, "list files that would change without writing them")
	staged := fs.Bool("staged", false, "with --check: check the git index (what the next commit records), not the working tree")
	jsonOut := fs.Bool("json", false, "emit structured JSON status")
	allowExternal := fs.Bool("allow-external-files", false, allowExternalUsage)
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) == 0 {
		pos = []string{"project.yaml"}
	}

	if *staged {
		if !*check {
			fmt.Fprintln(os.Stderr, "fmt: --staged needs --check (fmt never rewrites the index)")
			return exitUsage
		}
		return fmtCheckStaged(pos, *jsonOut)
	}

	seen := map[string]bool{}
	changed := []string{}
	for _, root := range pos {
		files, err := workspace.ProjectFilesWith(root, *allowExternal)
		if err != nil {
			return failJSON(*jsonOut, exitConfig, "fmt", err)
		}
		for _, path := range files {
			if seen[path] {
				continue
			}
			seen[path] = true
			data, err := os.ReadFile(path)
			if err != nil {
				return failJSON(*jsonOut, exitConfig, "fmt", err)
			}
			format := model.FormatYAML
			if strings.HasSuffix(strings.ToLower(path), ".json") {
				format = model.FormatJSON
			}
			formatted, err := format(data)
			if err != nil {
				return failJSON(*jsonOut, exitConfig, "fmt", err)
			}
			if string(formatted) == string(data) {
				continue
			}
			changed = append(changed, path)
			if *check {
				continue
			}
			if err := fsutil.WriteKeepingMode(path, formatted); err != nil {
				return failJSON(*jsonOut, exitInternal, "fmt", err)
			}
		}
	}

	if *jsonOut {
		emitJSON(map[string]any{"ok": !*check || len(changed) == 0, "check": *check, "changed": changed})
	} else {
		for _, path := range changed {
			fmt.Println(path)
		}
	}
	if *check && len(changed) > 0 {
		return exitTestFailed
	}
	return exitOK
}

// fmtCheckStaged is fmt --check --staged: it formats the staged content of
// each project file (read from the git index, like scan-secrets --staged),
// so a partially staged commit is judged by what it records.
func fmtCheckStaged(roots []string, jsonOut bool) int {
	changed := []string{}
	for _, root := range roots {
		read, err := workspace.StagedReader(filepath.Dir(root))
		if err != nil {
			return failJSON(jsonOut, exitConfig, "git", err)
		}
		err = workspace.WalkProject(root, read, func(file, _ string, data []byte) error {
			format := model.FormatYAML
			if strings.HasSuffix(strings.ToLower(file), ".json") {
				format = model.FormatJSON
			}
			formatted, err := format(data)
			if err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			if string(formatted) != string(data) {
				changed = append(changed, file)
			}
			return nil
		})
		if err != nil {
			return failJSON(jsonOut, exitConfig, "fmt", err)
		}
	}
	if jsonOut {
		emitJSON(map[string]any{"ok": len(changed) == 0, "check": true, "staged": true, "changed": changed})
	} else {
		for _, path := range changed {
			fmt.Println(path)
		}
		if len(changed) > 0 {
			fmt.Fprintln(os.Stderr, "staged project files are not formatted; run verity fmt and git add them")
		}
	}
	if len(changed) > 0 {
		return exitTestFailed
	}
	return exitOK
}
