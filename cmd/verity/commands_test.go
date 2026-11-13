package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureCommand(t *testing.T, stdin string, fn func() int) (int, string, string) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = inR, outW, errW
	var stdout, stderr bytes.Buffer
	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stdout, outR)
		close(outDone)
	}()
	go func() {
		_, _ = io.Copy(&stderr, errR)
		close(errDone)
	}()
	_, _ = io.WriteString(inW, stdin)
	_ = inW.Close()
	code := fn()
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	<-outDone
	<-errDone
	_ = inR.Close()
	_ = outR.Close()
	_ = errR.Close()
	return code, stdout.String(), stderr.String()
}

func decodeCommandJSON(t *testing.T, output string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, output)
	}
	return got
}

func writeCommandFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunDispatchAndUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no command", want: exitUsage},
		{name: "short help", args: []string{"-h"}, want: exitOK},
		{name: "long help", args: []string{"--help"}, want: exitOK},
		{name: "help command", args: []string{"help"}, want: exitOK},
		{name: "unknown", args: []string{"unknown"}, want: exitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr := captureCommand(t, "", func() int { return run(tt.args) })
			if code != tt.want {
				t.Fatalf("exit = %d, want %d", code, tt.want)
			}
			if !strings.Contains(stderr, "Usage:") {
				t.Fatalf("usage missing from stderr: %q", stderr)
			}
		})
	}
}

func TestVersionListAndSchemaCommands(t *testing.T) {
	t.Run("version text", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int { return run([]string{"version"}) })
		if code != exitOK || !strings.Contains(stdout, "verity (Verity) dev") {
			t.Fatalf("exit = %d, output = %q", code, stdout)
		}
	})
	t.Run("version JSON", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int { return run([]string{"version", "--json"}) })
		got := decodeCommandJSON(t, stdout)
		if code != exitOK || got["name"] != "verity" || got["product"] != "Verity" || got["go"] == "" {
			t.Fatalf("exit = %d, JSON = %#v", code, got)
		}
	})
	t.Run("list text", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int { return run([]string{"list"}) })
		if code != exitOK || !strings.Contains(stdout, "protocols:") || !strings.Contains(stdout, "graphql") {
			t.Fatalf("exit = %d, output = %q", code, stdout)
		}
	})
	t.Run("list JSON", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int { return run([]string{"list", "--json"}) })
		got := decodeCommandJSON(t, stdout)
		if code != exitOK || got["protocols"] == nil || got["assertions"] == nil {
			t.Fatalf("exit = %d, JSON = %#v", code, got)
		}
	})
	for _, kind := range []string{"project", "auth"} {
		t.Run("schema "+kind, func(t *testing.T) {
			code, stdout, _ := captureCommand(t, "", func() int { return run([]string{"schema", "--kind", kind}) })
			got := decodeCommandJSON(t, stdout)
			if code != exitOK || got["$schema"] == "" {
				t.Fatalf("exit = %d, schema keys = %#v", code, got)
			}
		})
	}
	code, _, stderr := captureCommand(t, "", func() int { return run([]string{"schema", "--kind", "other"}) })
	if code != exitUsage || !strings.Contains(stderr, "unknown kind") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestValidateCommandReportsOutcome(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yaml")
	writeCommandFile(t, valid, "schemaVersion: \"1.0\"\nname: valid\nenvironments:\n  - name: local\n    default: true\nsuites:\n  - name: suite\n    cases:\n      - name: case\n        steps:\n          - name: request\n            protocol: rest\n            request:\n              method: GET\n              url: /\n")
	invalid := filepath.Join(dir, "invalid.yaml")
	writeCommandFile(t, invalid, "schemaVersion: \"1.0\"\nsuites: []\n")

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOK   bool
	}{
		{name: "valid JSON", args: []string{"validate", valid, "--json"}, wantCode: exitOK, wantOK: true},
		{name: "invalid JSON", args: []string{"validate", invalid, "--json"}, wantCode: exitConfig, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, _ := captureCommand(t, "", func() int { return run(tt.args) })
			got := decodeCommandJSON(t, stdout)
			if code != tt.wantCode || got["ok"] != tt.wantOK {
				t.Fatalf("exit = %d, JSON = %#v", code, got)
			}
		})
	}
	code, stdout, _ := captureCommand(t, "", func() int { return run([]string{"validate", valid}) })
	if code != exitOK || !strings.Contains(stdout, "OK:") {
		t.Fatalf("exit = %d, output = %q", code, stdout)
	}
	code, _, stderr := captureCommand(t, "", func() int { return run([]string{"validate", invalid}) })
	if code != exitConfig || !strings.Contains(stderr, "INVALID:") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	for _, args := range [][]string{{"validate"}, {"validate", filepath.Join(dir, "missing.yaml"), "--json"}, {"validate", "--bad"}} {
		code, _, _ = captureCommand(t, "", func() int { return run(args) })
		want := exitConfig
		if len(args) == 1 || args[len(args)-1] == "--bad" {
			want = exitUsage
		}
		if code != want {
			t.Errorf("run(%v) exit = %d, want %d", args, code, want)
		}
	}
}

func TestImportCommandKindsAndErrors(t *testing.T) {
	tests := []struct {
		kind  string
		spec  string
		extra []string
	}{
		{kind: "openapi", spec: filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")},
		{kind: "rest", spec: filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")},
		{kind: "wsdl", spec: filepath.Join("..", "..", "examples", "specs", "calculator.wsdl")},
		{kind: "soap", spec: filepath.Join("..", "..", "examples", "specs", "calculator.wsdl")},
		{kind: "postman", spec: filepath.Join("..", "..", "examples", "specs", "sample.postman_collection.json"), extra: []string{filepath.Join("..", "..", "examples", "specs", "sample.postman_environment.json")}},
		{kind: "soapui", spec: filepath.Join("..", "..", "examples", "specs", "sample.soapui.xml")},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "project.yaml")
			args := []string{"import", tt.kind, tt.spec}
			args = append(args, tt.extra...)
			args = append(args, "--out", out, "--json")
			code, stdout, _ := captureCommand(t, "", func() int { return run(args) })
			got := decodeCommandJSON(t, stdout)
			if code != exitOK || got["ok"] != true || got["type"] != tt.kind {
				t.Fatalf("exit = %d, JSON = %#v", code, got)
			}
			if _, err := os.Stat(out); err != nil {
				t.Fatalf("output project: %v", err)
			}
		})
	}

	t.Run("stdout content", func(t *testing.T) {
		spec := filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")
		code, stdout, _ := captureCommand(t, "", func() int {
			return run([]string{"import", "openapi", spec, "--json"})
		})
		got := decodeCommandJSON(t, stdout)
		if code != exitOK || got["content"] == "" || got["out"] != "" {
			t.Fatalf("exit = %d, JSON = %#v", code, got)
		}
	})
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{name: "missing args", args: []string{"import"}, want: exitUsage},
		{name: "unknown kind", args: []string{"import", "other", "spec"}, want: exitUsage},
		{name: "extra OpenAPI arg", args: []string{"import", "openapi", "spec", "extra"}, want: exitUsage},
		{name: "load failure", args: []string{"import", "openapi", "missing.yaml", "--json"}, want: exitConfig},
		{name: "bad flag", args: []string{"import", "--bad"}, want: exitUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, _, _ := captureCommand(t, "", func() int { return run(tt.args) })
			if code != tt.want {
				t.Fatalf("exit = %d, want %d", code, tt.want)
			}
		})
	}
}

func TestGraphQLCommands(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.graphql")
	writeCommandFile(t, schema, "type Query { hello: String }\n")
	project := filepath.Join(dir, "project.yaml")
	writeCommandFile(t, project, "schemaVersion: \"1.0\"\nname: graphql\nschema:\n  file: schema.graphql\nenvironments:\n  - name: local\n    default: true\nsuites:\n  - name: suite\n    cases:\n      - name: case\n        steps:\n          - name: hello\n            protocol: graphql\n            request:\n              url: /graphql\n              query: \"{ hello }\"\n")

	t.Run("template text", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int {
			return run([]string{"graphql", "template", schema, "--operation", "Query.hello"})
		})
		if code != exitOK || !strings.Contains(stdout, "query hello") || !strings.Contains(stdout, "hello") {
			t.Fatalf("exit = %d, output = %q", code, stdout)
		}
	})
	t.Run("template JSON", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int {
			return run([]string{"graphql", "template", schema, "--operation", "Query.hello", "--json"})
		})
		got := decodeCommandJSON(t, stdout)
		if code != exitOK || got["ok"] != true || got["operation"] != "Query.hello" || got["query"] == "" {
			t.Fatalf("exit = %d, JSON = %#v", code, got)
		}
	})
	t.Run("schema file and JSON", func(t *testing.T) {
		out := filepath.Join(dir, "out.graphql")
		code, stdout, _ := captureCommand(t, "", func() int {
			return run([]string{"graphql", "schema", project, "--step", "hello", "--out", out, "--json"})
		})
		got := decodeCommandJSON(t, stdout)
		if code != exitOK || got["ok"] != true || got["out"] != out || !strings.Contains(got["sdl"].(string), "type Query") {
			t.Fatalf("exit = %d, JSON = %#v", code, got)
		}
		if body, err := os.ReadFile(out); err != nil || !strings.Contains(string(body), "hello") {
			t.Fatalf("SDL file = %q, %v", body, err)
		}
	})
	t.Run("schema text", func(t *testing.T) {
		code, stdout, _ := captureCommand(t, "", func() int {
			return run([]string{"graphql", "schema", project, "--step", "hello"})
		})
		if code != exitOK || !strings.Contains(stdout, "type Query") {
			t.Fatalf("exit = %d, output = %q", code, stdout)
		}
	})
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{name: "missing subcommand", args: []string{"graphql"}, want: exitUsage},
		{name: "help", args: []string{"graphql", "help"}, want: exitOK},
		{name: "unknown subcommand", args: []string{"graphql", "other"}, want: exitUsage},
		{name: "schema missing step", args: []string{"graphql", "schema", project}, want: exitUsage},
		{name: "schema step not found", args: []string{"graphql", "schema", project, "--step", "other", "--json"}, want: exitConfig},
		{name: "template missing operation", args: []string{"graphql", "template", schema}, want: exitUsage},
		{name: "template schema failure", args: []string{"graphql", "template", filepath.Join(dir, "missing.graphql"), "--operation", "Query.hello", "--json"}, want: exitConfig},
		{name: "template operation failure", args: []string{"graphql", "template", schema, "--operation", "Query.missing", "--json"}, want: exitConfig},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, _, _ := captureCommand(t, "", func() int { return run(tt.args) })
			if code != tt.want {
				t.Fatalf("exit = %d, want %d", code, tt.want)
			}
		})
	}
}

func TestPluginScaffoldCommand(t *testing.T) {
	for _, typ := range []string{"protocol", "assertion", "reporter", "auth", "datasource"} {
		t.Run(typ, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "plugin")
			code, stdout, _ := captureCommand(t, "", func() int {
				return run([]string{"plugin", "new", typ, "sample", "--out", out})
			})
			if code != exitOK || !strings.Contains(stdout, "Scaffolded "+typ) {
				t.Fatalf("exit = %d, output = %q", code, stdout)
			}
			for _, name := range []string{"go.mod", "main.go", "impl_test.go", "plugin.json", "README.md"} {
				body, err := os.ReadFile(filepath.Join(out, name))
				needle := "sample"
				if name == "impl_test.go" {
					needle = "Sample"
				}
				if err != nil || !bytes.Contains(body, []byte(needle)) {
					t.Fatalf("%s = %q, %v", name, body, err)
				}
			}
		})
	}
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "missing subcommand", args: []string{"plugin"}},
		{name: "unknown subcommand", args: []string{"plugin", "other"}},
		{name: "missing name", args: []string{"plugin", "new", "protocol"}},
		{name: "unknown type", args: []string{"plugin", "new", "other", "sample"}},
		{name: "bad flag", args: []string{"plugin", "new", "--bad"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, _, _ := captureCommand(t, "", func() int { return run(tt.args) })
			if code != exitUsage {
				t.Fatalf("exit = %d, want usage", code)
			}
		})
	}
}
