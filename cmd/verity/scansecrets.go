package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pgodlews/verity-api/core/secretscan"
	"github.com/pgodlews/verity-api/core/workspace"
)

// cmdScanSecrets is `verity scan-secrets [--staged] [--json] [project.yaml]`.
// It exits 0 when the project is clean, exitTestFailed (1) when it holds
// plaintext credentials, and exitUsage/exitConfig otherwise. --staged scans
// what `git commit` would record (the index) and also refuses staged .env
// files; it is what the pre-commit hook runs. The index reader lives in
// core/vcs and is shared with the desktop commit. Git is the installed git CLI.
func cmdScanSecrets(args []string) int {
	fs := flag.NewFlagSet("scan-secrets", flag.ContinueOnError)
	staged := fs.Bool("staged", false, "scan the git index (what the next commit records) instead of the working tree")
	jsonOut := fs.Bool("json", false, "emit structured JSON result")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	path := "project.yaml"
	if len(pos) > 0 {
		path = pos[0]
	}
	if len(pos) > 1 {
		fmt.Fprintln(os.Stderr, "scan-secrets: one project file at a time")
		return exitUsage
	}

	var found []secretscan.Finding
	var envFiles []string
	if *staged {
		scan, err := workspace.ScanStaged(path)
		if err != nil {
			return failJSON(*jsonOut, exitConfig, "git", err)
		}
		found = scan.Findings
		envFiles = scan.EnvFiles
	} else {
		found, err = workspace.ScanSecrets(path, nil)
		if err != nil {
			return failJSON(*jsonOut, exitConfig, "scan", err)
		}
	}
	if found == nil {
		found = []secretscan.Finding{}
	}

	clean := len(found) == 0 && len(envFiles) == 0
	if *jsonOut {
		emitJSON(map[string]any{"ok": clean, "findings": found, "stagedEnvFiles": envFiles})
	} else {
		for _, f := range found {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", f.File, f.Line, f.Message())
		}
		for _, f := range envFiles {
			fmt.Fprintf(os.Stderr, "%s: dotenv files hold real secret values and must not be committed (git rm --cached %s)\n", f, f)
		}
		if clean {
			fmt.Printf("OK: no plaintext secrets in %s\n", path)
		} else {
			fmt.Fprintf(os.Stderr, "%d plaintext secret(s) found; replace each with ${env:NAME} and keep the value in the environment or an untracked .env file\n", len(found)+len(envFiles))
		}
	}
	if !clean {
		return exitTestFailed
	}
	return exitOK
}
