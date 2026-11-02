package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pgodlews/verity-api/core/workspace"
)

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace an existing .env.example")
	jsonOut := fs.Bool("json", false, "emit structured JSON status")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) > 1 {
		fmt.Fprintln(os.Stderr, "init: usage: verity init [--force] [dir]")
		return exitUsage
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	result, err := workspace.Init(workspace.InitOptions{Dir: dir, Force: *force})
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "init", err)
	}
	if *jsonOut {
		emitJSON(map[string]any{"ok": true, "result": result})
	} else {
		for _, path := range result.Changed {
			fmt.Println(path)
		}
	}
	return exitOK
}
