package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func gqlProject() Project {
	return Project{
		SchemaVersion: "1.0", Name: "p",
		Suites: []Suite{{Name: "s", Cases: []TestCase{{Name: "c", Steps: []Step{{
			Name: "g", Protocol: "graphql",
		}}}}}},
	}
}

func TestGraphQLSchemaShape(t *testing.T) {
	both := gqlProject()
	both.Schema = &GraphQLSchema{File: "a.graphql", Introspect: true}
	if !hasMsg(both.Validate(), "file and introspect") {
		t.Fatalf("both sources: %v", both.Validate())
	}
	empty := gqlProject()
	empty.Suites[0].Schema = &GraphQLSchema{}
	if !hasMsg(empty.Validate(), "requires file or introspect") {
		t.Fatal("empty schema should fail")
	}
	ok := gqlProject()
	ok.Schema = &GraphQLSchema{File: "a.graphql"}
	ok.Suites[0].Cases[0].Steps[0].Schema = &GraphQLSchema{Introspect: true}
	if errs := ok.Validate(); len(errs) != 0 {
		t.Fatalf("valid sources: %v", errs)
	}
}

func hasMsg(errs []*verr.Error, part string) bool {
	for _, e := range errs {
		if e != nil && strings.Contains(e.Error(), part) {
			return true
		}
	}
	return false
}

func TestGraphQLSchemaSourceFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte("type Query { ping: String }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "project.yaml")
	body := []byte("schemaVersion: \"1.0\"\nname: p\nschema:\n  file: schema.graphql\nsuites:\n  - name: s\n    cases:\n      - name: c\n        steps:\n          - name: g\n            protocol: graphql\n            request:\n              url: /graphql\n              query: \"{ ping }\"\n")
	if err := os.WriteFile(proj, body, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(proj)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "schema.graphql")
	for _, f := range p.SourceFiles {
		if f == want {
			return
		}
	}
	t.Fatalf("schema file not in SourceFiles: %v", p.SourceFiles)
}
