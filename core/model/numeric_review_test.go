package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestYAMLMergeKeysKeepNumericFidelity(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, field, value, want string
	}{
		{
			name: "REST explicit values win", protocol: "rest", field: "json",
			value: "{<<: {a: 1, overridden: 1.0}, b: 2, overridden: 2.00}",
			want:  `{"a":1,"b":2,"overridden":2.00}`,
		},
		{
			name: "GraphQL earlier merged map wins", protocol: "graphql", field: "variables",
			value: "{<<: [{a: 1, first: 1.0}, {a: 2, second: 2e0}], b: 2}",
			want:  `{"a":1,"b":2,"first":1.0,"second":2e0}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "schemaVersion: \"1.0\"\nname: test\nsuites:\n  - name: suite\n    cases:\n      - name: case\n        steps:\n          - name: step\n            protocol: " + tc.protocol + "\n            request:\n              " + tc.field + ": " + tc.value + "\n"
			p, err := Decode([]byte(source), "project.yaml")
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(p.Suites[0].Cases[0].Steps[0].Request[tc.field])
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("merged JSON = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestProjectYAMLSavePreservesJSONNumbers(t *testing.T) {
	source := `{"schemaVersion":"1.0","name":"test","suites":[{"name":"suite","cases":[{"name":"case","steps":[{"name":"step","protocol":"rest","request":{"json":{"integer":1,"decimal":1.0,"exponent":2e0,"huge":1e400}}}]}]}]}`
	p, err := Decode([]byte(source), "project.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Marshal("copy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"integer: 1", "decimal: 1.0", "exponent: 2e0", "huge: !!float 1e400"} {
		if !strings.Contains(text, want) {
			t.Fatalf("YAML save lost numeric token %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `decimal: "1.0"`) || strings.Contains(text, `exponent: "2e0"`) || strings.Contains(text, `huge: "1e400"`) {
		t.Fatalf("YAML save quoted numbers:\n%s", text)
	}
	path := filepath.Join(t.TempDir(), "copy.yaml")
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := Decode(saved, path)
	if err != nil {
		t.Fatal(err)
	}
	body := reloaded.Suites[0].Cases[0].Steps[0].Request["json"].(map[string]any)
	for key, want := range map[string]string{"integer": "1", "decimal": "1.0", "exponent": "2e0", "huge": "1e400"} {
		number, ok := body[key].(json.Number)
		if !ok || number.String() != want {
			t.Fatalf("%s = %#v, want json.Number(%s)", key, body[key], want)
		}
	}
}

func TestYAMLOutOfRangeNumberDoesNotConvertQuotedString(t *testing.T) {
	source := "schemaVersion: \"1.0\"\nname: test\nsuites:\n  - name: suite\n    cases:\n      - name: case\n        steps:\n          - name: step\n            protocol: rest\n            request:\n              json: {plain: 1e400, quoted: \"1e400\"}\n"
	p, err := Decode([]byte(source), "project.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := p.Suites[0].Cases[0].Steps[0].Request["json"].(map[string]any)
	if number, ok := body["plain"].(json.Number); !ok || number.String() != "1e400" {
		t.Fatalf("plain number = %#v", body["plain"])
	}
	if quoted, ok := body["quoted"].(string); !ok || quoted != "1e400" {
		t.Fatalf("quoted string = %#v", body["quoted"])
	}
}

func TestJSONProjectRejectsTrailingContent(t *testing.T) {
	valid := `{"schemaVersion":"1.0","name":"test","suites":[]}`
	for _, suffix := range []string{" garbage", ` {"second":true}`} {
		if _, err := Decode([]byte(valid+suffix), "project.json"); err == nil {
			t.Fatalf("trailing content %q was accepted", suffix)
		}
	}
	if _, err := Decode([]byte(valid+" \n\t"), "project.json"); err != nil {
		t.Fatalf("trailing whitespace was rejected: %v", err)
	}
}
