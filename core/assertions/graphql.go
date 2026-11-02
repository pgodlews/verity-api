package assertions

import (
	"context"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/spi"
)

// graphqlSchemaAssertion checks that a GraphQL response data value matches
// the operation selection set. errors must be absent unless allowErrors is set.
type graphqlSchemaAssertion struct{}

func (graphqlSchemaAssertion) Type() string { return "graphql_schema" }

func (a graphqlSchemaAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	allow := false
	if spec.Config != nil {
		allow, _ = spec.Config["allowErrors"].(bool)
	}
	schema, query, op, fail := graphqlInputs(spec)
	if fail != "" {
		return result(a.Type(), spec.Description, false, actx.Redactor.Redact(fail), "matches schema", nil), nil
	}
	problems := graphql.CheckResponse(schema, query, op, actx.Response.Body, allow)
	if len(problems) == 0 {
		return result(a.Type(), spec.Description, true, "response data matches the GraphQL selection set", "matches schema", "matches schema"), nil
	}
	msg := strings.Join(problems, "; ")
	return result(a.Type(), spec.Description, false, actx.Redactor.Redact(msg), "matches schema", problems[0]), nil
}

// AssertionInput is the schema and operation the runner attaches for a
// graphql step. Tests may set schemaSDL and query on the assertion config
// instead.
func graphqlInputs(spec spi.AssertionSpec) (schema *ast.Schema, query, op, fail string) {
	if spec.Config != nil {
		if in, ok := spec.Config["__gql"].(*graphql.AssertionInput); ok && in != nil {
			if in.Err != nil {
				return nil, "", "", in.Err.Error()
			}
			if in.Schema == nil {
				return nil, "", "", "GraphQL schema is required"
			}
			return in.Schema, in.Query, in.Operation, ""
		}
		if sdl, ok := spec.Config["schemaSDL"].(string); ok && sdl != "" {
			parsed, err := graphql.ParseSDL(sdl, "assertion.graphql")
			if err != nil {
				return nil, "", "", err.Error()
			}
			q, _ := spec.Config["query"].(string)
			name, _ := spec.Config["operationName"].(string)
			return parsed, q, name, ""
		}
	}
	return nil, "", "", "no GraphQL schema configured for this step"
}
