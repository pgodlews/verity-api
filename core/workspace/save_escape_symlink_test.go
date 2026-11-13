package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

const soapJSONProject = `{
  "schemaVersion": "1.0",
  "name": "p",
  "suites": [
    {
      "name": "s",
      "cases": [
        {
          "name": "c",
          "steps": [
            {
              "name": "call",
              "protocol": "soap",
              "request": {
                "url": "http://127.0.0.1/?a=1&b=2",
                "body": "<soap:Envelope>x</soap:Envelope>"
              }
            }
          ]
        }
      ]
    }
  ]
}
`

// JSON saves must not HTML-escape <, >, & in bodies and URLs.
func TestJSONSaveDoesNotEscapeHTML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.json")
	if err := os.WriteFile(path, []byte(soapJSONProject), 0o644); err != nil {
		t.Fatal(err)
	}
	step := model.Step{Name: "call", Protocol: "soap", Request: map[string]any{
		"url": "http://127.0.0.1/?a=1&b=3", "body": "<soap:Envelope>y</soap:Envelope>",
	}}
	if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "s", Case: "c", Index: 0, Step: step}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if strings.Contains(text, "\\u003c") || strings.Contains(text, "\\u0026") || !strings.Contains(text, "<soap:Envelope>y</soap:Envelope>") || !strings.Contains(text, "a=1&b=3") {
		t.Fatalf("saved JSON escaped HTML:\n%s", text)
	}
	formatted, err := model.FormatJSON([]byte(soapJSONProject))
	if err != nil || strings.Contains(string(formatted), "\\u003c") {
		t.Fatalf("FormatJSON escaped HTML: %v\n%s", err, formatted)
	}
}

// Saving through a symlinked project file updates the target and keeps the link.
func TestSaveStepWritesThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "project.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(soapJSONProject), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "project.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	step := model.Step{Name: "call", Protocol: "soap", Request: map[string]any{"url": "http://127.0.0.1/", "body": "<b>changed</b>"}}
	if _, err := SaveStep(SaveStepRequest{Path: link, Suite: "s", Case: "c", Index: 0, Step: step}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by a regular file (%v)", err)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "<b>changed</b>") {
		t.Fatalf("target not updated:\n%s", data)
	}
}
