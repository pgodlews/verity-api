package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
)

// Build metadata, overridable at build time via -ldflags:
//
//	-X main.version=v0.1.0 -X main.commit=abc123 -X main.date=2025-01-01
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit structured JSON version info")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *jsonOut {
		emitJSON(map[string]any{
			"name":    "verity",
			"product": "Verity",
			"version": version,
			"commit":  commit,
			"date":    date,
			"go":      runtime.Version(),
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
		})
		return exitOK
	}
	fmt.Printf("verity (Verity) %s\n  commit: %s\n  built:  %s\n  go:     %s %s/%s\n",
		version, commit, date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return exitOK
}
