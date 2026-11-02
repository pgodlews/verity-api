package graphql

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator"
)

// issue is one GraphQL diagnostic. line and column are 1-based and zero when
// the diagnostic is not tied to the query text. onVariables selects the
// variables path instead of the query path.
type issue struct {
	msg         string
	line, col   int
	onVariables bool
}

func (i issue) text() string {
	if i.line > 0 {
		return fmt.Sprintf("line %d, column %d of the query: %s", i.line, i.col, i.msg)
	}
	return i.msg
}

func issuesFrom(err error) []issue {
	if err == nil {
		return nil
	}
	var list gqlerror.List
	if errors.As(err, &list) {
		out := make([]issue, 0, len(list))
		for _, e := range list {
			out = append(out, issueFrom(e))
		}
		return out
	}
	var one *gqlerror.Error
	if errors.As(err, &one) {
		return []issue{issueFrom(one)}
	}
	return []issue{{msg: err.Error()}}
}

func issueFrom(e *gqlerror.Error) issue {
	if e == nil {
		return issue{msg: "invalid GraphQL"}
	}
	out := issue{msg: e.Message}
	if len(e.Locations) > 0 {
		out.line = e.Locations[0].Line
		out.col = e.Locations[0].Column
	}
	return out
}

func loadQuery(schema *ast.Schema, query string) (*ast.QueryDocument, []issue) {
	doc, err := gqlparser.LoadQuery(schema, query)
	if err != nil {
		return nil, issuesFrom(err)
	}
	return doc, nil
}

func pickOperation(doc *ast.QueryDocument, name string) (*ast.OperationDefinition, error) {
	if doc == nil || len(doc.Operations) == 0 {
		return nil, fmt.Errorf("GraphQL query has no operation")
	}
	if name == "" {
		if len(doc.Operations) > 1 {
			return nil, fmt.Errorf("GraphQL query has more than one operation; set operationName")
		}
		return doc.Operations[0], nil
	}
	for _, op := range doc.Operations {
		if op.Name == name {
			return op, nil
		}
	}
	return nil, fmt.Errorf("GraphQL operation %q is not in the query", name)
}

// CheckQuery validates query against schema. Literal variable values are
// type-checked; values that still contain ${...} are left for run time.
func CheckQuery(schema *ast.Schema, query, operationName string, variables any) []issue {
	if schema == nil {
		return []issue{{msg: "GraphQL schema is required"}}
	}
	doc, problems := loadQuery(schema, query)
	if len(problems) > 0 {
		return problems
	}
	name := operationName
	if strings.Contains(name, "${") {
		// The name is still a placeholder. Check the only operation when
		// there is one; several operations stay unchecked until it resolves.
		name = ""
	}
	op, err := pickOperation(doc, name)
	if err != nil {
		if strings.Contains(operationName, "${") {
			return nil
		}
		return []issue{{msg: err.Error()}}
	}
	problems = append(problems, checkVariables(schema, op, variables)...)
	return problems
}

func checkVariables(schema *ast.Schema, op *ast.OperationDefinition, raw any) []issue {
	if op == nil {
		return nil
	}
	if s, ok := raw.(string); ok && strings.Contains(s, "${") {
		return nil
	}
	vars := map[string]any{}
	if raw != nil {
		parsed, ok := variableMap(raw)
		if !ok {
			return []issue{{msg: "GraphQL variables must be an object", onVariables: true}}
		}
		vars = parsed
	}
	defined := map[string]bool{}
	for _, d := range op.VariableDefinitions {
		defined[d.Variable] = true
	}
	var problems []issue
	kept := map[string]any{}
	var defs ast.VariableDefinitionList
	for name, value := range vars {
		if !defined[name] {
			problems = append(problems, issue{msg: fmt.Sprintf("unknown variable %q", name), onVariables: true})
			continue
		}
		if !isLiteral(value) {
			continue
		}
		kept[name] = value
	}
	for _, d := range op.VariableDefinitions {
		if _, present := vars[d.Variable]; present {
			continue
		}
		if d.Type != nil && d.Type.NonNull && d.DefaultValue == nil {
			problems = append(problems, issue{msg: fmt.Sprintf("required variable %q is missing", d.Variable), onVariables: true})
		}
	}
	for _, d := range op.VariableDefinitions {
		if _, ok := kept[d.Variable]; ok {
			defs = append(defs, d)
		}
	}
	if len(kept) == 0 {
		return problems
	}
	norm := normalizeJSON(kept)
	cp := *op
	cp.VariableDefinitions = defs
	if _, err := validator.VariableValues(schema, &cp, norm); err != nil {
		for _, item := range issuesFrom(err) {
			item.onVariables = true
			problems = append(problems, item)
		}
	}
	return problems
}

func variableMap(raw any) (map[string]any, bool) {
	switch m := raw.(type) {
	case map[string]any:
		return m, true
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, true
	default:
		return nil, false
	}
}

func isLiteral(v any) bool {
	switch t := v.(type) {
	case string:
		return !strings.Contains(t, "${")
	case map[string]any:
		for _, e := range t {
			if !isLiteral(e) {
				return false
			}
		}
		return true
	case []any:
		for _, e := range t {
			if !isLiteral(e) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func normalizeJSON(v map[string]any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}
