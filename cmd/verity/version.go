package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
)

// Build metadata, overridable at build time via -ldflags:
//
//	-X main.version=v0.1.0 -X main.commit=abc123 -X main.date=2025-01-01
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// buildMetadata returns the version, commit, and date to report. Values set
// with -ldflags win; otherwise (go install .../cmd/verity@vX.Y.Z, or go build
// in a Git checkout) the module version and VCS stamps the Go toolchain
// records in the binary fill them in.
func buildMetadata(info *debug.BuildInfo, ok bool) (v, c, d string) {
	v, c, d = version, commit, date
	if !ok || info == nil {
		return v, c, d
	}
	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if c == "none" && s.Value != "" {
				c = s.Value
				if len(c) > 12 {
					c = c[:12]
				}
			}
		case "vcs.time":
			if d == "unknown" && s.Value != "" {
				d = s.Value
			}
		}
	}
	return v, c, d
}

func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit structured JSON version info")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	version, commit, date := buildMetadata(debug.ReadBuildInfo())
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
