package graphql

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pgodlews/verity-api/core/verr"
)

// maxMemberFragments caps inline fragments generated for one abstract type.
const maxMemberFragments = 10

// Template is a generated operation and a variables skeleton.
type Template struct {
	Operation     string `json:"operation"`
	Kind          string `json:"kind"`
	OperationName string `json:"operationName"`
	Query         string `json:"query"`
	Variables     string `json:"variables"`
}

type varDecl struct {
	name  string
	typ   string
	value any
}

type obj struct {
	keys []string
	vals map[string]any
}

// Generate builds a query or mutation that selects scalar fields of
// Type.field down to depth. depth 1 is the field's own scalars. Required
// arguments become variables. Object fields that would re-enter a type
// already on the path are omitted.
func Generate(schema *ast.Schema, operation string, depth int) (Template, error) {
	if schema == nil {
		return Template{}, verr.New(verr.CodeValidation, "GraphQL schema is required")
	}
	if depth < 1 || depth > 20 {
		return Template{}, verr.Newf(verr.CodeValidation, "depth must be from 1 to 20, got %d", depth)
	}
	typeName, fieldName, ok := strings.Cut(operation, ".")
	if !ok || typeName == "" || fieldName == "" || strings.Contains(fieldName, ".") {
		return Template{}, verr.New(verr.CodeValidation, "operation must be Type.field, for example Query.user")
	}
	kind, root := rootOp(schema, typeName)
	if root == nil {
		return Template{}, verr.Newf(verr.CodeValidation, "operation type %q is not a root type", typeName)
	}
	field := findField(root, fieldName)
	if field == nil {
		return Template{}, verr.Newf(verr.CodeValidation, "type %s has no field %q", typeName, fieldName)
	}
	vars := []varDecl{}
	used := map[string]bool{}
	args := bindArgs(schema, field, &vars, used)
	selection := renderRoot(schema, field, depth, &vars, used)
	return Template{
		Operation:     operation,
		Kind:          kind,
		OperationName: field.Name,
		Query:         formatQuery(kind, field.Name, vars, field.Name, args, selection),
		Variables:     variablesJSON(vars),
	}, nil
}

// ListTemplates returns a template for every root field at depth.
func ListTemplates(schema *ast.Schema, depth int) ([]Template, error) {
	if schema == nil {
		return nil, verr.New(verr.CodeValidation, "GraphQL schema is required")
	}
	var out []Template
	for _, root := range []*ast.Definition{schema.Query, schema.Mutation, schema.Subscription} {
		if root == nil {
			continue
		}
		for _, f := range root.Fields {
			if f == nil || strings.HasPrefix(f.Name, "__") {
				continue
			}
			item, err := Generate(schema, root.Name+"."+f.Name, depth)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
	}
	return out, nil
}

func rootOp(schema *ast.Schema, name string) (string, *ast.Definition) {
	switch {
	case schema.Query != nil && schema.Query.Name == name:
		return "query", schema.Query
	case schema.Mutation != nil && schema.Mutation.Name == name:
		return "mutation", schema.Mutation
	case schema.Subscription != nil && schema.Subscription.Name == name:
		return "subscription", schema.Subscription
	default:
		return "", nil
	}
}

func findField(def *ast.Definition, name string) *ast.FieldDefinition {
	if def == nil {
		return nil
	}
	for _, f := range def.Fields {
		if f != nil && f.Name == name {
			return f
		}
	}
	return nil
}

func formatQuery(kind, opName string, vars []varDecl, field, args, selection string) string {
	var b strings.Builder
	b.WriteString(kind)
	b.WriteByte(' ')
	b.WriteString(opName)
	if len(vars) > 0 {
		b.WriteByte('(')
		for i, v := range vars {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteByte('$')
			b.WriteString(v.name)
			b.WriteString(": ")
			b.WriteString(v.typ)
		}
		b.WriteByte(')')
	}
	b.WriteString(" {\n  ")
	b.WriteString(field)
	b.WriteString(args)
	if selection != "" {
		b.WriteString(" {\n")
		b.WriteString(selection)
		b.WriteString("\n  }")
	}
	b.WriteString("\n}\n")
	return b.String()
}

func renderRoot(schema *ast.Schema, field *ast.FieldDefinition, depth int, vars *[]varDecl, used map[string]bool) string {
	def := namedDef(schema, field.Type)
	if def == nil || def.Kind == ast.Scalar || def.Kind == ast.Enum {
		return ""
	}
	if def.Kind == ast.Union || def.Kind == ast.Interface {
		return renderAbstract(schema, def, depth, map[string]bool{def.Name: true}, vars, used, "    ")
	}
	return renderFields(schema, def, depth, map[string]bool{}, vars, used, "    ")
}

func renderFields(schema *ast.Schema, def *ast.Definition, depth int, stack map[string]bool, vars *[]varDecl, used map[string]bool, indent string) string {
	var b strings.Builder
	wrote := false
	for _, f := range def.Fields {
		if f == nil || strings.HasPrefix(f.Name, "__") {
			continue
		}
		line, ok := renderField(schema, f, depth, stack, vars, used, indent)
		if !ok {
			continue
		}
		if wrote {
			b.WriteByte('\n')
		}
		wrote = true
		b.WriteString(line)
	}
	if !wrote {
		return indent + "__typename"
	}
	return b.String()
}

func renderField(schema *ast.Schema, f *ast.FieldDefinition, depth int, stack map[string]bool, vars *[]varDecl, used map[string]bool, indent string) (string, bool) {
	def := namedDef(schema, f.Type)
	if def == nil {
		return "", false
	}
	switch def.Kind {
	case ast.Scalar, ast.Enum:
		return indent + f.Name + bindArgs(schema, f, vars, used), true
	case ast.Union, ast.Interface:
		if stack[def.Name] {
			return "", false
		}
		stack[def.Name] = true
		inner := renderAbstract(schema, def, depth, stack, vars, used, indent+"  ")
		delete(stack, def.Name)
		return indent + f.Name + bindArgs(schema, f, vars, used) + " {\n" + inner + "\n" + indent + "}", true
	case ast.Object:
		if depth <= 1 || stack[def.Name] {
			return "", false
		}
		stack[def.Name] = true
		inner := renderFields(schema, def, depth-1, stack, vars, used, indent+"  ")
		delete(stack, def.Name)
		return indent + f.Name + bindArgs(schema, f, vars, used) + " {\n" + inner + "\n" + indent + "}", true
	default:
		return "", false
	}
}

// renderAbstract selects __typename, the interface's own fields, and one
// inline fragment per concrete member. More than maxMemberFragments members
// are cut, with a # truncated comment.
func renderAbstract(schema *ast.Schema, def *ast.Definition, depth int, stack map[string]bool, vars *[]varDecl, used map[string]bool, indent string) string {
	var b strings.Builder
	b.WriteString(indent + "__typename")
	if def.Kind == ast.Interface {
		for _, f := range def.Fields {
			if f == nil || strings.HasPrefix(f.Name, "__") {
				continue
			}
			line, ok := renderField(schema, f, depth, stack, vars, used, indent)
			if !ok {
				continue
			}
			b.WriteByte('\n')
			b.WriteString(line)
		}
	}
	members := concreteMembers(schema, def)
	truncated := len(members) > maxMemberFragments
	if truncated {
		members = members[:maxMemberFragments]
	}
	skip := def
	if def.Kind != ast.Interface {
		skip = nil
	}
	for _, member := range members {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString("... on ")
		b.WriteString(member.Name)
		b.WriteString(" {\n")
		b.WriteString(renderMemberFields(schema, skip, member, depth, stack, vars, used, indent+"  "))
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteByte('}')
	}
	if truncated {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString("# truncated")
	}
	return b.String()
}

func renderMemberFields(schema *ast.Schema, skip, member *ast.Definition, depth int, stack map[string]bool, vars *[]varDecl, used map[string]bool, indent string) string {
	var b strings.Builder
	wrote := false
	for _, f := range member.Fields {
		if f == nil || strings.HasPrefix(f.Name, "__") {
			continue
		}
		if skip != nil && findField(skip, f.Name) != nil {
			continue
		}
		line, ok := renderField(schema, f, depth, stack, vars, used, indent)
		if !ok {
			continue
		}
		if wrote {
			b.WriteByte('\n')
		}
		wrote = true
		b.WriteString(line)
	}
	if !wrote {
		return indent + "__typename"
	}
	return b.String()
}

func namedDef(schema *ast.Schema, t *ast.Type) *ast.Definition {
	name := namedType(t)
	if name == "" || schema == nil {
		return nil
	}
	return schema.Types[name]
}

func namedType(t *ast.Type) string {
	for t != nil {
		if t.NamedType != "" {
			return t.NamedType
		}
		t = t.Elem
	}
	return ""
}

func bindArgs(schema *ast.Schema, f *ast.FieldDefinition, vars *[]varDecl, used map[string]bool) string {
	if f == nil {
		return ""
	}
	var parts []string
	for _, a := range f.Arguments {
		if a == nil || a.Type == nil || !a.Type.NonNull || a.DefaultValue != nil {
			continue
		}
		name := uniqueVar(used, f.Name, a.Name)
		*vars = append(*vars, varDecl{
			name:  name,
			typ:   a.Type.String(),
			value: skeleton(schema, a.Type, map[string]bool{}),
		})
		parts = append(parts, a.Name+": $"+name)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func uniqueVar(used map[string]bool, field, arg string) string {
	if arg != "" && !used[arg] {
		used[arg] = true
		return arg
	}
	name := field + "_" + arg
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s_%s_%d", field, arg, i)
	}
	used[name] = true
	return name
}

func skeleton(schema *ast.Schema, t *ast.Type, stack map[string]bool) any {
	if t == nil {
		return nil
	}
	if t.Elem != nil {
		return []any{}
	}
	def := schema.Types[t.NamedType]
	if def == nil {
		return nil
	}
	switch def.Kind {
	case ast.Enum:
		if len(def.EnumValues) > 0 && def.EnumValues[0] != nil {
			return def.EnumValues[0].Name
		}
		return nil
	case ast.InputObject:
		if stack[def.Name] {
			return nil
		}
		stack[def.Name] = true
		defer delete(stack, def.Name)
		o := obj{vals: map[string]any{}}
		for _, f := range def.Fields {
			if f == nil || f.Type == nil || !f.Type.NonNull || f.DefaultValue != nil {
				continue
			}
			o.keys = append(o.keys, f.Name)
			o.vals[f.Name] = skeleton(schema, f.Type, stack)
		}
		return o
	default:
		switch def.Name {
		case "String", "ID":
			return ""
		case "Int", "Float":
			return 0
		case "Boolean":
			return false
		default:
			return nil
		}
	}
}

func variablesJSON(vars []varDecl) string {
	if len(vars) == 0 {
		return "{}\n"
	}
	var b strings.Builder
	b.WriteString("{\n")
	for i, v := range vars {
		b.WriteString("  ")
		key, _ := json.Marshal(v.name)
		b.Write(key)
		b.WriteString(": ")
		writeJSON(&b, v.value, "  ")
		if i+1 < len(vars) {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.String()
}

func writeJSON(b *strings.Builder, v any, indent string) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case string:
		raw, _ := json.Marshal(t)
		b.Write(raw)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(t))
	case float64:
		raw, _ := json.Marshal(t)
		b.Write(raw)
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, el := range t {
			b.WriteString(indent + "  ")
			writeJSON(b, el, indent+"  ")
			if i+1 < len(t) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case obj:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(indent + "  ")
			raw, _ := json.Marshal(k)
			b.Write(raw)
			b.WriteString(": ")
			writeJSON(b, t.vals[k], indent+"  ")
			if i+1 < len(t.keys) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			b.WriteString("null")
			return
		}
		b.Write(raw)
	}
}
