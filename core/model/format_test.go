package model_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/importers"
	"github.com/pgodlews/verity-api/core/model"
)

func TestFormatYAML(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "indent order and comments",
			input: "name: demo # project\nschemaVersion: \"1.0\"\nsuites:\n    # suite comment\n    - name: smoke\n      cases: []\n",
			want:  "name: demo # project\nschemaVersion: \"1.0\"\nsuites:\n  # suite comment\n  - name: smoke\n    cases: []\n",
		},
		{
			name:  "document markers",
			input: "---\nname: demo\n...\n",
			want:  "---\nname: demo\n...\n",
		},
		{
			name:  "flow style",
			input: "name: demo\nsuites: [{name: smoke, cases: []}]\n",
			want:  "name: demo\nsuites: [{name: smoke, cases: []}]\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.FormatYAML([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("format:\n%s\nwant:\n%s", got, tc.want)
			}
			twice, err := model.FormatYAML(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(twice) != string(got) {
				t.Fatalf("not idempotent:\nfirst:\n%s\nsecond:\n%s", got, twice)
			}
		})
	}
}

func TestFormatJSONPreservesOrderAndNumbers(t *testing.T) {
	input := []byte(`{"z":10000000,"a":{"second":2,"first":1},"values":[null,true,"text"]}`)
	want := "{\n" +
		"  \"z\": 10000000,\n" +
		"  \"a\": {\n" +
		"    \"second\": 2,\n" +
		"    \"first\": 1\n" +
		"  },\n" +
		"  \"values\": [\n" +
		"    null,\n" +
		"    true,\n" +
		"    \"text\"\n" +
		"  ]\n" +
		"}\n"
	got, err := model.FormatJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("format:\n%s\nwant:\n%s", got, want)
	}
	twice, err := model.FormatJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(twice) != string(got) {
		t.Fatalf("not idempotent:\n%s", twice)
	}
}

func TestFormatPreservesLoadedProject(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
	}{
		{
			name:     "YAML with include",
			filename: "project.yaml",
			content:  "schemaVersion: \"1.0\"\nname: demo\ninclude: [fragment.yaml]\nenvironments:\n    - name: local\n      default: true\n      variables:\n          TOKEN: ${env:FORMAT_TEST_TOKEN}\nsuites: []\n",
		},
		{
			name:     "JSON",
			filename: "project.json",
			content:  `{"name":"demo","schemaVersion":"1.0","suites":[{"cases":[{"steps":[{"request":{"url":"${BASE_URL}"},"protocol":"rest","name":"get"}],"name":"case"}],"name":"suite"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.filename == "project.yaml" {
				fragment := "suites:\n    - name: included\n      cases:\n          - name: case\n            steps:\n                - name: get\n                  protocol: rest\n                  request: {url: \"${BASE_URL}\"}\n"
				if err := os.WriteFile(filepath.Join(dir, "fragment.yaml"), []byte(fragment), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before, err := model.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			format := model.FormatYAML
			if strings.HasSuffix(tc.filename, ".json") {
				format = model.FormatJSON
			}
			formatted, err := format(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, formatted, 0o644); err != nil {
				t.Fatal(err)
			}
			after, err := model.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("loaded project changed:\nbefore: %#v\nafter: %#v", before, after)
			}
		})
	}
}

func TestFormatExamplesIdempotent(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".yaml") && !strings.EqualFold(filepath.Ext(path), ".yml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		once, err := model.FormatYAML(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		twice, err := model.FormatYAML(once)
		if err != nil {
			t.Errorf("%s: second format: %v", path, err)
			return nil
		}
		if string(once) != string(twice) {
			t.Errorf("%s: formatting is not idempotent\nfirst:\n%s\nsecond:\n%s", path, once, twice)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFormatImporterOutputsIdempotent(t *testing.T) {
	specs := []struct {
		kind string
		path string
	}{
		{kind: "openapi", path: "petstore.openapi.yaml"},
		{kind: "wsdl", path: "calculator.wsdl"},
		{kind: "postman", path: "sample.postman_collection.json"},
		{kind: "soapui", path: "sample.soapui.xml"},
	}
	for _, tc := range specs {
		t.Run(tc.kind, func(t *testing.T) {
			path := filepath.Join("..", "..", "examples", "specs", tc.path)
			var project *model.Project
			switch tc.kind {
			case "openapi":
				result, err := importers.ImportOpenAPI(path)
				if err != nil {
					t.Fatal(err)
				}
				project = result.Project
			case "wsdl":
				result, err := importers.ImportWSDL(path)
				if err != nil {
					t.Fatal(err)
				}
				project = result.Project
			case "postman":
				result, err := importers.ImportPostman(path)
				if err != nil {
					t.Fatal(err)
				}
				project = result.Project
			case "soapui":
				result, err := importers.ImportSoapUI(path)
				if err != nil {
					t.Fatal(err)
				}
				project = result.Project
			}
			data, err := project.Marshal("project.yaml")
			if err != nil {
				t.Fatal(err)
			}
			once, err := model.FormatYAML(data)
			if err != nil {
				t.Fatal(err)
			}
			twice, err := model.FormatYAML(once)
			if err != nil {
				t.Fatal(err)
			}
			if string(once) != string(twice) {
				t.Fatal("importer output formatting is not idempotent")
			}
		})
	}
}
