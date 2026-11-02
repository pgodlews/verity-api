package mock

import (
	"math"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSampleNumericBoundsAndFallbacks(t *testing.T) {
	g := newGen(map[string]any{}, 1)
	if got := g.number(map[string]any{}); got != 0 {
		t.Fatalf("unbounded number = %v", got)
	}
	for _, tt := range []struct {
		name string
		m    map[string]any
		lo   float64
		hi   float64
	}{
		{name: "minimum only", m: map[string]any{"minimum": 2}, lo: 2, hi: 2},
		{name: "maximum only", m: map[string]any{"maximum": 3}, lo: 3, hi: 3},
		{name: "inverted", m: map[string]any{"minimum": 5, "maximum": 1}, lo: 1, hi: 1},
		{name: "range", m: map[string]any{"minimum": 1.5, "maximum": 2.5}, lo: 1.5, hi: 2.5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := g.number(tt.m)
			if got < tt.lo || got > tt.hi {
				t.Fatalf("number = %v, want [%v, %v]", got, tt.lo, tt.hi)
			}
		})
	}
	lo, hi, hasLo, hasHi := intBounds(map[string]any{"minimum": 1.2, "exclusiveMinimum": true, "maximum": 4.8, "exclusiveMaximum": true})
	if lo != 3 || hi != 3 || !hasLo || !hasHi {
		t.Fatalf("boolean exclusive intBounds = %d, %d, %v, %v", lo, hi, hasLo, hasHi)
	}
	lo, hi, _, _ = intBounds(map[string]any{"exclusiveMinimum": 1.2, "exclusiveMaximum": 4.8})
	if lo != 2 || hi != 4 {
		t.Fatalf("numeric exclusive intBounds = %d, %d", lo, hi)
	}
	flo, fhi, hasLo, hasHi := floatBounds(map[string]any{"minimum": 1, "exclusiveMinimum": true, "maximum": 2, "exclusiveMaximum": true})
	if flo <= 1 || fhi >= 2 || !hasLo || !hasHi {
		t.Fatalf("boolean exclusive floatBounds = %v, %v, %v, %v", flo, fhi, hasLo, hasHi)
	}
	flo, fhi, _, _ = floatBounds(map[string]any{"exclusiveMinimum": 1, "exclusiveMaximum": 2})
	if flo <= 1 || fhi >= 2 {
		t.Fatalf("numeric exclusive floatBounds = %v, %v", flo, fhi)
	}

	fallbacks := []struct {
		schema any
		want   any
	}{
		{schema: map[string]any{"nullable": true}, want: nil},
		{schema: map[string]any{"type": "array"}, want: []any{}},
		{schema: map[string]any{"type": "object"}, want: map[string]any{}},
		{schema: map[string]any{"type": "integer"}, want: 0},
		{schema: map[string]any{"type": "number"}, want: 0.0},
		{schema: map[string]any{"type": "boolean"}, want: false},
		{schema: map[string]any{"type": "null"}, want: nil},
		{schema: map[string]any{"type": "string"}, want: "sample"},
	}
	for _, tt := range fallbacks {
		if got := g.cycleFallback(tt.schema); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("cycleFallback(%#v) = %#v, want %#v", tt.schema, got, tt.want)
		}
	}
}

func TestSampleStringAndConversionHelpers(t *testing.T) {
	for _, format := range []string{"date-time", "date", "time", "email", "uuid", "uri", "url", "uri-reference", "hostname", "ipv4", "ipv6", "byte", "binary", "password"} {
		if got, ok := formatSample(format); !ok || got == "" {
			t.Errorf("formatSample(%q) = %q, %v", format, got, ok)
		}
	}
	if got, ok := formatSample("other"); ok || got != "" {
		t.Fatalf("unknown format = %q, %v", got, ok)
	}
	if got := fitLen("éclair", 0, 2); got != "éc" {
		t.Fatalf("Unicode truncation = %q", got)
	}
	if got := fitLen("x", 3, -1); got != "xaa" {
		t.Fatalf("padding = %q", got)
	}
	for _, tt := range []struct {
		schema map[string]any
		want   string
	}{
		{schema: map[string]any{"type": "null", "properties": map[string]any{}}, want: "object"},
		{schema: map[string]any{"type": []any{"null", "integer"}}, want: "integer"},
		{schema: map[string]any{"type": []any{"null"}}, want: "null"},
		{schema: map[string]any{"properties": map[string]any{}}, want: "object"},
		{schema: map[string]any{"items": map[string]any{}}, want: "array"},
		{schema: map[string]any{}, want: "string"},
	} {
		if got := schemaType(tt.schema); got != tt.want {
			t.Errorf("schemaType(%#v) = %q, want %q", tt.schema, got, tt.want)
		}
	}
	for _, value := range []any{int(1), int64(2), uint64(3), float64(4), "5"} {
		if _, ok := asInt(value); !ok {
			t.Errorf("asInt(%T) failed", value)
		}
	}
	for _, value := range []any{math.Inf(1), "bad", true} {
		if _, ok := asInt(value); ok {
			t.Errorf("asInt(%#v) succeeded", value)
		}
	}
	for _, value := range []any{int(1), int64(2), uint64(3), float64(4), float32(5), "6.5"} {
		if _, ok := asFloat(value); !ok {
			t.Errorf("asFloat(%T) failed", value)
		}
	}
	if _, ok := asFloat("bad"); ok {
		t.Fatal("asFloat accepted malformed text")
	}
	if got := enumText([]any{"a", 1, true}); got != "a, 1, true" {
		t.Fatalf("enumText = %q", got)
	}
}

func TestSchemaValidationHelpers(t *testing.T) {
	for _, tt := range []struct {
		format string
		value  string
		want   bool
	}{
		{format: "date", value: "2020-01-01", want: true},
		{format: "date", value: "01-01-2020"},
		{format: "date-time", value: "2020-01-01T00:00:00Z", want: true},
		{format: "date-time", value: "2020-01-01"},
		{format: "email", value: "user@example", want: true},
		{format: "email", value: "user @example"},
		{format: "uuid", value: "00000000-0000-4000-8000-000000000000", want: true},
		{format: "uuid", value: "not-a-uuid"},
		{format: "ipv4", value: "not-an-ip"},
		{format: "ipv6", value: "not-an-ip"},
		{format: "other", value: "anything", want: true},
	} {
		if got := formatOK(tt.format, tt.value); got != tt.want {
			t.Errorf("formatOK(%q, %q) = %v, want %v", tt.format, tt.value, got, tt.want)
		}
	}
	if got := validateAnyOf([]any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}, true, "value", nil, 0); len(got) == 0 {
		t.Fatal("anyOf accepted a value matching no alternative")
	}
	if got := validateAnyOf([]any{map[string]any{}}, "anything", "value", nil, 0); len(got) != 0 {
		t.Fatalf("anyOf empty schema = %v", got)
	}
	for _, tt := range []struct {
		m map[string]any
		n float64
	}{
		{m: map[string]any{"minimum": 1}, n: 0},
		{m: map[string]any{"minimum": 1, "exclusiveMinimum": true}, n: 1},
		{m: map[string]any{"exclusiveMinimum": 1}, n: 1},
		{m: map[string]any{"maximum": 1}, n: 2},
		{m: map[string]any{"maximum": 1, "exclusiveMaximum": true}, n: 1},
		{m: map[string]any{"exclusiveMaximum": 1}, n: 1},
	} {
		if got := numberBounds(tt.m, tt.n, "value"); len(got) == 0 {
			t.Errorf("numberBounds(%#v, %v) accepted", tt.m, tt.n)
		}
	}
}

func TestOpenAPIResponseAndBodySelection(t *testing.T) {
	bodies := []oasBody{{contentType: "application/json", schema: "json"}, {contentType: "text/plain", schema: "text"}}
	for _, tt := range []struct {
		name string
		ct   string
		raw  string
		want any
		ok   bool
	}{
		{name: "explicit JSON", ct: "application/json; charset=utf-8", want: "json", ok: true},
		{name: "explicit mismatch", ct: "application/xml"},
		{name: "detect JSON", raw: `{"id":1}`, want: "json", ok: true},
		{name: "ambiguous unknown", raw: "plain"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, ok := matchBody(bodies, tt.ct, []byte(tt.raw))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("matchBody = %#v, %v; want %#v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
	if got, _, ok := matchBody([]oasBody{{contentType: "text/plain", schema: "only"}}, "", []byte("text")); got != "only" || !ok {
		t.Fatalf("single body fallback = %#v, %v", got, ok)
	}
	if got := responseStatus(oasResp{class: 4}); got != 400 {
		t.Fatalf("class responseStatus = %d", got)
	}
	if got := responseStatus(oasResp{status: 404}); got != 404 {
		t.Fatalf("exact responseStatus = %d", got)
	}
	responses := []oasResp{{status: 500}, {class: 4}, {status: 400}}
	chosen, status, ok := pickResponse(responses, 0)
	if !ok || status != 400 || chosen.class != 4 {
		t.Fatalf("fallback response = %#v, %d, %v", chosen, status, ok)
	}
	if _, _, ok := pickResponse([]oasResp{{status: 201}}, 404); ok {
		t.Fatal("pickResponse found a missing preferred status")
	}
}

func TestOpenAPIEncodingAndYAMLHelpers(t *testing.T) {
	if got := string(encodeBody("application/json", ` {"id":1} `)); got != `{"id":1}` {
		t.Fatalf("raw JSON = %q", got)
	}
	if got := string(encodeBody("application/json", map[string]any{"id": 1})); got != `{"id":1}` {
		t.Fatalf("encoded JSON = %q", got)
	}
	if got := string(encodeBody("application/json", func() {})); got != "null" {
		t.Fatalf("unsupported JSON = %q", got)
	}
	if got := string(encodeBody("text/plain", "value")); got != "value" {
		t.Fatalf("plain body = %q", got)
	}
	if got := encodeBody("text/plain", nil); got != nil {
		t.Fatalf("nil body = %#v", got)
	}
	if got := string(encodeBody("application/octet-stream", map[string]any{"id": 1})); got != `{"id":1}` {
		t.Fatalf("binary fallback = %q", got)
	}
	if got := encodeBody("application/octet-stream", func() {}); got != nil {
		t.Fatalf("unsupported binary body = %#v", got)
	}
	for _, method := range []string{"get", "POST", "put", "patch", "delete", "head", "options", "trace"} {
		if !isHTTPMethod(method) {
			t.Errorf("isHTTPMethod(%q) = false", method)
		}
	}
	if isHTTPMethod("connect") {
		t.Fatal("CONNECT unexpectedly accepted")
	}
	if got := mediaTypeOf("application/json; charset=utf-8"); got != "application/json" {
		t.Fatalf("mediaTypeOf = %q", got)
	}
	if got := mediaTypeOf(" application/json ; broken"); got != "application/json" {
		t.Fatalf("malformed mediaTypeOf = %q", got)
	}
	if got := mediaTypeOf(""); got != "" {
		t.Fatalf("empty mediaTypeOf = %q", got)
	}

	var root yaml.Node
	if err := yaml.Unmarshal([]byte("components:\n  schemas:\n    a/b:\n      type: string\n    list:\n      - name: first\n"), &root); err != nil {
		t.Fatal(err)
	}
	yamlRoot := docNode(&root)
	if got := nodeString(yamlPointer(yamlRoot, "#/components/schemas/a~1b/type")); got != "string" {
		t.Fatalf("escaped YAML pointer = %q", got)
	}
	if got := nodeString(yamlPointer(yamlRoot, "#/components/schemas/list/0/name")); got != "first" {
		t.Fatalf("sequence YAML pointer = %q", got)
	}
	for _, ref := range []string{"other", "#/components/schemas/list/2", "#/components/schemas/list/bad", "#/components/schemas/list/0/name/child"} {
		if got := yamlPointer(yamlRoot, ref); got != nil {
			t.Errorf("yamlPointer(%q) = %#v, want nil", ref, got)
		}
	}
	if got := yamlPointer(nil, "#/anything"); got != nil {
		t.Fatalf("nil YAML pointer = %#v", got)
	}
}

func TestWSDLScalarAndElementHelpers(t *testing.T) {
	d := &wsdlDoc{}
	for _, typ := range []string{"int", "integer", "long", "short", "byte", "unsignedInt", "unsignedLong", "unsignedShort", "unsignedByte", "nonNegativeInteger", "nonPositiveInteger", "negativeInteger", "float", "double", "decimal"} {
		if got := d.scalar(typ, 7); got != "7" {
			t.Errorf("scalar(%q) = %q, want 7", typ, got)
		}
	}
	for _, tt := range []struct {
		typ  string
		seed int64
		want string
	}{
		{typ: "positiveInteger", want: "1"},
		{typ: "positiveInteger", seed: 3, want: "3"},
		{typ: "boolean", seed: 1, want: "true"},
		{typ: "boolean", want: "false"},
		{typ: "date", want: "2020-01-01"},
		{typ: "dateTime", want: "2020-01-01T00:00:00Z"},
		{typ: "time", want: "00:00:00Z"},
		{typ: "string", want: "sample"},
		{typ: "", want: "sample"},
		{typ: "int", seed: -2, want: "0"},
	} {
		if got := d.scalar(tt.typ, tt.seed); got != tt.want {
			t.Errorf("scalar(%q, %d) = %q, want %q", tt.typ, tt.seed, got, tt.want)
		}
	}
	simple := xsdSimple{Restriction: &xsdRestriction{Base: "xsd:int", Enums: []xsdFacet{{Value: "first"}, {Value: "second"}}}}
	if got := d.simpleText(simple, "string", 3); got != "second" {
		t.Fatalf("enum simpleText = %q", got)
	}
	if got := d.simpleText(simple, "string", -1); got != "first" {
		t.Fatalf("negative enum simpleText = %q", got)
	}
	simple.Restriction.Enums = nil
	if got := d.simpleText(simple, "string", 4); got != "4" {
		t.Fatalf("restriction base simpleText = %q", got)
	}
	if got := d.simpleText(xsdSimple{}, "xsd:boolean", 1); got != "true" {
		t.Fatalf("fallback simpleText = %q", got)
	}

	d.elem = map[string]xsdElement{"ns item": {Name: "item", Type: "xsd:string"}}
	d.elemName = map[string]xsdElement{"fallback": {Name: "fallback"}}
	if _, ok := d.lookupElem("ns", "item"); !ok {
		t.Fatal("namespaced element not found")
	}
	if _, ok := d.lookupElem("other", "fallback"); !ok {
		t.Fatal("fallback element not found")
	}
	if got := d.innerXML(xsdElement{Ref: "missing"}, 0, 0); got != "" {
		t.Fatalf("missing ref = %q", got)
	}
	if got := d.innerXML(xsdElement{Name: "item"}, 0, 9); got != "" {
		t.Fatalf("depth limit = %q", got)
	}
	if got := d.innerXML(xsdElement{}, 0, 0); got != "sample" {
		t.Fatalf("default innerXML = %q", got)
	}
}
