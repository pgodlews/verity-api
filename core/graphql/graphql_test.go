package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

const sampleSDL = `
scalar JSON

type Query {
  ping: String
  user(id: ID!, verbose: Boolean = false, q: String): User
}

type User {
  id: ID!
  name: String
  score: Int
  tags: [String!]
  friend: User
  posts(limit: Int!): [Post!]
  meta: JSON
}

type Post {
  title: String
}
`

func TestValidateSDLFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.graphql"), []byte(sampleSDL), 0o644); err != nil {
		t.Fatal(err)
	}
	query := "query {\n  user(id: \"1\") {\n    nope\n  }\n}\n"
	p := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, query, nil)
	errs := ValidateProject(context.Background(), p, Options{})
	if len(errs) != 1 {
		t.Fatalf("errs: %v", errs)
	}
	got := errs[0].Error()
	if !strings.Contains(got, "line 3, column 5") || !strings.Contains(got, "nope") {
		t.Fatalf("position: %s", got)
	}
	if errs[0].Path != "suites[0].cases[0].steps[0].request.query" {
		t.Fatalf("path: %s", errs[0].Path)
	}
	if errs[0].Details["line"] != 3 || errs[0].Details["column"] != 5 {
		t.Fatalf("details: %#v", errs[0].Details)
	}
	if errs[0].Code != verr.CodeValidation {
		t.Fatalf("code: %s", errs[0].Code)
	}

	badFrag := "query {\n  user(id: \"1\") {\n    ...Missing\n  }\n}\n"
	errs = ValidateProject(context.Background(), projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, badFrag, nil), Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "Missing") {
		t.Fatalf("fragment: %v", errs)
	}

	missingArg := "query {\n  user {\n    id\n  }\n}\n"
	errs = ValidateProject(context.Background(), projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, missingArg, nil), Options{})
	if len(errs) == 0 || !strings.Contains(strings.ToLower(errs[0].Error()), "id") {
		t.Fatalf("required arg: %v", errs)
	}

	varsQuery := "query ($id: ID!, $n: Int!) {\n  user(id: $id) {\n    posts(limit: $n) { title }\n  }\n}\n"
	badVars := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, varsQuery, map[string]any{"id": "1", "n": "nope"})
	errs = ValidateProject(context.Background(), badVars, Options{})
	if len(errs) == 0 || errs[0].Path != "suites[0].cases[0].steps[0].request.variables" {
		t.Fatalf("variable type: %v", errs)
	}

	skipped := projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, varsQuery, map[string]any{"id": "${env:ID}", "n": 1})
	if errs = ValidateProject(context.Background(), skipped, Options{}); len(errs) != 0 {
		t.Fatalf("interpolated variable should be skipped: %v", errs)
	}

	okQuery := "query {\n  user(id: \"1\") {\n    id\n    name\n  }\n}\n"
	if errs = ValidateProject(context.Background(), projectWith(dir, &model.GraphQLSchema{File: "schema.graphql"}, okQuery, nil), Options{}); len(errs) != 0 {
		t.Fatalf("valid query: %v", errs)
	}
}

func TestSchemaPathConfinement(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "secret.graphql")
	if err := os.WriteFile(outside, []byte(sampleSDL), 0o644); err != nil {
		t.Fatal(err)
	}
	p := projectWith(dir, &model.GraphQLSchema{File: "../secret.graphql"}, "{ ping }", nil)
	errs := ValidateProject(context.Background(), p, Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "outside the project directory") {
		t.Fatalf("relative escape: %v", errs)
	}
	abs := projectWith(dir, &model.GraphQLSchema{File: outside}, "{ ping }", nil)
	errs = ValidateProject(context.Background(), abs, Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "outside the project directory") {
		t.Fatalf("absolute escape: %v", errs)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.graphql")); err != nil {
		t.Fatal(err)
	}
	link := projectWith(dir, &model.GraphQLSchema{File: "link.graphql"}, "{ ping }", nil)
	errs = ValidateProject(context.Background(), link, Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "outside the project directory") {
		t.Fatalf("symlink escape: %v", errs)
	}
}

func TestIntrospectionAndCache(t *testing.T) {
	var hits atomic.Int32
	token := "fixture-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Trace") != "keep" {
			t.Errorf("step header was not forwarded")
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(introBody())
	}))
	defer srv.Close()

	query := "query {\n  user(id: \"1\") {\n    nope\n  }\n}\n"
	p := projectWith("", &model.GraphQLSchema{Introspect: true}, query, nil)
	p.Suites[0].Cases[0].Steps[0].Request["url"] = srv.URL
	p.Suites[0].Cases[0].Steps[0].Request["headers"] = map[string]any{"X-Trace": "keep", "Authorization": "Bearer " + token}
	cache := NewCache()
	loc := StepLoc{Suite: &p.Suites[0], Case: &p.Suites[0].Cases[0], Step: &p.Suites[0].Cases[0].Steps[0], Path: "suites[0].cases[0].steps[0]", SuitePath: "suites[0]"}
	if _, err := loadStepSchema(context.Background(), cache, p, loc, p.Suites[0].Cases[0].Steps[0].Schema, Options{Timeout: 5 * time.Second}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStepSchema(context.Background(), cache, p, loc, p.Suites[0].Cases[0].Steps[0].Schema, Options{Timeout: 5 * time.Second}, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("introspection calls: %d", hits.Load())
	}

	errs := ValidateProject(context.Background(), p, Options{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "line 3, column 5") {
		t.Fatalf("introspected validation: %v", errs)
	}
	for _, e := range errs {
		if strings.Contains(e.Error(), token) || strings.Contains(e.Error(), "Authorization") {
			t.Fatalf("secret leaked: %s", e.Error())
		}
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Authorization: Bearer " + token))
	}))
	defer bad.Close()
	p.Suites[0].Cases[0].Steps[0].Request["url"] = bad.URL
	errs = ValidateProject(context.Background(), p, Options{})
	if len(errs) == 0 {
		t.Fatal("expected introspection failure")
	}
	if strings.Contains(errs[0].Error(), token) || strings.Contains(errs[0].Error(), "Authorization") {
		t.Fatalf("failure leaked auth: %s", errs[0].Error())
	}
}

func TestTemplateDepthRecursionAndArgs(t *testing.T) {
	schema, err := ParseSDL(sampleSDL, "schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	shallow, err := Generate(schema, "Query.user", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := "query user($id: ID!) {\n  user(id: $id) {\n    id\n    name\n    score\n    tags\n    meta\n  }\n}\n"
	if shallow.Query != want {
		t.Fatalf("depth 1:\n%s", shallow.Query)
	}
	if strings.Contains(shallow.Query, "verbose") || strings.Contains(shallow.Query, "$q") || strings.Contains(shallow.Query, "friend") || strings.Contains(shallow.Query, "posts") {
		t.Fatalf("depth 1 included optional or nested fields:\n%s", shallow.Query)
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(shallow.Variables), &vars); err != nil {
		t.Fatal(err)
	}
	if _, ok := vars["id"]; !ok || len(vars) != 1 {
		t.Fatalf("variables: %s", shallow.Variables)
	}

	deep, err := Generate(schema, "Query.user", 5)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(deep.Query, "friend {") != 1 {
		t.Fatalf("recursion:\n%s", deep.Query)
	}
	if !strings.Contains(deep.Query, "posts(limit: $limit)") || !strings.Contains(deep.Query, "$limit: Int!") {
		t.Fatalf("required nested arg:\n%s", deep.Query)
	}
	if err := json.Unmarshal([]byte(deep.Variables), &vars); err != nil {
		t.Fatal(err)
	}
	if _, ok := vars["limit"]; !ok {
		t.Fatalf("limit variable missing: %s", deep.Variables)
	}

	ping, err := Generate(schema, "Query.ping", 2)
	if err != nil {
		t.Fatal(err)
	}
	if ping.Query != "query ping {\n  ping\n}\n" || strings.TrimSpace(ping.Variables) != "{}" {
		t.Fatalf("scalar: %s vars %s", ping.Query, ping.Variables)
	}

	collide, err := ParseSDL("type Query { friend(id: ID!): User }\ntype User { id: ID!\n  friend(id: ID!): User\n}\n", "collide.graphql")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Generate(collide, "Query.friend", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Query, "friend(id: $id)") || !strings.Contains(got.Query, "friend(id: $friend_id)") {
		t.Fatalf("argument collision:\n%s", got.Query)
	}
	if strings.Count(got.Query, "friend(id:") != 2 {
		t.Fatalf("recursion should stop at the second User:\n%s", got.Query)
	}
}

func TestCheckResponse(t *testing.T) {
	schema, err := ParseSDL(sampleSDL, "schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	query := "query { user(id: \"1\") { id name score tags friend { id } meta } }"
	okBody := `{"data":{"user":{"id":"1","name":"a","score":1,"tags":["x"],"friend":{"id":"2"},"meta":{"k":1}}}}`
	if p := CheckResponse(schema, query, "", []byte(okBody), false); len(p) != 0 {
		t.Fatal(p)
	}
	cases := []struct {
		name  string
		body  string
		allow bool
		path  string
	}{
		{"null id", `{"data":{"user":{"id":null,"name":"a","score":1,"tags":[],"friend":null,"meta":null}}}`, false, "$.data.user.id"},
		{"wrong scalar", `{"data":{"user":{"id":"1","name":"a","score":"high","tags":[],"friend":null,"meta":null}}}`, false, "$.data.user.score"},
		{"extra field", `{"data":{"user":{"id":"1","name":"a","score":1,"tags":[],"friend":null,"meta":null,"extra":1}}}`, false, "$.data.user"},
		{"null in list", `{"data":{"user":{"id":"1","name":"a","score":1,"tags":[null],"friend":null,"meta":null}}}`, false, "$.data.user.tags[0]"},
		{"errors", `{"errors":[{"message":"nope"}],"data":{"user":null}}`, false, "$.errors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := CheckResponse(schema, query, "", []byte(tc.body), tc.allow)
			if len(problems) == 0 || !strings.Contains(problems[0], tc.path) {
				t.Fatalf("got %v", problems)
			}
		})
	}
	allowed := `{"errors":[{"message":"nope"}],"data":null}`
	if p := CheckResponse(schema, query, "", []byte(allowed), true); len(p) != 0 {
		t.Fatalf("allowErrors: %v", p)
	}
}

func projectWith(dir string, schema *model.GraphQLSchema, query string, vars map[string]any) *model.Project {
	req := map[string]any{"url": "/graphql", "query": query}
	if vars != nil {
		req["variables"] = vars
	}
	return &model.Project{
		SchemaVersion: "1.0",
		Name:          "p",
		SourceDir:     dir,
		Suites: []model.Suite{{
			Name: "s",
			Cases: []model.TestCase{{
				Name: "c",
				Steps: []model.Step{{
					Name: "g", Protocol: "graphql", Schema: schema, Request: req,
				}},
			}},
		}},
	}
}

func introBody() []byte {
	id, user, str := "ID", "User", "String"
	nonNullID := introTypeRef{Kind: "NON_NULL", OfType: &introTypeRef{Kind: "SCALAR", Name: &id}}
	env := introEnvelope{}
	env.Data.Schema = &introSchema{
		QueryType: &introName{Name: "Query"},
		Types: []introFullType{
			{Kind: "OBJECT", Name: "Query", Fields: []introField{{
				Name: "user",
				Args: []introValue{{Name: "id", Type: nonNullID}},
				Type: introTypeRef{Kind: "OBJECT", Name: &user},
			}}},
			{Kind: "OBJECT", Name: "User", Fields: []introField{
				{Name: "id", Type: nonNullID},
				{Name: "name", Type: introTypeRef{Kind: "SCALAR", Name: &str}},
				{Name: "friend", Type: introTypeRef{Kind: "OBJECT", Name: &user}},
			}},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		panic(err)
	}
	return b
}
