package graphql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// CheckResponse reports whether body.data matches the operation selection set
// and schema types. Custom scalars accept any JSON value. errors must be
// absent or empty unless allowErrors is set. Messages name a JSON path.
func CheckResponse(schema *ast.Schema, query, operationName string, body []byte, allowErrors bool) []string {
	if schema == nil {
		return []string{"GraphQL schema is required"}
	}
	doc, problems := loadQuery(schema, query)
	if len(problems) > 0 {
		out := make([]string, 0, len(problems))
		for _, p := range problems {
			out = append(out, p.text())
		}
		return out
	}
	op, err := pickOperation(doc, operationName)
	if err != nil {
		return []string{err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return []string{"response is not valid JSON"}
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return []string{"response is not a JSON object"}
	}
	var out []string
	errPresent := false
	if raw, exists := obj["errors"]; exists && raw != nil {
		list, isList := raw.([]any)
		if !isList {
			out = append(out, "$.errors: expected a list")
		} else if len(list) > 0 {
			errPresent = true
			if !allowErrors {
				out = append(out, "$.errors: GraphQL errors are present")
			}
		}
	}
	data, hasData := obj["data"]
	if !hasData || data == nil {
		if allowErrors && errPresent {
			return limitProblems(out)
		}
		if !hasData {
			out = append(out, "$.data: response data is missing")
		} else {
			out = append(out, "$.data: expected an object, got null")
		}
		return limitProblems(out)
	}
	dataObj, ok := data.(map[string]any)
	if !ok {
		out = append(out, "$.data: expected an object, got "+jsonKind(data))
		return limitProblems(out)
	}
	// The operation selection is checked against data. A synthetic field
	// definition is not required: walk the selection with no static parent
	// type beyond the root operation type.
	root := schema.Query
	switch op.Operation {
	case ast.Mutation:
		root = schema.Mutation
	case ast.Subscription:
		root = schema.Subscription
	}
	if root == nil {
		out = append(out, "$.data: schema has no "+string(op.Operation)+" type")
		return limitProblems(out)
	}
	checkSelection(schema, root, op.SelectionSet, dataObj, "$.data", &out)
	return limitProblems(out)
}

func limitProblems(in []string) []string {
	const max = 8
	if len(in) <= max {
		return in
	}
	return append(in[:max], fmt.Sprintf("and %d more", len(in)-max))
}

type expect struct {
	typ      *ast.Type
	sel      ast.SelectionSet
	optional bool
}

func checkSelection(schema *ast.Schema, parent *ast.Definition, set ast.SelectionSet, obj map[string]any, path string, out *[]string) {
	runtime := parent
	concrete := parent != nil && parent.Kind == ast.Object
	if name, ok := obj["__typename"].(string); ok && name != "" {
		if t := schema.Types[name]; t != nil {
			runtime = t
			concrete = t.Kind == ast.Object
		}
	}
	if typ, ok := obj["__typename"]; ok && typ != nil {
		if _, isStr := typ.(string); !isStr {
			*out = append(*out, path+".__typename: expected String, got "+jsonKind(typ))
		}
	}
	if !concrete && isAbstract(parent) {
		checkAbstract(schema, parent, set, obj, path, out)
		return
	}
	applySelection(schema, runtime, set, obj, path, nil, out)
}

func isAbstract(def *ast.Definition) bool {
	return def != nil && (def.Kind == ast.Interface || def.Kind == ast.Union)
}

// checkAbstract runs when the runtime type is still an interface or union.
// Fields selected on that type are always checked. Member fragments require
// __typename unless the object fits exactly one possible member.
func checkAbstract(schema *ast.Schema, parent *ast.Definition, set ast.SelectionSet, obj map[string]any, path string, out *[]string) {
	members := selectedMembers(schema, parent, set)
	if len(members) == 0 {
		applySelection(schema, parent, set, obj, path, nil, out)
		return
	}
	var matched []*ast.Definition
	for _, member := range members {
		if objectMatches(schema, member, set, obj) {
			matched = append(matched, member)
		}
	}
	if len(matched) == 1 {
		applySelection(schema, matched[0], set, obj, path, nil, out)
		return
	}
	allow := map[string]bool{}
	selectionKeys(set, allow)
	applySelection(schema, parent, set, obj, path, allow, out)
	*out = append(*out, path+": __typename needed to check an abstract type")
}

func applySelection(schema *ast.Schema, runtime *ast.Definition, set ast.SelectionSet, obj map[string]any, path string, allowExtra map[string]bool, out *[]string) {
	fields := map[string]*expect{}
	collect(schema, runtime, set, fields, true, out)
	for key, exp := range fields {
		val, present := obj[key]
		if !present {
			if !exp.optional {
				*out = append(*out, path+"."+key+": missing field")
			}
			continue
		}
		checkTyped(schema, exp.typ, exp.sel, val, path+"."+key, out)
	}
	for key := range obj {
		if key == "__typename" || allowExtra[key] {
			continue
		}
		if _, ok := fields[key]; !ok {
			*out = append(*out, path+": unexpected field "+gqlString(key))
		}
	}
}

func selectionKeys(set ast.SelectionSet, dst map[string]bool) {
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			key := s.Alias
			if key == "" {
				key = s.Name
			}
			dst[key] = true
		case *ast.InlineFragment:
			selectionKeys(s.SelectionSet, dst)
		case *ast.FragmentSpread:
			if s.Definition != nil {
				selectionKeys(s.Definition.SelectionSet, dst)
			}
		}
	}
}

// concreteMembers lists object types that can appear for an interface or union.
// Interface implementors are sorted by name. Union members keep declaration order.
func concreteMembers(schema *ast.Schema, def *ast.Definition) []*ast.Definition {
	if schema == nil || def == nil {
		return nil
	}
	if def.Kind == ast.Union {
		var out []*ast.Definition
		for _, name := range def.Types {
			if t := schema.Types[name]; t != nil && t.Kind == ast.Object {
				out = append(out, t)
			}
		}
		return out
	}
	if def.Kind != ast.Interface {
		return nil
	}
	names := make([]string, 0)
	for name, t := range schema.Types {
		if t != nil && t.Kind == ast.Object && implements(schema, t, def.Name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]*ast.Definition, 0, len(names))
	for _, name := range names {
		out = append(out, schema.Types[name])
	}
	return out
}

// selectedMembers is the concrete types a member fragment can pick for parent.
func selectedMembers(schema *ast.Schema, parent *ast.Definition, set ast.SelectionSet) []*ast.Definition {
	var out []*ast.Definition
	for _, member := range concreteMembers(schema, parent) {
		if memberFragmented(schema, parent, member, set) {
			out = append(out, member)
		}
	}
	return out
}

func memberFragmented(schema *ast.Schema, parent, member *ast.Definition, set ast.SelectionSet) bool {
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if memberFragmented(schema, parent, member, s.SelectionSet) {
				return true
			}
		case *ast.InlineFragment:
			if fragmentOnMember(schema, parent, member, s.TypeCondition) {
				return true
			}
			if s.TypeCondition == "" || typeMatches(schema, parent, s.TypeCondition) {
				if memberFragmented(schema, parent, member, s.SelectionSet) {
					return true
				}
			}
		case *ast.FragmentSpread:
			if s.Definition == nil {
				continue
			}
			if fragmentOnMember(schema, parent, member, s.Definition.TypeCondition) {
				return true
			}
		}
	}
	return false
}

func fragmentOnMember(schema *ast.Schema, parent, member *ast.Definition, cond string) bool {
	if cond == "" || parent == nil || cond == parent.Name || typeMatches(schema, parent, cond) {
		return false
	}
	return typeMatches(schema, member, cond)
}

// objectMatches reports whether obj can be this member: every non-null
// selected field is present and well typed, and no key is unknown to it.
func objectMatches(schema *ast.Schema, member *ast.Definition, set ast.SelectionSet, obj map[string]any) bool {
	for key := range obj {
		if key == "__typename" {
			continue
		}
		if lookupField(schema, member, key) == nil {
			return false
		}
	}
	fields := map[string]*expect{}
	var problems []string
	collect(schema, member, set, fields, true, &problems)
	if len(problems) > 0 {
		return false
	}
	for key, exp := range fields {
		val, present := obj[key]
		if !present {
			if exp.typ != nil && exp.typ.NonNull {
				return false
			}
			continue
		}
		if !valueMatches(schema, exp.typ, exp.sel, val) {
			return false
		}
	}
	return true
}

func valueMatches(schema *ast.Schema, typ *ast.Type, sel ast.SelectionSet, value any) bool {
	var problems []string
	checkTyped(schema, typ, sel, value, "$", &problems)
	for _, problem := range problems {
		if strings.Contains(problem, "unexpected field") {
			continue
		}
		return false
	}
	return true
}

func lookupField(schema *ast.Schema, def *ast.Definition, name string) *ast.FieldDefinition {
	seen := map[string]bool{}
	var walk func(*ast.Definition) *ast.FieldDefinition
	walk = func(d *ast.Definition) *ast.FieldDefinition {
		if d == nil || seen[d.Name] {
			return nil
		}
		seen[d.Name] = true
		for _, f := range d.Fields {
			if f != nil && f.Name == name {
				return f
			}
		}
		if schema == nil {
			return nil
		}
		for _, iface := range d.Interfaces {
			if got := walk(schema.Types[iface]); got != nil {
				return got
			}
		}
		return nil
	}
	return walk(def)
}

func collect(schema *ast.Schema, runtime *ast.Definition, set ast.SelectionSet, dst map[string]*expect, required bool, out *[]string) {
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			if s.Definition == nil || s.Definition.Type == nil {
				*out = append(*out, "field "+s.Name+" has no schema type")
				continue
			}
			key := s.Alias
			if key == "" {
				key = s.Name
			}
			prev := dst[key]
			if prev == nil {
				dst[key] = &expect{typ: s.Definition.Type, sel: s.SelectionSet, optional: !required}
				continue
			}
			if required {
				prev.optional = false
			}
			prev.sel = append(prev.sel, s.SelectionSet...)
		case *ast.InlineFragment:
			cond := s.TypeCondition
			if cond != "" && !typeMatches(schema, runtime, cond) {
				continue
			}
			next := runtime
			if cond != "" && schema.Types[cond] != nil && schema.Types[cond].Kind == ast.Object {
				next = schema.Types[cond]
			}
			collect(schema, next, s.SelectionSet, dst, required, out)
		case *ast.FragmentSpread:
			if s.Definition == nil {
				*out = append(*out, "unknown fragment "+s.Name)
				continue
			}
			cond := s.Definition.TypeCondition
			if !typeMatches(schema, runtime, cond) {
				continue
			}
			next := runtime
			if schema.Types[cond] != nil && schema.Types[cond].Kind == ast.Object {
				next = schema.Types[cond]
			}
			collect(schema, next, s.Definition.SelectionSet, dst, required, out)
		}
	}
}

func typeMatches(schema *ast.Schema, runtime *ast.Definition, cond string) bool {
	if cond == "" {
		return true
	}
	if runtime == nil {
		return false
	}
	if runtime.Name == cond || implements(schema, runtime, cond) {
		return true
	}
	if schema != nil {
		if def := schema.Types[cond]; def != nil && def.Kind == ast.Union {
			for _, member := range def.Types {
				if member == runtime.Name {
					return true
				}
			}
		}
	}
	return false
}

// implements walks interface parents. gqlparser records only the direct
// implements clause on Definition.Interfaces.
func implements(schema *ast.Schema, def *ast.Definition, cond string) bool {
	if def == nil || cond == "" {
		return false
	}
	seen := map[string]bool{}
	var walk func(*ast.Definition) bool
	walk = func(d *ast.Definition) bool {
		if d == nil || seen[d.Name] {
			return false
		}
		seen[d.Name] = true
		for _, iface := range d.Interfaces {
			if iface == cond {
				return true
			}
			if schema != nil && walk(schema.Types[iface]) {
				return true
			}
		}
		return false
	}
	return walk(def)
}

func checkTyped(schema *ast.Schema, typ *ast.Type, sel ast.SelectionSet, value any, path string, out *[]string) {
	if typ == nil {
		*out = append(*out, path+": missing schema type")
		return
	}
	if value == nil {
		if typ.NonNull {
			*out = append(*out, path+": expected non-null "+typeLabel(typ)+", got null")
		}
		return
	}
	if typ.Elem != nil {
		list, ok := value.([]any)
		if !ok {
			*out = append(*out, path+": expected a list, got "+jsonKind(value))
			return
		}
		for i, el := range list {
			checkTyped(schema, typ.Elem, sel, el, fmt.Sprintf("%s[%d]", path, i), out)
		}
		return
	}
	def := schema.Types[typ.NamedType]
	if def == nil {
		*out = append(*out, path+": unknown type "+typ.NamedType)
		return
	}
	switch def.Kind {
	case ast.Object, ast.Interface, ast.Union:
		obj, ok := value.(map[string]any)
		if !ok {
			*out = append(*out, path+": expected an object, got "+jsonKind(value))
			return
		}
		checkSelection(schema, def, sel, obj, path, out)
	case ast.Enum:
		s, ok := value.(string)
		if !ok {
			*out = append(*out, path+": expected enum "+def.Name+", got "+jsonKind(value))
			return
		}
		for _, ev := range def.EnumValues {
			if ev.Name == s {
				return
			}
		}
		*out = append(*out, path+": expected enum "+def.Name+", got a non-member")
	default:
		if msg := checkScalar(def, value); msg != "" {
			*out = append(*out, path+": "+msg)
		}
	}
}

func typeLabel(t *ast.Type) string {
	if t == nil {
		return "value"
	}
	if t.Elem != nil {
		return "list"
	}
	if t.NamedType != "" {
		return t.NamedType
	}
	return "value"
}

func checkScalar(def *ast.Definition, value any) string {
	switch def.Name {
	case "String":
		if _, ok := value.(string); !ok {
			return "expected String, got " + jsonKind(value)
		}
	case "Boolean":
		if _, ok := value.(bool); !ok {
			return "expected Boolean, got " + jsonKind(value)
		}
	case "Int":
		if !isInt32(value) {
			return "expected Int, got " + jsonKind(value)
		}
	case "Float":
		if !isFinite(value) {
			return "expected Float, got " + jsonKind(value)
		}
	case "ID":
		if _, ok := value.(string); ok || isIntegral(value) {
			return ""
		}
		return "expected ID, got " + jsonKind(value)
	default:
		// Custom scalars accept any JSON value.
	}
	return ""
}

// isInt32 accepts an integral number in the GraphQL Int range.
func isInt32(v any) bool {
	n, ok := asNumber(v)
	if !ok || !integralText(string(n)) {
		return false
	}
	if strings.ContainsAny(string(n), ".eE") {
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) {
			return false
		}
		return f >= math.MinInt32 && f <= math.MaxInt32
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil {
		return false
	}
	return i >= math.MinInt32 && i <= math.MaxInt32
}

// isIntegral accepts any integer, including values past 2^53. The text of a
// json.Number is kept so those values are not rounded through float64.
func isIntegral(v any) bool {
	n, ok := asNumber(v)
	if !ok {
		return false
	}
	s := string(n)
	if !integralText(s) {
		return false
	}
	if strings.ContainsAny(s, ".eE") {
		f, err := n.Float64()
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f)
	}
	return true
}

func integralText(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' || s[0] == '+' {
		i++
	}
	if i >= len(s) {
		return false
	}
	dot, exp := false, false
	digits := false
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digits = true
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && !exp && digits:
			exp = true
			if i+1 < len(s) && (s[i+1] == '+' || s[i+1] == '-') {
				i++
			}
			digits = false
		default:
			return false
		}
	}
	return digits
}

func isFinite(v any) bool {
	n, ok := asNumber(v)
	if !ok {
		return false
	}
	f, err := n.Float64()
	return err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}

func asNumber(v any) (json.Number, bool) {
	switch n := v.(type) {
	case json.Number:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", false
		}
		return json.Number(strconv.FormatFloat(n, 'f', -1, 64)), true
	case int:
		return json.Number(strconv.Itoa(n)), true
	case int32:
		return json.Number(strconv.FormatInt(int64(n), 10)), true
	case int64:
		return json.Number(strconv.FormatInt(n, 10)), true
	default:
		return "", false
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case float64, float32, int, int32, int64, json.Number:
		return "number"
	case []any:
		return "list"
	case map[string]any:
		return "object"
	default:
		return "value"
	}
}
