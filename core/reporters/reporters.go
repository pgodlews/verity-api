// Package reporters provides Verity's built-in run reporters: console,
// JUnit XML, structured JSON, and HTML. Each implements spi.Reporter and is
// registered through the registry like any third-party reporter.
//
// All reporters consume the single spi.RunResult and route text through the
// run's Redactor so secrets cannot leak into any output artifact.
package reporters

import (
	"io"
	"os"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
)

// Register installs all built-in reporters.
func Register(r *registry.Registry) {
	r.RegisterReporter(&ConsoleReporter{})
	r.RegisterReporter(&JUnitReporter{})
	r.RegisterReporter(&JSONReporter{})
	r.RegisterReporter(&HTMLReporter{})
}

// openOut returns a writer for the given destination. "-" or "" means stdout.
// The caller must call the returned close func. Parent directories are created
// as needed so a path like "reports/junit.xml" works without a pre-existing
// "reports/" directory.
func openOut(out string) (io.Writer, func() error, error) {
	if out == "" || out == "-" {
		return os.Stdout, func() error { return nil }, nil
	}
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}
	f, err := os.Create(out)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// redact is a nil-safe helper that masks text through the run's redactor.
func redact(red *secrets.Redactor, s string) string {
	if red == nil {
		return s
	}
	return red.Redact(s)
}
