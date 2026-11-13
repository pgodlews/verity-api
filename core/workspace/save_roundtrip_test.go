package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

// The macOS regression run saw the first save of a request rewrite other
// parts of the file: quotes dropped, blank lines between suites removed, a
// folded description turned literal. A save now changes only the step.
const roundTripYAML = `---
# project comment
schemaVersion: "1.0"
name: 'round trip'
description: >
  Folded text that
  spans two lines.

environments:
  - name: local
    default: true
    variables:
      # the base URL
      BASE_URL: "http://localhost:8080"
      ZETA: z   # kept after an edit
      ALPHA: a

suites:
  - name: first
    cases:
      - name: case
        steps:
          # request comment
          - protocol: rest   # protocol before name, on purpose
            name: get
            request:
              method: GET
              url: "${BASE_URL}/before"   # url comment
              body: "<order><id>1</id></order>"
            assertions:
              - { type: status, value: 200 }

  - name: second
    cases:
      - name: other
        steps:
          - name: untouched
            protocol: rest
            request: { url: '${BASE_URL}/other' }
...
`

func writeRoundTrip(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStepSaveChangesOnlyTheEditedValue(t *testing.T) {
	path := writeRoundTrip(t, "project.yaml", roundTripYAML)
	p, err := model.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	step := p.Suites[0].Cases[0].Steps[0]
	step.Request["url"] = "${BASE_URL}/after"
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "first", Case: "case", Index: 0, Step: step}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	// Only the edited step is re-encoded: its keys, comments, and styles
	// stay, with the encoder's spacing for comments and flow mappings.
	want := strings.NewReplacer(
		`"${BASE_URL}/before"   # url comment`, `"${BASE_URL}/after" # url comment`,
		"rest   # protocol before name", "rest # protocol before name",
		"{ type: status, value: 200 }", "{type: status, value: 200}",
	).Replace(roundTripYAML)
	if string(got) != want {
		t.Fatalf("save rewrote more than the url:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestEnvironmentSaveKeepsOrderAndComments(t *testing.T) {
	path := writeRoundTrip(t, "project.yaml", roundTripYAML)
	_, err := SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "local", Variables: map[string]string{
		"BASE_URL": "http://localhost:8080", "ZETA": "changed", "NEW": "n", // ALPHA removed
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.Replace(roundTripYAML, "      ZETA: z   # kept after an edit\n      ALPHA: a\n",
		"      ZETA: changed # kept after an edit\n      NEW: n\n", 1)
	if string(got) != want {
		t.Fatalf("environment save:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

const roundTripJSON = `{
  "schemaVersion": "1.0",
  "name": "json",
  "suites": [
    {
      "name": "s",
      "cases": [
        {
          "name": "c",
          "steps": [
            {
              "protocol": "soap",
              "name": "call",
              "request": {
                "url": "http://localhost/ws?a=1&b=2",
                "body": "<Envelope><Id>9007199254740993</Id></Envelope>",
                "limit": 9007199254740993
              }
            }
          ]
        }
      ]
    }
  ]
}
`

func TestJSONStepSaveKeepsOrderNumbersAndMarkup(t *testing.T) {
	path := writeRoundTrip(t, "project.json", roundTripJSON)
	p, err := model.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	step := p.Suites[0].Cases[0].Steps[0]
	step.Request["url"] = "http://localhost/ws?a=2&b=3"
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "s", Case: "c", Index: 0, Step: step}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.Replace(roundTripJSON, "a=1&b=2", "a=2&b=3", 1)
	if string(got) != want {
		t.Fatalf("JSON save:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestJSONStepMergeKeepsUnknownKeys(t *testing.T) {
	src := strings.Replace(roundTripJSON, `"name": "call",`, `"name": "call",
              "x-note": "kept",`, 1)
	out, found, err := model.ReplaceJSONStep([]byte(src), "s", "c", 0, model.Step{
		Name: "call", Protocol: "soap", Request: map[string]any{"url": "http://localhost/ws"},
	})
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if !strings.Contains(string(out), `"x-note": "kept"`) || strings.Contains(string(out), `"limit"`) {
		t.Fatalf("merge result:\n%s", out)
	}
}

func TestSaveWritesThroughASymlinkedProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	real := writeRoundTrip(t, "real.yaml", roundTripYAML)
	link := filepath.Join(t.TempDir(), "project.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEnvironment(SaveEnvironmentRequest{Path: link, Name: "local", Variables: map[string]string{"BASE_URL": "http://localhost:9"}}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
	got, _ := os.ReadFile(real)
	if !strings.Contains(string(got), `BASE_URL: "http://localhost:9"`) {
		t.Fatalf("target not updated:\n%s", got)
	}
}
