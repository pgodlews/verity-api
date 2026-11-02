package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUploadPaths(t *testing.T) {
	tests := []struct {
		name string
		req  map[string]any
		want []string
	}{
		{name: "nil", req: nil, want: nil},
		{name: "bodyFile", req: map[string]any{"bodyFile": "a.bin"}, want: []string{"a.bin"}},
		{name: "empty bodyFile", req: map[string]any{"bodyFile": ""}, want: nil},
		{
			name: "multipart file and text",
			req: map[string]any{"multipart": []any{
				map[string]any{"name": "n", "value": "v"},
				map[string]any{"name": "f", "file": "p.bin"},
			}},
			want: []string{"p.bin"},
		},
		{
			name: "empty file part",
			req:  map[string]any{"multipart": []any{map[string]any{"name": "f", "file": ""}}},
			want: nil,
		},
		{
			name: "map slice",
			req:  map[string]any{"multipart": []map[string]any{{"file": "q.bin"}}},
			want: []string{"q.bin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uploadPaths(tt.req)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("uploadPaths = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLoadSourceFiles(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	nested := filepath.Join(sub, "nested")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	absJSON := filepath.Join(dir, "abs.json")
	write(filepath.Join(dir, "rows.csv"), "id\n1\n")
	write(filepath.Join(dir, "rows.json"), "[]\n")
	write(absJSON, `[{"a":"b"}]`+"\n")
	write(filepath.Join(dir, "pic.bin"), "x")
	write(filepath.Join(dir, "body.bin"), "y")
	write(filepath.Join(dir, "hook.bin"), "z")
	write(filepath.Join(dataDir, "extra.csv"), "id\n1\n")
	// missing.csv is referenced and must be recorded without being created.

	write(filepath.Join(nested, "leaf.yaml"), `
suites:
  - name: leaf
    cases:
      - name: from-leaf
        feed:
          type: csv
          path: rows.csv
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
              bodyFile: body.bin
`)
	write(filepath.Join(sub, "mid.yaml"), `
include:
  - nested/leaf.yaml
suites:
  - name: mid
    setup:
      steps:
        - name: prep
          protocol: rest
          request:
            url: /up
            multipart:
              - name: file
                file: hook.bin
    teardown:
      steps:
        - name: done
          protocol: rest
          request:
            url: /up
            bodyFile: hook.bin
    cases:
      - name: inline-only
        feed:
          type: csv
          path: ignored.csv
          inline:
            - id: "1"
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
`)
	shared := filepath.Join(dir, "shared.yaml")
	write(shared, `
suites:
  - name: shared
    cases:
      - name: c
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
`)
	aPath := filepath.Join(dir, "a.yaml")
	write(aPath, `
include:
  - shared.yaml
suites:
  - name: a
    cases:
      - name: c
        feed:
          type: json
          path: rows.json
        steps:
          - name: up
            protocol: rest
            request:
              url: /up
              multipart:
                - name: note
                  value: hi
                - name: f
                  file: pic.bin
`)
	bPath := filepath.Join(dir, "b.yaml")
	write(bPath, `
include:
  - shared.yaml
`)
	main := filepath.Join(dir, "main.yaml")
	write(main, `
schemaVersion: "1.0"
name: src
include:
  - a.yaml
  - b.yaml
  - sub/mid.yaml
suites:
  - name: root
    cases:
      - name: abs
        feed:
          type: json
          path: `+quoteYAML(absJSON)+`
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
      - name: again
        feed:
          type: csv
          path: rows.csv
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
      - name: extra
        feed:
          type: csv
          path: data/extra.csv
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
      - name: missing
        feed:
          type: csv
          path: missing.csv
        steps:
          - name: s
            protocol: rest
            request:
              url: /x
`)

	p, err := Load(main)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.SourceDir != dir {
		t.Fatalf("SourceDir = %q, want %q", p.SourceDir, dir)
	}
	want := []string{
		main,
		aPath,
		bPath,
		shared,
		filepath.Join(sub, "mid.yaml"),
		filepath.Join(nested, "leaf.yaml"),
		filepath.Join(dir, "rows.csv"),
		filepath.Join(dir, "rows.json"),
		absJSON,
		filepath.Join(dir, "pic.bin"),
		filepath.Join(dir, "body.bin"),
		filepath.Join(dir, "hook.bin"),
		filepath.Join(dir, "missing.csv"),
		filepath.Join(dataDir, "extra.csv"),
	}
	for i, path := range want {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		want[i] = abs
	}
	sort.Strings(want)
	if !sort.StringsAreSorted(p.SourceFiles) {
		t.Fatalf("SourceFiles not sorted: %v", p.SourceFiles)
	}
	if !reflect.DeepEqual(p.SourceFiles, want) {
		t.Fatalf("SourceFiles:\n got %v\nwant %v", p.SourceFiles, want)
	}
	for _, path := range p.SourceFiles {
		if strings.HasSuffix(path, string(filepath.Separator)+"ignored.csv") {
			t.Fatalf("inline feed path was recorded: %s", path)
		}
	}

	jb, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var jdoc map[string]any
	if err := json.Unmarshal(jb, &jdoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := jdoc["sourceFiles"]; ok {
		t.Fatal("json leaked sourceFiles")
	}
	for _, path := range []string{main, aPath, bPath, shared} {
		if strings.Contains(string(jb), path) {
			t.Fatalf("json contains source path %s", path)
		}
	}
	yb, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var ydoc map[string]any
	if err := yaml.Unmarshal(yb, &ydoc); err != nil {
		t.Fatal(err)
	}
	if _, ok := ydoc["sourceFiles"]; ok {
		t.Fatal("yaml leaked sourceFiles")
	}
	if strings.Contains(string(yb), main) {
		t.Fatalf("yaml contains project path %s", main)
	}
}

func quoteYAML(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
