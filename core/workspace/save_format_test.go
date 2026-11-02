package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
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
