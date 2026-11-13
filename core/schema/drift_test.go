package schema

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

// The Go model is the source of truth. Every object in the embedded project
// schema must list exactly the JSON fields of its model type, and must
// require exactly the fields the model's validation requires. Change the
// model and this test names what the schema is missing.
func TestProjectSchemaMatchesTheModel(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(ProjectSchemaJSON()), &doc); err != nil {
		t.Fatal(err)
	}
	defs := doc["$defs"].(map[string]any)
	def := func(name string) map[string]any {
		if name == "" {
			return doc
		}
		d, ok := defs[name].(map[string]any)
		if !ok {
			t.Fatalf("schema has no $defs/%s", name)
		}
		return d
	}
	step := def("step")
	retry := step["properties"].(map[string]any)["retry"].(map[string]any)
	for _, tc := range []struct {
		typ      any
		schema   map[string]any
		required []string
	}{
		{model.Project{}, def(""), []string{"schemaVersion", "name"}},
		{model.GraphQLSchema{}, def("graphqlSchema"), nil},
		{model.Environment{}, def("environment"), []string{"name"}},
		{model.AuthDef{}, def("authDef"), []string{"name", "type"}},
		{model.AuthRef{}, def("authRef"), nil},
		{model.Suite{}, def("suite"), []string{"name", "cases"}},
		{model.Hook{}, def("hook"), nil},
		{model.TestCase{}, def("case"), []string{"name", "steps"}},
		{model.Feed{}, def("feed"), []string{"type"}},
		{model.Step{}, step, []string{"name", "protocol"}},
		{model.Retry{}, retry, []string{"count", "until"}},
		{model.Assertion{}, def("assertion"), []string{"type"}},
		{model.TransferSpec{}, def("transfer"), []string{"name", "from"}},
		{model.Mock{}, def("mock"), []string{"method", "path"}},
		{model.MockMatch{}, def("mockMatch"), nil},
	} {
		name := reflect.TypeOf(tc.typ).Name()
		props, _ := tc.schema["properties"].(map[string]any)
		var schemaKeys []string
		for k := range props {
			schemaKeys = append(schemaKeys, k)
		}
		sort.Strings(schemaKeys)
		if got, want := schemaKeys, jsonFields(reflect.TypeOf(tc.typ)); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: schema properties %v, model fields %v", name, got, want)
		}
		if tc.schema["additionalProperties"] != false {
			t.Errorf("%s: schema allows unknown keys, but decoding rejects them", name)
		}
		var req []string
		if r, ok := tc.schema["required"].([]any); ok {
			for _, v := range r {
				req = append(req, v.(string))
			}
		}
		sort.Strings(req)
		want := append([]string(nil), tc.required...)
		sort.Strings(want)
		if strings.Join(req, ",") != strings.Join(want, ",") {
			t.Errorf("%s: schema requires %v, model validation requires %v", name, req, want)
		}
	}
	if id, _ := doc["$id"].(string); !strings.HasPrefix(id, "https://github.com/pgodlews/verity-api/") {
		t.Errorf("project schema $id %q", id)
	}
	var auth map[string]any
	if err := json.Unmarshal([]byte(AuthSchemaJSON()), &auth); err != nil {
		t.Fatal(err)
	}
	if id, _ := auth["$id"].(string); !strings.HasPrefix(id, "https://github.com/pgodlews/verity-api/") {
		t.Errorf("auth schema $id %q", id)
	}
}

func jsonFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
