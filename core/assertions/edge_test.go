package assertions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/internal/jsonnum"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestCompareOperatorsAndConversions(t *testing.T) {
	tests := []struct {
		name     string
		op       string
		actual   any
		expected any
		want     bool
		message  string
	}{
		{name: "default equality", actual: "same", expected: "same", want: true},
		{name: "exists", op: "exists", actual: 0, want: true},
		{name: "exists nil", op: "exists", actual: nil},
		{name: "not exists", op: "not_exists", actual: nil, want: true},
		{name: "numeric equality", op: "eq", actual: " 2 ", expected: float32(2), want: true},
		{name: "string inequality", op: "ne", actual: "one", expected: "two", want: true},
		{name: "numeric inequality", op: "ne", actual: int64(2), expected: 3, want: true},
		{name: "less", op: "lt", actual: 1, expected: 2, want: true},
		{name: "less equal", op: "lte", actual: 2, expected: 2, want: true},
		{name: "greater", op: "gt", actual: 3, expected: 2, want: true},
		{name: "greater equal", op: "gte", actual: 2, expected: 2, want: true},
		{name: "numeric required", op: "lt", actual: "one", expected: 2, message: "requires numeric"},
		{name: "contains bytes", op: "contains", actual: []byte("hello"), expected: "ell", want: true},
		{name: "matches", op: "matches", actual: true, expected: `^tr.e$`, want: true},
		{name: "invalid regex", op: "matches", actual: "value", expected: "[", message: "invalid regex"},
		{name: "unknown", op: "other", actual: "value", expected: "value", message: "unknown operator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passed, message := compare(tt.op, tt.actual, tt.expected)
			if passed != tt.want || (tt.message != "" && !strings.Contains(message, tt.message)) {
				t.Fatalf("compare = %v, %q; want %v and message containing %q", passed, message, tt.want, tt.message)
			}
		})
	}
	if got := toString(nil); got != "" {
		t.Fatalf("toString(nil) = %q", got)
	}
	if got := toString(map[string]int{"id": 1}); got != "map[id:1]" {
		t.Fatalf("toString(map) = %q", got)
	}
	if _, ok := jsonnum.Rat(true); ok {
		t.Fatal("boolean converted to a number")
	}
}

func TestBuiltinAssertionEdgeCases(t *testing.T) {
	t.Run("status expression and response time", func(t *testing.T) {
		if got := eval(t, statusAssertion{}, jsonResp("{}"), spi.AssertionSpec{Expr: "200"}); !got.Passed {
			t.Fatal(got.Message)
		}
		resp := jsonResp("{}")
		resp.Duration = 5 * time.Millisecond
		if got := eval(t, responseTimeAssertion{}, resp, spi.AssertionSpec{Value: 5}); !got.Passed || !strings.Contains(got.Message, "took 5ms") {
			t.Fatalf("response time = %#v", got)
		}
	})
	t.Run("XPath errors and default operation", func(t *testing.T) {
		a := xPathAssertion{}
		if got := eval(t, a, &spi.Response{Body: []byte("<root><status>OK</status></root>")}, spi.AssertionSpec{Expr: "//status"}); !got.Passed {
			t.Fatal(got.Message)
		}
		if got := eval(t, a, &spi.Response{Body: []byte("<root>")}, spi.AssertionSpec{Expr: "//status"}); got.Passed || !strings.Contains(got.Message, "parse XML") {
			t.Fatalf("malformed XML = %#v", got)
		}
		if got := eval(t, a, &spi.Response{Body: []byte("<root/>")}, spi.AssertionSpec{Expr: "//*[", Value: "x"}); got.Passed || !strings.Contains(got.Message, "invalid XPath") {
			t.Fatalf("invalid XPath = %#v", got)
		}
		if got := eval(t, a, &spi.Response{Body: []byte("<root/>")}, spi.AssertionSpec{Expr: "//missing", Value: "x"}); got.Passed || !strings.Contains(got.Message, "matched nothing") {
			t.Fatalf("missing XPath = %#v", got)
		}
	})
	t.Run("JSONPath errors and missing comparison", func(t *testing.T) {
		a := jsonPathAssertion{}
		if got := eval(t, a, jsonResp("not-json"), spi.AssertionSpec{Expr: "$.id"}); got.Passed || !strings.Contains(got.Message, "parse JSON") {
			t.Fatalf("malformed JSON = %#v", got)
		}
		if got := eval(t, a, jsonResp(`{"id":1}`), spi.AssertionSpec{Expr: "$.missing", Value: 1}); got.Passed || !strings.Contains(got.Message, "matched nothing") {
			t.Fatalf("missing JSONPath = %#v", got)
		}
		if got := eval(t, a, jsonResp(`{"id":1}`), spi.AssertionSpec{Expr: "$.[", Value: 1}); got.Passed {
			t.Fatalf("invalid JSONPath = %#v", got)
		}
	})
	t.Run("body and header defaults", func(t *testing.T) {
		if got := eval(t, containsAssertion{}, jsonResp("hello"), spi.AssertionSpec{Expr: "ell", Op: "not_exists"}); got.Passed {
			t.Fatal("present substring passed not_exists")
		}
		if got := eval(t, regexAssertion{}, jsonResp("value"), spi.AssertionSpec{Value: "["}); got.Passed || !strings.Contains(got.Message, "invalid regex") {
			t.Fatalf("invalid value regex = %#v", got)
		}
		resp := &spi.Response{Headers: http.Header{"X-Test": {"value"}}}
		if got := eval(t, headerAssertion{}, resp, spi.AssertionSpec{Expr: "X-Test"}); !got.Passed {
			t.Fatal(got.Message)
		}
		if got := eval(t, headerAssertion{}, resp, spi.AssertionSpec{Expr: "X-Test", Value: "value"}); !got.Passed {
			t.Fatal(got.Message)
		}
	})
}

func TestJSONSchemaAssertionInputsAndFailures(t *testing.T) {
	a := jsonSchemaAssertion{}
	validSchema := `{"type":"object","required":["id"],"properties":{"id":{"type":"number"}}}`
	t.Run("inline string", func(t *testing.T) {
		if got := eval(t, a, jsonResp(`{"id":1}`), spi.AssertionSpec{Config: map[string]any{"schema": validSchema}}); !got.Passed {
			t.Fatal(got.Message)
		}
	})
	t.Run("relative schema file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(validSchema), 0o644); err != nil {
			t.Fatal(err)
		}
		spec := spi.AssertionSpec{Config: map[string]any{"schemaFile": "schema.json", "__dir": dir}}
		if got := eval(t, a, jsonResp(`{"id":1}`), spec); !got.Passed {
			t.Fatal(got.Message)
		}
	})
	tests := []struct {
		name    string
		body    string
		spec    spi.AssertionSpec
		message string
	}{
		{name: "missing schema", body: `{}`, message: "requires 'schema'"},
		{name: "unserializable inline schema", body: `{}`, spec: spi.AssertionSpec{Config: map[string]any{"schema": func() {}}}, message: "invalid inline schema"},
		{name: "invalid inline JSON", body: `{}`, spec: spi.AssertionSpec{Config: map[string]any{"schema": "not-json"}}, message: "add schema"},
		{name: "invalid schema definition", body: `{}`, spec: spi.AssertionSpec{Config: map[string]any{"schema": `{"type":12}`}}, message: "compile schema"},
		{name: "missing schema file", body: `{}`, spec: spi.AssertionSpec{Expr: "missing.json", Config: map[string]any{"__dir": t.TempDir()}}, message: "read schema file"},
		{name: "invalid response JSON", body: `not-json`, spec: spi.AssertionSpec{Config: map[string]any{"schema": validSchema}}, message: "response is not valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eval(t, a, jsonResp(tt.body), tt.spec); got.Passed || !strings.Contains(got.Message, tt.message) {
				t.Fatalf("result = %#v, want message containing %q", got, tt.message)
			}
		})
	}
}

func TestXSDRootMismatch(t *testing.T) {
	a := xsdAssertion{}
	got := eval(t, a, &spi.Response{Body: []byte(`<?xml version="1.0"?><Actual/>`)}, spi.AssertionSpec{Config: map[string]any{"rootElement": "Expected"}})
	if got.Passed || !strings.Contains(got.Message, `expected root element "Expected", got "Actual"`) {
		t.Fatalf("root mismatch = %#v", got)
	}
}

func TestGraphQLInputs(t *testing.T) {
	_, _, _, fail := graphqlInputs(spi.AssertionSpec{})
	if fail == "" {
		t.Fatal("missing GraphQL config was accepted")
	}
	_, _, _, fail = graphqlInputs(spi.AssertionSpec{Config: map[string]any{"__gql": &graphql.AssertionInput{Err: errors.New("load failed")}}})
	if fail != "load failed" {
		t.Fatalf("input error = %q", fail)
	}
	_, _, _, fail = graphqlInputs(spi.AssertionSpec{Config: map[string]any{"__gql": &graphql.AssertionInput{}}})
	if fail != "GraphQL schema is required" {
		t.Fatalf("missing schema error = %q", fail)
	}
	_, _, _, fail = graphqlInputs(spi.AssertionSpec{Config: map[string]any{"schemaSDL": "type Query {"}})
	if !strings.Contains(fail, "invalid GraphQL schema") {
		t.Fatalf("invalid SDL error = %q", fail)
	}
	schema, query, operation, fail := graphqlInputs(spi.AssertionSpec{Config: map[string]any{
		"schemaSDL": "type Query { hello: String }", "query": "query Named { hello }", "operationName": "Named",
	}})
	if fail != "" || schema == nil || query != "query Named { hello }" || operation != "Named" {
		t.Fatalf("SDL inputs = %#v, %q, %q, %q", schema, query, operation, fail)
	}
}

func TestAssertionRedactsValidationFailure(t *testing.T) {
	secret := "fixture-secret"
	red := secrets.NewRedactor()
	red.Add(secret)
	a := jsonSchemaAssertion{}
	result, err := a.Evaluate(context.Background(), &spi.AssertionContext{
		Response: &spi.Response{Body: []byte(`{"id":"other"}`)}, Redactor: red,
	}, spi.AssertionSpec{Config: map[string]any{"schema": map[string]any{"properties": map[string]any{"id": map[string]any{"enum": []any{secret}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed || strings.Contains(result.Message, secret) || !strings.Contains(result.Message, "***REDACTED***") {
		t.Fatalf("validation failure was not redacted: %#v", result)
	}
}
