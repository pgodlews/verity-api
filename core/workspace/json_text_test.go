package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopJSONTextRoundTrip(t *testing.T) {
	const numbers = `"big":9007199254740993,"infinity":1e400,"negativeZero":-0,"decimal":0.1`
	for _, tc := range []struct {
		name, extension, source string
	}{
		{name: "YAML", extension: ".yaml", source: "schemaVersion: \"1.0\"\nname: test\nsuites:\n  - name: suite\n    cases:\n      - name: case\n        steps:\n          - name: body\n            protocol: rest\n            request: {url: \"${BASE_URL}\", json: {" + numbers + "}}\n          - name: variables\n            protocol: graphql\n            request: {url: \"${BASE_URL}\", query: \"{ viewer { id } }\", variables: {" + numbers + "}}\n"},
		{name: "JSON", extension: ".json", source: `{"schemaVersion":"1.0","name":"test","suites":[{"name":"suite","cases":[{"name":"case","steps":[{"name":"body","protocol":"rest","request":{"url":"${BASE_URL}","json":{` + numbers + `}}},{"name":"variables","protocol":"graphql","request":{"url":"${BASE_URL}","query":"{ viewer { id } }","variables":{` + numbers + `}}}]}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "project"+tc.extension)
			if err := os.WriteFile(path, []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			info, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			steps := info.Project.Suites[0].Cases[0].Steps
			for i, field := range []string{"jsonText", "variablesText"} {
				if _, ok := steps[i].Request[field].(string); !ok {
					t.Fatalf("step %d did not expose %s: %#v", i, field, steps[i].Request)
				}
				steps[i].Name = "edited"
				if _, err := SaveStep(SaveStepRequest{Path: path, Suite: "suite", Case: "case", Index: i, Step: steps[i]}); err != nil {
					t.Fatal(err)
				}
			}
			out, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			for _, literal := range []string{"9007199254740993", "1e400", "-0", "0.1"} {
				if !strings.Contains(text, literal) {
					t.Errorf("saved project lost %s:\n%s", literal, text)
				}
			}
			if strings.Contains(text, "jsonText") || strings.Contains(text, "variablesText") {
				t.Fatalf("desktop-only field saved:\n%s", text)
			}
		})
	}
}
