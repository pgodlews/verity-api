package main

import (
	"runtime/debug"
	"testing"
)

func TestBuildMetadataFallsBackToBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.7.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123"},
			{Key: "vcs.time", Value: "2026-11-13T10:00:00Z"},
		},
	}
	v, c, d := buildMetadata(info, true)
	if v != "v0.7.0" || c != "0123456789ab" || d != "2026-11-13T10:00:00Z" {
		t.Fatalf("go install metadata = %q %q %q", v, c, d)
	}
	if v, _, _ := buildMetadata(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true); v != "dev" {
		t.Fatalf("(devel) build version = %q, want dev", v)
	}
	old := [3]string{version, commit, date}
	t.Cleanup(func() { version, commit, date = old[0], old[1], old[2] })
	version, commit, date = "v1.0.0", "abc", "2026-01-01"
	if v, c, d := buildMetadata(info, true); v != "v1.0.0" || c != "abc" || d != "2026-01-01" {
		t.Fatalf("ldflags values must win: %q %q %q", v, c, d)
	}
}
