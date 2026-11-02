package mock

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Sample sizes are capped so a hostile or careless schema cannot allocate
// gigabytes while the server is starting. Integer spans are clamped the same
// way: hi-lo can wrap past MaxInt, and rand.IntN panics on a non-positive
// argument.
const (
	maxSampleItems = 32
	maxSampleLen   = 1024
	maxSampleSpan  = 10000
	maxSampleDepth = 32
)

// gen produces deterministic samples from a JSON Schema / OpenAPI schema.
// The same seed yields the same values when properties are visited in sorted
// order. allOf is merged. oneOf and anyOf use the first alternative when
// generating. Validation requires oneOf to match exactly one branch and anyOf
// at least one. At a cycle or the depth limit, optional properties are
// omitted, arrays are empty, and null is emitted only for a nullable schema.
type gen struct {
	root *map[string]any
	rng  *rand.Rand
	seen map[string]bool
}

func newGen(root map[string]any, seed int64) *gen {
	return &gen{
		root: &root,
		rng:  rand.New(rand.NewPCG(uint64(seed), uint64(seed)+1)),
		seen: map[string]bool{},
	}
}

func (g *gen) sample(schema any) any {
	v, omit := g.sampleInner(schema, 0)
	if omit {
		return g.cycleFallback(schema)
	}
	return v
}

// sampleInner reports omit when schema is a cycle or past the depth limit.
// Callers decide whether that value is dropped, an empty array, or null.
func (g *gen) sampleInner(schema any, depth int) (any, bool) {
	if depth > maxSampleDepth {
		return nil, true
	}
	m := asMap(schema)
	if m == nil {
		return "sample", false
	}
	if ref, ok := m["$ref"].(string); ok {
		if g.seen[ref] {
			return nil, true
		}
		g.seen[ref] = true
		defer delete(g.seen, ref)
		target := resolvePtr(*g.root, ref)
		if len(m) == 1 {
			return g.sampleInner(target, depth+1)
		}
		return g.sampleInner(mergeAllOf([]any{target, without(m, "$ref")}, *g.root, 0), depth+1)
	}
	if ex, ok := m["example"]; ok {
		return ex, false
	}
	if c, ok := m["const"]; ok {
		return c, false
	}
	if parts, ok := m["allOf"].([]any); ok && len(parts) > 0 {
		merged := mergeAllOf(append([]any{without(m, "allOf")}, parts...), *g.root, 0)
		return g.sampleInner(merged, depth+1)
	}
	if parts, ok := m["oneOf"].([]any); ok && len(parts) > 0 {
		return g.sampleInner(parts[0], depth+1)
	}
	if parts, ok := m["anyOf"].([]any); ok && len(parts) > 0 {
		return g.sampleInner(parts[0], depth+1)
	}
	if en := asSlice(m["enum"]); len(en) > 0 {
		return en[g.rng.IntN(len(en))], false
	}
	switch schemaType(m) {
	case "object":
		return g.object(m, depth), false
	case "array":
		return g.array(m, depth), false
	case "integer":
		return g.integer(m), false
	case "number":
		return g.number(m), false
	case "boolean":
		return g.rng.IntN(2) == 1, false
	case "null":
		return nil, false
	default:
		return g.str(m), false
	}
}

func (g *gen) object(m map[string]any, depth int) any {
	props := asMap(m["properties"])
	if len(props) == 0 {
		if ap := asMap(m["additionalProperties"]); ap != nil {
			v, omit := g.sampleInner(ap, depth+1)
			if omit {
				v = g.cycleFallback(ap)
			}
			return map[string]any{"extra": v}
		}
		return map[string]any{}
	}
	req := map[string]bool{}
	for _, r := range asSlice(m["required"]) {
		if s, ok := r.(string); ok {
			req[s] = true
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		v, omit := g.sampleInner(props[k], depth+1)
		if omit {
			if !keepCycle(props[k], req[k], *g.root) {
				continue
			}
			v = g.cycleFallback(props[k])
		}
		out[k] = v
	}
	return out
}

func (g *gen) array(m map[string]any, depth int) any {
	n := 1
	if g.rng.IntN(2) == 1 {
		n = 2
	}
	if v, ok := asInt(m["minItems"]); ok && n < v {
		n = v
	}
	if v, ok := asInt(m["maxItems"]); ok && n > v {
		n = v
	}
	if n > maxSampleItems {
		n = maxSampleItems
	}
	if n < 0 {
		n = 0
	}
	if n == 0 {
		return []any{}
	}
	item := m["items"]
	first, omit := g.sampleInner(item, depth+1)
	if omit {
		return []any{}
	}
	out := make([]any, n)
	out[0] = first
	for i := 1; i < n; i++ {
		v, again := g.sampleInner(item, depth+1)
		if again {
			v = g.cycleFallback(item)
		}
		out[i] = v
	}
	return out
}

// keepCycle reports whether a value at the recursion limit is written.
// Optional properties are omitted. Arrays are kept as [] and nullable
// schemas are kept as null.
func keepCycle(schema any, required bool, root map[string]any) bool {
	if schemaNullable(schema, root, 0) {
		return true
	}
	if schemaType(resolveSchemaMap(schema, root, 0)) == "array" {
		return true
	}
	return required
}

func (g *gen) cycleFallback(schema any) any {
	if schemaNullable(schema, *g.root, 0) {
		return nil
	}
	switch schemaType(resolveSchemaMap(schema, *g.root, 0)) {
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	case "integer":
		return 0
	case "number":
		return 0.0
	case "boolean":
		return false
	case "null":
		return nil
	default:
		return "sample"
	}
}

func (g *gen) integer(m map[string]any) int {
	lo, hi, hasLo, hasHi := intBounds(m)
	if !hasLo && !hasHi {
		return 0
	}
	if !hasLo {
		lo = hi
	}
	if !hasHi {
		hi = lo
	}
	if lo > hi {
		lo = hi
	}
	if lo == hi {
		return lo
	}
	span := boundedSpan(lo, hi, maxSampleSpan)
	if span <= 0 {
		return lo
	}
	return lo + g.rng.IntN(span+1)
}

// boundedSpan is hi-lo clamped to cap, without wrapping past MaxInt and
// without returning a negative or overflowing lo+span.
func boundedSpan(lo, hi, cap int) int {
	if lo >= hi || cap <= 0 {
		return 0
	}
	span := cap
	if lo >= 0 || hi <= math.MaxInt+lo {
		diff := hi - lo
		if diff < span {
			span = diff
		}
	}
	if lo > math.MaxInt-span {
		span = math.MaxInt - lo
	}
	if span < 0 {
		return 0
	}
	return span
}

func (g *gen) number(m map[string]any) float64 {
	lo, hi, hasLo, hasHi := floatBounds(m)
	if !hasLo && !hasHi {
		return 0
	}
	if !hasLo {
		lo = hi
	}
	if !hasHi {
		hi = lo
	}
	if lo > hi {
		lo = hi
	}
	if lo == hi {
		return lo
	}
	return lo + (hi-lo)*float64(g.rng.IntN(1000))/1000
}

func (g *gen) str(m map[string]any) string {
	minL, maxL := 0, -1
	if v, ok := asInt(m["minLength"]); ok {
		minL = v
	}
	if v, ok := asInt(m["maxLength"]); ok {
		maxL = v
	}
	minL, maxL = capStrLen(minL, maxL)
	if f, _ := m["format"].(string); f != "" {
		if s, ok := formatSample(f); ok {
			return fitLen(s, minL, maxL)
		}
	}
	if minL == 0 && maxL < 0 {
		return "sample"
	}
	n := 6
	if minL > n {
		n = minL
	}
	if maxL >= 0 && n > maxL {
		n = maxL
	}
	if maxL >= 0 && minL <= maxL {
		n = minL
		if maxL > minL {
			span := maxL - minL
			if span > maxSampleLen {
				span = maxSampleLen
			}
			n = minL + g.rng.IntN(span+1)
			if n > maxL {
				n = maxL
			}
		}
	} else if minL > 0 {
		n = minL
	}
	if n < 0 {
		n = 0
	}
	if n > maxSampleLen {
		n = maxSampleLen
	}
	return strings.Repeat("a", n)
}

func capStrLen(minL, maxL int) (int, int) {
	if minL < 0 {
		minL = 0
	}
	if minL > maxSampleLen {
		minL = maxSampleLen
	}
	if maxL > maxSampleLen {
		maxL = maxSampleLen
	}
	return minL, maxL
}

func formatSample(f string) (string, bool) {
	switch f {
	case "date-time":
		return "2020-01-01T00:00:00Z", true
	case "date":
		return "2020-01-01", true
	case "time":
		return "00:00:00Z", true
	case "email":
		return "user@example.com", true
	case "uuid":
		return "00000000-0000-4000-8000-000000000000", true
	case "uri", "url", "uri-reference":
		return "https://example.com/resource", true
	case "hostname":
		return "example.com", true
	case "ipv4":
		return "192.0.2.1", true
	case "ipv6":
		return "2001:db8::1", true
	case "byte":
		return base64.StdEncoding.EncodeToString([]byte("sample")), true
	case "binary", "password":
		return "sample", true
	default:
		return "", false
	}
}

func fitLen(s string, minL, maxL int) string {
	minL, maxL = capStrLen(minL, maxL)
	n := utf8.RuneCountInString(s)
	if maxL >= 0 && n > maxL {
		r := []rune(s)
		s = string(r[:maxL])
		n = maxL
	}
	if n < minL {
		s += strings.Repeat("a", minL-n)
	}
	return s
}

func schemaType(m map[string]any) string {
	switch t := m["type"].(type) {
	case string:
		if t == "null" {
			if _, ok := m["properties"]; ok {
				return "object"
			}
		}
		return t
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "null" {
				return s
			}
		}
		return "null"
	}
	if _, ok := m["properties"]; ok {
		return "object"
	}
	if _, ok := m["items"]; ok {
		return "array"
	}
	return "string"
}

func mergeAllOf(parts []any, root map[string]any, depth int) map[string]any {
	if depth > 16 {
		return map[string]any{}
	}
	out := map[string]any{}
	props := map[string]any{}
	var req []any
	haveProps := false
	for _, p := range parts {
		pm := asMap(p)
		if pm == nil {
			continue
		}
		if ref, ok := pm["$ref"].(string); ok {
			target := resolvePtr(root, ref)
			pm = mergeAllOf([]any{target, without(pm, "$ref")}, root, depth+1)
		}
		if nested, ok := pm["allOf"].([]any); ok {
			pm = mergeAllOf(append([]any{without(pm, "allOf")}, nested...), root, depth+1)
		}
		for k, v := range pm {
			switch k {
			case "properties":
				haveProps = true
				for pk, pv := range asMap(v) {
					props[pk] = pv
				}
			case "required":
				req = append(req, asSlice(v)...)
			case "allOf", "$ref":
			default:
				out[k] = v
			}
		}
	}
	if haveProps {
		out["properties"] = props
	}
	if len(req) > 0 {
		out["required"] = req
	}
	return out
}

func without(m map[string]any, drop ...string) map[string]any {
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if !skip[k] {
			out[k] = v
		}
	}
	return out
}

func resolvePtr(root map[string]any, ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = root
	for _, raw := range strings.Split(ref[2:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		m := asMap(cur)
		if m == nil {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	default:
		return nil
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		if n < math.MinInt || n > math.MaxInt {
			return 0, false
		}
		return int(n), true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	default:
		return 0, false
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func intBounds(m map[string]any) (lo, hi int, hasLo, hasHi bool) {
	if v, ok := asFloat(m["minimum"]); ok {
		lo = int(math.Ceil(v))
		hasLo = true
		if b, ok := m["exclusiveMinimum"].(bool); ok && b {
			lo++
		}
	}
	if v, ok := asFloat(m["exclusiveMinimum"]); ok {
		lo = int(math.Floor(v)) + 1
		hasLo = true
	}
	if v, ok := asFloat(m["maximum"]); ok {
		hi = int(math.Floor(v))
		hasHi = true
		if b, ok := m["exclusiveMaximum"].(bool); ok && b {
			hi--
		}
	}
	if v, ok := asFloat(m["exclusiveMaximum"]); ok {
		hi = int(math.Ceil(v)) - 1
		hasHi = true
	}
	return lo, hi, hasLo, hasHi
}

func floatBounds(m map[string]any) (lo, hi float64, hasLo, hasHi bool) {
	if v, ok := asFloat(m["minimum"]); ok {
		lo, hasLo = v, true
		if b, ok := m["exclusiveMinimum"].(bool); ok && b {
			lo = math.Nextafter(lo, math.Inf(1))
		}
	}
	if v, ok := asFloat(m["exclusiveMinimum"]); ok {
		lo, hasLo = math.Nextafter(v, math.Inf(1)), true
	}
	if v, ok := asFloat(m["maximum"]); ok {
		hi, hasHi = v, true
		if b, ok := m["exclusiveMaximum"].(bool); ok && b {
			hi = math.Nextafter(hi, math.Inf(-1))
		}
	}
	if v, ok := asFloat(m["exclusiveMaximum"]); ok {
		hi, hasHi = math.Nextafter(v, math.Inf(-1)), true
	}
	return lo, hi, hasLo, hasHi
}

// validateSchema checks value against an OpenAPI schema. Paths are dotted
// (body.name, query.limit). Errors are sorted.
func validateSchema(schema any, value any, path string, root map[string]any, depth int) []string {
	if depth > 32 {
		return []string{path + ": schema is too deep"}
	}
	m := asMap(schema)
	if m == nil {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok {
		target := resolvePtr(root, ref)
		if len(m) == 1 {
			return validateSchema(target, value, path, root, depth+1)
		}
		return validateSchema(mergeAllOf([]any{target, without(m, "$ref")}, root, 0), value, path, root, depth+1)
	}
	if parts, ok := m["allOf"].([]any); ok && len(parts) > 0 {
		merged := mergeAllOf(append([]any{without(m, "allOf")}, parts...), root, 0)
		return validateSchema(merged, value, path, root, depth+1)
	}
	if parts, ok := m["oneOf"].([]any); ok && len(parts) > 0 {
		return validateOneOf(parts, value, path, root, depth)
	}
	if parts, ok := m["anyOf"].([]any); ok && len(parts) > 0 {
		return validateAnyOf(parts, value, path, root, depth)
	}
	if value == nil {
		if nullable(m) || schemaType(m) == "null" {
			return nil
		}
		return []string{path + ": must not be null"}
	}
	if en := asSlice(m["enum"]); len(en) > 0 && !enumHas(en, value) {
		return []string{fmt.Sprintf("%s: must be one of %s", path, enumText(en))}
	}
	if c, ok := m["const"]; ok && !sameValue(c, value) {
		return []string{fmt.Sprintf("%s: must be %v", path, c)}
	}
	var errs []string
	switch schemaType(m) {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return []string{path + ": expected object"}
		}
		props := asMap(m["properties"])
		req := map[string]bool{}
		for _, r := range asSlice(m["required"]) {
			if s, ok := r.(string); ok {
				req[s] = true
			}
		}
		for name := range req {
			if _, ok := obj[name]; !ok {
				errs = append(errs, joinPath(path, name)+": required")
			}
		}
		ap := m["additionalProperties"]
		apSchema := asMap(ap)
		closed := false
		if b, ok := ap.(bool); ok && !b {
			closed = true
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := joinPath(path, k)
			if ps, ok := props[k]; ok {
				errs = append(errs, validateSchema(ps, obj[k], child, root, depth+1)...)
				continue
			}
			if closed {
				errs = append(errs, child+": unexpected property")
				continue
			}
			if apSchema != nil {
				errs = append(errs, validateSchema(apSchema, obj[k], child, root, depth+1)...)
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return []string{path + ": expected array"}
		}
		if v, ok := asInt(m["minItems"]); ok && len(arr) < v {
			errs = append(errs, fmt.Sprintf("%s: length must be >= %d", path, v))
		}
		if v, ok := asInt(m["maxItems"]); ok && len(arr) > v {
			errs = append(errs, fmt.Sprintf("%s: length must be <= %d", path, v))
		}
		for i, el := range arr {
			errs = append(errs, validateSchema(m["items"], el, fmt.Sprintf("%s[%d]", path, i), root, depth+1)...)
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return []string{path + ": expected string"}
		}
		n := utf8.RuneCountInString(s)
		if v, ok := asInt(m["minLength"]); ok && n < v {
			errs = append(errs, fmt.Sprintf("%s: length must be >= %d", path, v))
		}
		if v, ok := asInt(m["maxLength"]); ok && n > v {
			errs = append(errs, fmt.Sprintf("%s: length must be <= %d", path, v))
		}
		if p, ok := m["pattern"].(string); ok && p != "" {
			re, err := regexp.Compile(p)
			if err != nil {
				errs = append(errs, path+": invalid pattern")
			} else if !re.MatchString(s) {
				errs = append(errs, path+": must match pattern")
			}
		}
		if f, ok := m["format"].(string); ok && !formatOK(f, s) {
			errs = append(errs, path+": must be a valid "+f)
		}
	case "integer":
		n, ok := asFloat(value)
		if !ok || n != math.Trunc(n) {
			return []string{path + ": expected integer"}
		}
		errs = append(errs, numberBounds(m, n, path)...)
	case "number":
		n, ok := asFloat(value)
		if !ok {
			return []string{path + ": expected number"}
		}
		errs = append(errs, numberBounds(m, n, path)...)
	case "boolean":
		if _, ok := value.(bool); !ok {
			return []string{path + ": expected boolean"}
		}
	}
	sort.Strings(errs)
	return errs
}

func validateOneOf(parts []any, value any, path string, root map[string]any, depth int) []string {
	matches := 0
	var first []string
	for i, p := range parts {
		errs := validateSchema(p, value, path, root, depth+1)
		if len(errs) == 0 {
			matches++
			continue
		}
		if i == 0 {
			first = errs
		}
	}
	switch matches {
	case 1:
		return nil
	case 0:
		if len(first) == 0 {
			return []string{path + ": matches no alternative"}
		}
		return first
	default:
		return []string{path + ": must match exactly one alternative"}
	}
}

func validateAnyOf(parts []any, value any, path string, root map[string]any, depth int) []string {
	var first []string
	for i, p := range parts {
		errs := validateSchema(p, value, path, root, depth+1)
		if len(errs) == 0 {
			return nil
		}
		if i == 0 {
			first = errs
		}
	}
	if len(first) == 0 {
		return []string{path + ": matches no alternative"}
	}
	return first
}

func schemaNullable(schema any, root map[string]any, depth int) bool {
	if depth > 8 {
		return false
	}
	m := asMap(schema)
	if m == nil {
		return false
	}
	if nullable(m) {
		return true
	}
	if ref, ok := m["$ref"].(string); ok && len(m) == 1 {
		return schemaNullable(resolvePtr(root, ref), root, depth+1)
	}
	return false
}

func resolveSchemaMap(schema any, root map[string]any, depth int) map[string]any {
	if depth > 8 {
		return map[string]any{}
	}
	m := asMap(schema)
	if m == nil {
		return map[string]any{}
	}
	ref, ok := m["$ref"].(string)
	if !ok {
		return m
	}
	t := asMap(resolvePtr(root, ref))
	if t == nil {
		return m
	}
	if len(m) == 1 {
		return resolveSchemaMap(t, root, depth+1)
	}
	return mergeAllOf([]any{t, without(m, "$ref")}, root, 0)
}

func numberBounds(m map[string]any, n float64, path string) []string {
	var errs []string
	if v, ok := asFloat(m["minimum"]); ok {
		excl, _ := m["exclusiveMinimum"].(bool)
		if excl {
			if n <= v {
				errs = append(errs, fmt.Sprintf("%s: must be > %v", path, v))
			}
		} else if n < v {
			errs = append(errs, fmt.Sprintf("%s: must be >= %v", path, v))
		}
	}
	if v, ok := asFloat(m["exclusiveMinimum"]); ok {
		if n <= v {
			errs = append(errs, fmt.Sprintf("%s: must be > %v", path, v))
		}
	}
	if v, ok := asFloat(m["maximum"]); ok {
		excl, _ := m["exclusiveMaximum"].(bool)
		if excl {
			if n >= v {
				errs = append(errs, fmt.Sprintf("%s: must be < %v", path, v))
			}
		} else if n > v {
			errs = append(errs, fmt.Sprintf("%s: must be <= %v", path, v))
		}
	}
	if v, ok := asFloat(m["exclusiveMaximum"]); ok {
		if n >= v {
			errs = append(errs, fmt.Sprintf("%s: must be < %v", path, v))
		}
	}
	return errs
}

func nullable(m map[string]any) bool {
	if b, ok := m["nullable"].(bool); ok && b {
		return true
	}
	switch t := m["type"].(type) {
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s == "null" {
				return true
			}
		}
	case string:
		return t == "null"
	}
	return false
}

func enumHas(en []any, value any) bool {
	for _, e := range en {
		if sameValue(e, value) {
			return true
		}
	}
	return false
}

func sameValue(a, b any) bool {
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af == bf
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func enumText(en []any) string {
	parts := make([]string, len(en))
	for i, e := range en {
		parts[i] = fmt.Sprint(e)
	}
	return strings.Join(parts, ", ")
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func formatOK(format, s string) bool {
	switch format {
	case "date":
		return regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(s)
	case "date-time":
		return regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`).MatchString(s)
	case "email":
		return strings.Contains(s, "@") && !strings.ContainsAny(s, " \t")
	case "uuid":
		return regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(s)
	case "ipv4", "ipv6":
		ip := net.ParseIP(s)
		if ip == nil {
			return false
		}
		if format == "ipv4" {
			return ip.To4() != nil
		}
		return ip.To4() == nil
	default:
		return true
	}
}
