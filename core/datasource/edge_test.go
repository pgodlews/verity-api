package datasource

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/registry"
)

func TestRegisterAndTypes(t *testing.T) {
	reg := registry.New()
	Register(reg)
	for _, typ := range []string{"csv", "json"} {
		source, err := reg.DataSource(typ)
		if err != nil || source.Type() != typ {
			t.Fatalf("DataSource(%q) = %#v, %v", typ, source, err)
		}
	}
}

func TestCSVSourceEdgeCases(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		content string
		config  map[string]any
		want    []map[string]string
	}{
		{name: "empty file", content: ""},
		{name: "header only", content: "id,name\n", want: []map[string]string{}},
		{name: "without header", content: "1,one\n2,two\n", config: map[string]any{"header": false}, want: []map[string]string{{"col0": "1", "col1": "one"}, {"col0": "2", "col1": "two"}}},
		{name: "trim leading space", content: "id,name\n 1, value\n", want: []map[string]string{{"id": "1", "name": "value"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".csv")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			rows, err := (csvSource{}).Rows("", tt.config, nil, path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rows, tt.want) {
				t.Fatalf("rows = %#v, want %#v", rows, tt.want)
			}
		})
	}
	rows, err := (csvSource{}).Rows(dir, nil, nil, "missing.csv")
	if err == nil || rows != nil || !strings.Contains(err.Error(), "open CSV feed") {
		t.Fatalf("missing CSV = %#v, %v", rows, err)
	}
	bad := filepath.Join(dir, "bad.csv")
	if err := os.WriteFile(bad, []byte("id,name\n\"unterminated,value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (csvSource{}).Rows("", nil, nil, bad); err == nil || !strings.Contains(err.Error(), "read CSV feed") {
		t.Fatalf("malformed CSV error = %v", err)
	}
}

func TestJSONSourceValuesAndErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")
	content := `[{"number":10000000,"decimal":19.99,"text":"value","bool":true,"null":null,"object":{"id":1},"array":[1,2]}]`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := (jsonSource{}).Rows(dir, nil, nil, "values.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"number": "10000000", "decimal": "19.99", "text": "value", "bool": "true", "null": "", "object": `{"id":1}`, "array": "[1,2]"}
	if len(rows) != 1 || !reflect.DeepEqual(rows[0], want) {
		t.Fatalf("row = %#v, want %#v", rows, want)
	}
	inline := []map[string]string{{"id": "inline"}}
	rows, err = (jsonSource{}).Rows("", nil, inline, "ignored.json")
	if err != nil || !reflect.DeepEqual(rows, inline) {
		t.Fatalf("inline rows = %#v, %v", rows, err)
	}
	if _, err := (jsonSource{}).Rows(dir, nil, nil, "missing.json"); err == nil || !strings.Contains(err.Error(), "read JSON feed") {
		t.Fatalf("missing JSON error = %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"id":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (jsonSource{}).Rows("", nil, nil, bad); err == nil || !strings.Contains(err.Error(), "expected array of objects") {
		t.Fatalf("malformed JSON error = %v", err)
	}
}

func TestResolve(t *testing.T) {
	if got := resolve("project", "feed.csv"); got != filepath.Join("project", "feed.csv") {
		t.Fatalf("relative resolve = %q", got)
	}
	absolute := filepath.Join(string(filepath.Separator), "project", "feed.csv")
	if got := resolve("ignored", absolute); got != absolute {
		t.Fatalf("absolute resolve = %q", got)
	}
	if got := resolve("", "feed.csv"); got != "feed.csv" {
		t.Fatalf("empty dir resolve = %q", got)
	}
}
