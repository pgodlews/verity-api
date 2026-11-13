//go:build !devbridge

package main

import (
	"strings"
	"testing"
)

// Release builds (no devbridge tag) do not ship the dev bridge command.
func TestGUICallIsNotInReleaseBuilds(t *testing.T) {
	code, _, stderr := captureCommand(t, "[]", func() int { return run([]string{"gui-call", "Catalog"}) })
	if code != exitUsage || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if len(devCommands) != 0 {
		t.Fatalf("dev commands compiled in: %v", devCommands)
	}
}
