package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateWarnsAboutStepsWithoutAssertions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.yaml")
	// projectYAML has a status assertion; drop it to get a step with none.
	body := projectYAML("http://127.0.0.1:1/")
	body = body[:strings.Index(body, "            assertions:")]
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	out := withStdout(t, func() { code = run([]string{"validate", path, "--json"}) })
	if code != exitOK {
		t.Fatalf("exit %d (a warning must not fail validation): %s", code, out)
	}
	var got struct {
		Warnings []struct{ Code, Path string } `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Code != "no_assertions" || got.Warnings[0].Path != "suites[0].cases[0].steps[0]" {
		t.Fatalf("warnings = %+v", got.Warnings)
	}
}
