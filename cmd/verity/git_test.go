package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

func TestFmtCheckWriteAndIncludes(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project.yaml")
	fragment := filepath.Join(dir, "fragment.yaml")
	feed := filepath.Join(dir, "feed.json")
	root := `schemaVersion: "1.0"
name: demo
include: [fragment.yaml]
suites:
    - name: root
      cases:
          - name: case
            feed: {type: json, path: feed.json}
            steps:
                - name: get
                  protocol: rest
                  request: {url: "${BASE_URL}"}
`
	fragmentYAML := `suites:
    - name: included
      cases: []
`
	for path, content := range map[string]string{project: root, fragment: fragmentYAML} {
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(feed, []byte("not JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if code := cmdFmt([]string{"--check", project}); code != exitTestFailed {
		t.Fatalf("fmt --check exit %d, want %d", code, exitTestFailed)
	}
	afterCheck, _ := os.ReadFile(project)
	if string(afterCheck) != string(before) {
		t.Fatal("fmt --check modified the project")
	}
	if code := cmdFmt([]string{project}); code != exitOK {
		t.Fatalf("fmt exit %d", code)
	}
	formatted, _ := os.ReadFile(project)
	if strings.Contains(string(formatted), "\n    - name: root") {
		t.Fatalf("project was not normalized to two-space indentation:\n%s", formatted)
	}
	fragmentAfter, _ := os.ReadFile(fragment)
	if strings.Contains(string(fragmentAfter), "\n    - name: included") {
		t.Fatalf("included fragment was not formatted:\n%s", fragmentAfter)
	}
	if st, _ := os.Stat(project); st.Mode().Perm() != 0o640 {
		t.Fatalf("project mode changed to %v", st.Mode().Perm())
	}
	if code := cmdFmt([]string{"--check", project}); code != exitOK {
		t.Fatalf("formatted project failed --check with exit %d", code)
	}
	feedAfter, _ := os.ReadFile(feed)
	if string(feedAfter) != "not JSON" {
		t.Fatal("fmt touched a JSON feed")
	}
}

func TestFmtJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.json")
	content := `{"name":"demo","schemaVersion":"1.0","suites":[]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdFmt([]string{path}); code != exitOK {
		t.Fatalf("fmt exit %d", code)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"name\": \"demo\",\n  \"schemaVersion\": \"1.0\",\n  \"suites\": []\n}\n"
	if string(data) != want {
		t.Fatalf("JSON output:\n%s\nwant:\n%s", data, want)
	}
	if _, err := model.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestInitCommand(t *testing.T) {
	dir := t.TempDir()
	if code := cmdInit([]string{dir}); code != exitOK {
		t.Fatalf("init exit %d", code)
	}
	project, err := model.Load(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if errs := project.Validate(); len(errs) > 0 {
		t.Fatalf("starter project is invalid: %v", errs)
	}
	if code := cmdInit([]string{"one", "two"}); code != exitUsage {
		t.Fatalf("init with extra args exit %d, want %d", code, exitUsage)
	}
}
