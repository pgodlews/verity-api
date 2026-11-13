package main

import (
	"bytes"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	old := [3]string{version, commit, date}
	t.Cleanup(func() { version, commit, date = old[0], old[1], old[2] })
	version, commit, date = "v1.2.3", "abc1234", "2026-11-13T00:00:00Z"
	var buf bytes.Buffer
	printVersion(&buf)
	if got, want := buf.String(), "verity-grpc v1.2.3 (commit abc1234, built 2026-11-13T00:00:00Z)\n"; got != want {
		t.Fatalf("printVersion = %q, want %q", got, want)
	}
}
