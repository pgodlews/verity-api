package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"gopkg.in/yaml.v3"
)

func TestSavesMatchFormatterOutput(t *testing.T) {
	tests := []struct {
		name   string
		format string
		save   string
	}{
		{name: "YAML step", format: "yaml", save: "step"},
		{name: "YAML environment", format: "yaml", save: "environment"},
		{name: "JSON step", format: "json", save: "step"},
		{name: "JSON environment", format: "json", save: "environment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "project."+tc.format)
			project := &model.Project{
				SchemaVersion: "1.0",
				Name:          "demo",
				Environments:  []model.Environment{{Name: "local", Default: true}},
				Suites: []model.Suite{{Name: "suite", Cases: []model.TestCase{{Name: "case", Steps: []model.Step{{
					Name: "step", Protocol: "rest", Request: map[string]any{"url": "${BASE_URL}"},
				}}}}}},
			}
			data, err := project.Marshal(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.format == "yaml" {
				data = append([]byte("---\n"), data...)
				data = append(data, []byte("...\n")...)
				// YAML saves keep the bytes they do not edit, so a file
				// that is already formatted stays formatted.
				if data, err = model.FormatYAML(data); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.save == "step" {
				_, err = SaveStep(SaveStepRequest{Path: path, Suite: "suite", Case: "case", Index: 0, Step: model.Step{
					Name: "saved", Protocol: "rest", Request: map[string]any{"url": "${BASE_URL}/saved"},
				}})
			} else {
				_, err = SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "local", Variables: map[string]string{"TOKEN": "${env:SAVE_FORMAT_TOKEN}"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			format := model.FormatYAML
			if tc.format == "json" {
				format = model.FormatJSON
			} else if !strings.HasPrefix(string(saved), "---\n") || !strings.HasSuffix(string(saved), "...\n") {
				t.Fatalf("save lost document markers:\n%s", saved)
			}
			formatted, err := format(saved)
			if err != nil {
				t.Fatal(err)
			}
			if string(saved) != string(formatted) {
				t.Fatalf("save output differs from fmt:\nsaved:\n%s\nformatted:\n%s", saved, formatted)
			}
		})
	}
}

func TestSavesPreserveUnrelatedYAMLValues(t *testing.T) {
	for _, edit := range []string{"step", "environment"} {
		for _, style := range []string{">", ">-", ">+", "|", "|-", "|+"} {
			t.Run(edit+"/"+style, func(t *testing.T) {
				source := "---\n# project comment\nschemaVersion: \"1.0\"\nname: demo\ndescription: " + style + " # description comment\n  First paragraph\n  continues here.\n\n    indented text\n    another indented line\n\n  Last paragraph.\n\nenvironments:\n  - name: local\n    variables: {VALUE: before}\nsuites:\n  - name: suite\n    cases:\n      - name: case\n        steps:\n          # request comment\n          - name: step\n            protocol: rest\n            request:\n              url: '${BASE_URL}/before'\n              body: |\n                first line\n                second line\n...\n"
				path := filepath.Join(t.TempDir(), "project.yaml")
				if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
				var before map[string]any
				if err := yaml.Unmarshal([]byte(source), &before); err != nil {
					t.Fatal(err)
				}
				for attempt := 0; attempt < 2; attempt++ {
					var err error
					if edit == "step" {
						project, loadErr := model.Load(path)
						if loadErr != nil {
							t.Fatal(loadErr)
						}
						step := project.Suites[0].Cases[0].Steps[0]
						step.Request["url"] = "${BASE_URL}/after"
						_, err = SaveStep(SaveStepRequest{Path: path, Suite: "suite", Case: "case", Index: 0, Step: step})
						expected := before["suites"].([]any)[0].(map[string]any)["cases"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
						expected["request"].(map[string]any)["url"] = "${BASE_URL}/after"
					} else {
						_, err = SaveEnvironment(SaveEnvironmentRequest{Path: path, Name: "local", Variables: map[string]string{"VALUE": "after"}})
						before["environments"].([]any)[0].(map[string]any)["variables"] = map[string]any{"VALUE": "after"}
					}
					if err != nil {
						t.Fatal(err)
					}
					saved, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var after map[string]any
					if err := yaml.Unmarshal(saved, &after); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("save changed unrelated values: before %#v, after %#v", before, after)
					}
					for _, comment := range []string{"# project comment", "# description comment", "# request comment"} {
						if !strings.Contains(string(saved), comment) {
							t.Fatalf("lost %s", comment)
						}
					}
					if !strings.HasPrefix(string(saved), "---\n") || !strings.HasSuffix(string(saved), "...\n") {
						t.Fatal("lost document markers")
					}
				}
			})
		}
	}
}
