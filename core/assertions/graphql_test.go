package assertions

import (
	"context"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

const gqlSDL = `
type Query {
  user(id: ID!): User
}
type User {
  id: ID!
  name: String
  score: Int
  tags: [String!]
  extra: String
}
`

func TestGraphQLSchemaAssertion(t *testing.T) {
	query := `query { user(id: "1") { id name score tags } }`
	cfg := map[string]any{"schemaSDL": gqlSDL, "query": query}
	pass := evalGQL(t, `{"data":{"user":{"id":"1","name":"a","score":2,"tags":["x"]}}}`, cfg)
	if !pass.Passed {
		t.Fatal(pass.Message)
	}
	cases := []struct {
		name, body, path string
		allow            bool
	}{
		{"null in non-null", `{"data":{"user":{"id":null,"name":"a","score":2,"tags":[]}}}`, "$.data.user.id", false},
		{"wrong scalar", `{"data":{"user":{"id":"1","name":"a","score":"no","tags":[]}}}`, "$.data.user.score", false},
		{"extra field", `{"data":{"user":{"id":"1","name":"a","score":2,"tags":[],"nope":1}}}`, "$.data.user", false},
		{"errors present", `{"errors":[{"message":"boom"}],"data":{"user":null}}`, "$.errors", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evalGQL(t, tc.body, cfg)
			if got.Passed || !strings.Contains(got.Message, tc.path) {
				t.Fatalf("got passed=%v msg=%s", got.Passed, got.Message)
			}
		})
	}
	allow := map[string]any{"schemaSDL": gqlSDL, "query": query, "allowErrors": true}
	ok := evalGQL(t, `{"errors":[{"message":"boom"}],"data":null}`, allow)
	if !ok.Passed {
		t.Fatal(ok.Message)
	}
	missing := evalGQL(t, `{"data":{"user":{"id":"1","name":"a","score":1,"tags":[]}}}`, map[string]any{"query": query})
	if missing.Passed || !strings.Contains(missing.Message, "no GraphQL schema") {
		t.Fatalf("missing schema: %+v", missing)
	}
}

func evalGQL(t *testing.T, body string, cfg map[string]any) *spi.AssertionResult {
	t.Helper()
	a := &graphqlSchemaAssertion{}
	res, err := a.Evaluate(context.Background(), &spi.AssertionContext{
		Response: &spi.Response{Body: []byte(body)},
		Redactor: secrets.NewRedactor(),
	}, spi.AssertionSpec{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return res
}
