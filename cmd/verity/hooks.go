package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pgodlews/verity-api/core/workspace"
)

func cmdHooks(args []string) int {
	if len(args) == 0 {
		hooksUsage()
		return exitUsage
	}
	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("hooks install", flag.ContinueOnError)
		force := fs.Bool("force", false, "back up and replace a foreign pre-commit hook")
		checkFmt := fs.Bool("fmt", false, "also check project formatting before commit")
		jsonOut := fs.Bool("json", false, "emit structured JSON status")
		fs.SetOutput(os.Stderr)
		pos, err := parsePermuted(fs, args[1:])
		if err != nil {
			return exitUsage
		}
		if len(pos) > 1 {
			hooksUsage()
			return exitUsage
		}
		project := "project.yaml"
		if len(pos) == 1 {
			project = pos[0]
		}
		path, err := workspace.InstallHook(workspace.HookOptions{Project: project, Force: *force, Fmt: *checkFmt})
		if err != nil {
			return failJSON(*jsonOut, exitConfig, "hooks install", err)
		}
		if *jsonOut {
			emitJSON(map[string]any{"ok": true, "hook": path})
		} else {
			fmt.Printf("Installed %s\n", path)
		}
		return exitOK
	case "uninstall":
		fs := flag.NewFlagSet("hooks uninstall", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "emit structured JSON status")
		fs.SetOutput(os.Stderr)
		pos, err := parsePermuted(fs, args[1:])
		if err != nil {
			return exitUsage
		}
		if len(pos) > 1 {
			hooksUsage()
			return exitUsage
		}
		project := "project.yaml"
		if len(pos) == 1 {
			project = pos[0]
		}
		path, err := workspace.UninstallHook(project)
		if err != nil {
			return failJSON(*jsonOut, exitConfig, "hooks uninstall", err)
		}
		if *jsonOut {
			emitJSON(map[string]any{"ok": true, "hook": path})
		} else {
			fmt.Printf("Uninstalled %s\n", path)
		}
		return exitOK
	default:
		hooksUsage()
		return exitUsage
	}
}

func hooksUsage() {
	fmt.Fprintln(os.Stderr, "usage: verity hooks install [--force] [--fmt] [project.yaml]")
	fmt.Fprintln(os.Stderr, "       verity hooks uninstall [project.yaml]")
}
