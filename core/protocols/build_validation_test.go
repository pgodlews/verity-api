package protocols

import (
	"context"
	"reflect"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func requireConfigErr(t *testing.T, name string, err error) {
	t.Helper()
	if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
		t.Errorf("%s: error = %v, want %s", name, err, verr.CodeConfig)
	}
}

func TestRESTAndGraphQLBuildValidation(t *testing.T) {
	const u = "http://127.0.0.1/x"
	rest := map[string]map[string]any{
		"json+form":     {"url": u, "json": map[string]any{"a": 1}, "form": map[string]any{"b": "2"}},
		"json+body":     {"url": u, "json": map[string]any{"a": 1}, "body": "raw"},
		"form not map":  {"url": u, "form": "a=1"},
		"query not map": {"url": u, "query": []any{"a"}},
		"headers list":  {"url": u, "headers": []any{"X-A: 1"}},
		"empty url":     {"url": "  "},
		"missing url":   {"method": "GET"},
	}
	for name, raw := range rest {
		_, err := RESTProtocol{}.BuildRequest(context.Background(), raw)
		requireConfigErr(t, "rest "+name, err)
	}
	gql := map[string]map[string]any{
		"empty query": {"url": u, "query": ""},
		"empty url":   {"query": "{ a }"},
	}
	for name, raw := range gql {
		_, err := GraphQLProtocol{}.BuildRequest(context.Background(), raw)
		requireConfigErr(t, "graphql "+name, err)
	}
	if _, err := (RESTProtocol{}).BuildRequest(context.Background(), map[string]any{"url": u, "form": map[string]any{"a": 1}}); err != nil {
		t.Fatalf("valid form: %v", err)
	}
}

func TestMultipartValidationDoesNotMutateInput(t *testing.T) {
	part := map[string]any{"name": "n", "value": 42}
	raw := map[string]any{"url": "http://127.0.0.1/x", "multipart": []any{part}}
	before := map[string]any{"name": "n", "value": 42}
	if _, err := (RESTProtocol{}).BuildRequest(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(part, before) {
		t.Fatalf("part mutated: %#v", part)
	}
	dir := t.TempDir()
	req, _ := RESTProtocol{}.BuildRequest(context.Background(), raw)
	if err := prepareUpload(dir, false, req); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(part, before) {
		t.Fatalf("part mutated by upload: %#v", part)
	}
}
