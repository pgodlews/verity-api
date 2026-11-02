package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func requireGraphQLCode(t *testing.T, err error, code verr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", code)
	}
	got, ok := verr.As(err)
	if !ok || got.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}

func TestProjectNavigationAndSchemaHelpers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte(sampleSDL), 0o644); err != nil {
		t.Fatal(err)
	}
	p := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, "{ ping }", nil)
	p.Suites[0].Setup = &model.Hook{Steps: []model.Step{{Name: "g", Protocol: "graphql", Request: map[string]any{"query": "{ ping }"}}}}
	p.Suites[0].Teardown = &model.Hook{Steps: []model.Step{{Name: "g", Protocol: "graphql", Request: map[string]any{"query": "{ ping }"}}}}
	var paths []string
	Walk(p, func(loc StepLoc) { paths = append(paths, loc.Path) })
	wantPaths := []string{"suites[0].setup.steps[0]", "suites[0].cases[0].steps[0]", "suites[0].teardown.steps[0]"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("Walk paths = %v, want %v", paths, wantPaths)
	}
	Walk(nil, func(StepLoc) { t.Fatal("Walk visited a nil project") })
	if got := Find(p, "", "", "g"); len(got) != 3 {
		t.Fatalf("Find all = %d, want 3", len(got))
	}
	if got := Find(p, "s", "c", "g"); len(got) != 1 || got[0].Case == nil {
		t.Fatalf("Find case = %#v", got)
	}
	if got := Find(p, "other", "", "g"); len(got) != 0 {
		t.Fatalf("Find wrong suite = %#v", got)
	}
	loc, err := FindAt(p, "s", "c", 0)
	if err != nil || loc.Step == nil || loc.Path != "suites[0].cases[0].steps[0]" {
		t.Fatalf("FindAt = %#v, %v", loc, err)
	}
	for _, tt := range []struct {
		name  string
		p     *model.Project
		suite string
		kase  string
		index int
	}{
		{name: "nil project", p: nil, suite: "s", kase: "c"},
		{name: "missing suite", p: p, suite: "other", kase: "c"},
		{name: "missing case", p: p, suite: "s", kase: "other"},
		{name: "negative index", p: p, suite: "s", kase: "c", index: -1},
		{name: "large index", p: p, suite: "s", kase: "c", index: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FindAt(tt.p, tt.suite, tt.kase, tt.index)
			requireGraphQLCode(t, err, verr.CodeNotFound)
		})
	}

	projectSchema := &model.GraphQLSchema{File: "project.graphql"}
	suiteSchema := &model.GraphQLSchema{File: "suite.graphql"}
	stepSchema := &model.GraphQLSchema{File: "step.graphql"}
	if got := Resolve(projectSchema, suiteSchema, stepSchema); got != stepSchema {
		t.Fatal("step schema did not take precedence")
	}
	if got := Resolve(projectSchema, suiteSchema, nil); got != suiteSchema {
		t.Fatal("suite schema did not take precedence")
	}
	if got := Resolve(projectSchema, nil, nil); got != projectSchema {
		t.Fatal("project schema was not used")
	}

	caseLoc := FindAtMust(t, p, "s", "c", 0)
	sdl, err := StepSDL(context.Background(), p, caseLoc, Options{})
	if err != nil || sdl != sampleSDL {
		t.Fatalf("StepSDL = %q, %v", sdl, err)
	}
	templates, err := StepTemplates(context.Background(), p, caseLoc, 0, Options{})
	if err != nil || len(templates) != 2 {
		t.Fatalf("StepTemplates = %#v, %v", templates, err)
	}
	badStep := StepLoc{Path: "bad"}
	if _, err := StepSDL(context.Background(), p, badStep, Options{}); err == nil {
		t.Fatal("StepSDL accepted a missing step")
	}
	if _, err := StepTemplates(context.Background(), p, badStep, 1, Options{}); err == nil {
		t.Fatal("StepTemplates accepted a missing step")
	}
	rest := projectWith(dir, nil, "", nil)
	rest.Suites[0].Cases[0].Steps[0].Protocol = "rest"
	restLoc := FindAtMust(t, rest, "s", "c", 0)
	_, err = StepSDL(context.Background(), rest, restLoc, Options{})
	requireGraphQLCode(t, err, verr.CodeValidation)
	noSchema := projectWith(dir, nil, "{ ping }", nil)
	noSchemaLoc := FindAtMust(t, noSchema, "s", "c", 0)
	_, err = StepTemplates(context.Background(), noSchema, noSchemaLoc, 1, Options{})
	requireGraphQLCode(t, err, verr.CodeValidation)
	both := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql", Introspect: true}, "{ ping }", nil)
	bothLoc := FindAtMust(t, both, "s", "c", 0)
	_, err = StepSDL(context.Background(), both, bothLoc, Options{})
	requireGraphQLCode(t, err, verr.CodeValidation)
}

func FindAtMust(t *testing.T, p *model.Project, suite, kase string, index int) StepLoc {
	t.Helper()
	loc, err := FindAt(p, suite, kase, index)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestLoadExternalAndListTemplates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.graphql")
	sdl := `
type Query { hello(input: Filter): String }
type Mutation { update(count: Int!): Boolean }
type Subscription { changed: ID }
input Filter { term: String! }
`
	if err := os.WriteFile(path, []byte(sdl), 0o644); err != nil {
		t.Fatal(err)
	}
	schema, err := LoadExternal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	templates, err := ListTemplates(schema, 2)
	if err != nil || len(templates) != 3 {
		t.Fatalf("ListTemplates = %#v, %v", templates, err)
	}
	kinds := map[string]bool{}
	for _, tpl := range templates {
		kinds[tpl.Kind] = true
		if tpl.Query == "" || tpl.Variables == "" {
			t.Fatalf("empty template: %#v", tpl)
		}
	}
	if !kinds["query"] || !kinds["mutation"] || !kinds["subscription"] {
		t.Fatalf("template kinds = %v", kinds)
	}
	_, err = ListTemplates(nil, 1)
	requireGraphQLCode(t, err, verr.CodeValidation)
	_, err = LoadExternal(context.Background(), "custom://schema")
	requireGraphQLCode(t, err, verr.CodeValidation)
	_, err = LoadExternal(context.Background(), filepath.Join(dir, "missing.graphql"))
	requireGraphQLCode(t, err, verr.CodeValidation)
	for _, tt := range []struct {
		name string
		sdl  string
		want string
	}{
		{name: "empty", want: "schema is empty"},
		{name: "invalid", sdl: "type Query {", want: "invalid GraphQL schema"},
		{name: "no roots", sdl: "type Other { id: ID }", want: "no query, mutation, or subscription"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSDL(tt.sdl, "test.graphql")
			requireGraphQLCode(t, err, verr.CodeValidation)
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
	long := strings.Repeat("x", 301) + "\ntrailing"
	if got := firstLine(long); len(got) != 300 {
		t.Fatalf("firstLine length = %d, want 300", len(got))
	}
}

func TestIntrospectionRichTypeConversion(t *testing.T) {
	stringName, idName, roleName, filterName := "String", "ID", "Role", "Filter"
	userName, nodeName, dogName, catName := "User", "Node", "Dog", "Cat"
	description, multiline, oldReason, defaultTerm := "A user", "first\nsecond", "Use new", `"guest"`
	stringRef := introTypeRef{Kind: "SCALAR", Name: &stringName}
	env := introEnvelope{}
	env.Data.Schema = &introSchema{
		QueryType: &introName{Name: "Query"},
		Types: []introFullType{
			{Kind: "OBJECT", Name: "Query", Fields: []introField{
				{Name: "user", Type: introTypeRef{Kind: "OBJECT", Name: &userName}, Args: []introValue{{Name: "filter", Type: introTypeRef{Kind: "INPUT_OBJECT", Name: &filterName}}}},
				{Name: "old", Type: stringRef, IsDeprecated: true},
			}},
			{Kind: "OBJECT", Name: userName, Description: &description, Interfaces: []introTypeRef{{Kind: "INTERFACE", Name: &nodeName}}, Fields: []introField{{Name: "id", Type: introTypeRef{Kind: "SCALAR", Name: &idName}}}},
			{Kind: "INTERFACE", Name: nodeName, Fields: []introField{{Name: "id", Type: introTypeRef{Kind: "SCALAR", Name: &idName}}}},
			{Kind: "INPUT_OBJECT", Name: filterName, InputFields: []introValue{{Name: "term", Description: &multiline, Type: stringRef, DefaultValue: &defaultTerm}}},
			{Kind: "ENUM", Name: roleName, EnumValues: []introEnum{{Name: "ADMIN"}, {Name: "OLD", IsDeprecated: true, DeprecationReason: &oldReason}}},
			{Kind: "UNION", Name: "Pet", PossibleTypes: []introTypeRef{{Kind: "OBJECT", Name: &dogName}, {Kind: "OBJECT", Name: &catName}}},
			{Kind: "OBJECT", Name: dogName, Fields: []introField{{Name: "name", Type: stringRef}}},
			{Kind: "OBJECT", Name: catName, Fields: []introField{{Name: "name", Type: stringRef}}},
			{Kind: "SCALAR", Name: "JSON", Description: &description},
			{Kind: "OBJECT", Name: "__Ignored"},
			{Kind: "SCALAR", Name: "String"},
		},
		Directives: []introDirective{
			{Name: "tag", Description: &description, Locations: []string{"FIELD_DEFINITION", "OBJECT"}, Args: []introValue{{Name: "role", Type: introTypeRef{Kind: "ENUM", Name: &roleName}}}},
			{Name: "skip", Locations: []string{"FIELD"}},
			{Name: "invalid", Locations: nil},
		},
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	sdl, schema, err := SDLFromIntrospection(body, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type User implements Node", "input Filter", `term: String = "guest"`,
		"enum Role", `@deprecated(reason: "Use new")`, "union Pet = Dog | Cat",
		"scalar JSON", "directive @tag(role: Role) on FIELD_DEFINITION | OBJECT",
		`@deprecated(reason: "No longer supported")`, "first\n  second",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q:\n%s", want, sdl)
		}
	}
	if strings.Contains(sdl, "__Ignored") || strings.Contains(sdl, "directive @skip") || strings.Contains(sdl, "directive @invalid") {
		t.Fatalf("SDL contains filtered definitions:\n%s", sdl)
	}
	if schema.Query == nil || schema.Types["Filter"] == nil || schema.Types["Pet"] == nil {
		t.Fatalf("converted schema is incomplete: %#v", schema)
	}
}

func TestIntrospectionErrorsAndStringHelpers(t *testing.T) {
	secret := "fixture-secret"
	red := secrets.NewRedactor()
	red.Add(secret)
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "not JSON", body: "not-json", want: "not JSON"},
		{name: "GraphQL errors", body: `{"errors":[{"message":"failure ` + secret + `"}]}`, want: "introspection failed"},
		{name: "no schema", body: `{}`, want: "no schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := SDLFromIntrospection([]byte(tt.body), red)
			requireGraphQLCode(t, err, verr.CodeValidation)
			if !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), secret) {
				t.Fatalf("error = %q", err)
			}
		})
	}
	multiline := "a\r\nb\rc"
	if got := blockLiteral(multiline); got != "\"\"\"\na\nb\nc\n\"\"\"" {
		t.Fatalf("blockLiteral = %q", got)
	}
	quoted := `"a\nb"`
	if got := defaultLit(&quoted); got != `"a\nb"` {
		t.Fatalf("defaultLit quoted = %q", got)
	}
	raw := "a\nb"
	if got := defaultLit(&raw); got != blockLiteral(raw) {
		t.Fatalf("defaultLit raw = %q", got)
	}
	if got := reason(nil); got != "No longer supported" {
		t.Fatalf("reason(nil) = %q", got)
	}
	if got := gqlDesc("  first\nsecond  ", "  "); !strings.Contains(got, "\"\"\"\n  first\n  second") {
		t.Fatalf("gqlDesc = %q", got)
	}
	if got := gqlDesc(" \n", ""); got != "" {
		t.Fatalf("blank gqlDesc = %q", got)
	}
	if got := gqlString("a\n\t\x01"); got != `"a\n\t"` {
		t.Fatalf("gqlString = %q", got)
	}
}

func TestCacheKeyAndQueryHelpers(t *testing.T) {
	got, err := withQuery("/graphql?existing=one", map[string]string{"b": "two words", "a": "1"})
	if err != nil || !strings.Contains(got, "existing=one") || !strings.Contains(got, "a=1") || !strings.Contains(got, "b=two+words") {
		t.Fatalf("withQuery = %q, %v", got, err)
	}
	if _, err := withQuery("%", map[string]string{"a": "1"}); err == nil {
		t.Fatal("withQuery accepted an invalid URL")
	}
	sig := "AWS4-HMAC-SHA256 Credential=fixture, SignedHeaders=host, Signature=one"
	if got := authIdentity(sig); got != "AWS4-HMAC-SHA256 Credential=fixture" {
		t.Fatalf("authIdentity = %q", got)
	}
	if got := authIdentity("Bearer fixture"); got != "Bearer fixture" {
		t.Fatalf("bearer identity = %q", got)
	}
	req := &spi.Request{
		URL:     "/graphql",
		Headers: http.Header{"Authorization": {sig}, "Cookie": {"one", "two"}},
		Cookies: map[string]string{"b": "2", "a": "1"},
		Query:   map[string]string{"b": "2", "a": "1"},
		TLS:     &spi.TLSConfig{ClientCertPEM: []byte("certificate")},
	}
	first := introspectionKey(req.URL, req, []string{"auth"})
	second := introspectionKey(req.URL, req, []string{"auth"})
	if first != second || len(first) != 64 || strings.Contains(first, "certificate") {
		t.Fatalf("introspection key = %q / %q", first, second)
	}
	req.Cookies["a"] = "changed"
	if introspectionKey(req.URL, req, []string{"auth"}) == first {
		t.Fatal("cookie change did not change the cache key")
	}
}
