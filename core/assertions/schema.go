package assertions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/antchfx/xmlquery"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/internal/jsonnum"
	"github.com/pgodlews/verity-api/core/spi"
)

// parseJSON decodes body into a generic value for JSONPath/schema use. Numbers
// that float64 would change stay json.Number so they compare exactly.
func parseJSON(body []byte) (any, error) {
	return jsonnum.Decode(body)
}

// jsonSchemaAssertion validates the JSON body against a JSON Schema. The schema
// is supplied via spec.Config["schema"] (inline object or string) or
// spec.Config["schemaFile"] (path relative to the project dir, passed in
// spec.Config["__dir"] by the runner) or spec.Expr (file path).
type jsonSchemaAssertion struct{}

func (jsonSchemaAssertion) Type() string { return "json_schema" }
func (a jsonSchemaAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	compiler := jsonschema.NewCompiler()
	var schemaURL string

	switch {
	case spec.Config["schema"] != nil:
		// Inline schema (object or JSON string).
		raw, err := toSchemaBytes(spec.Config["schema"])
		if err != nil {
			return result(a.Type(), spec.Description, false, "invalid inline schema: "+err.Error(), nil, nil), nil
		}
		schemaURL = "inline://schema.json"
		if err := compiler.AddResource(schemaURL, strings.NewReader(string(raw))); err != nil {
			return result(a.Type(), spec.Description, false, "add schema: "+err.Error(), nil, nil), nil
		}
	default:
		path := spec.Expr
		if path == "" {
			if s, ok := spec.Config["schemaFile"].(string); ok {
				path = s
			}
		}
		if path == "" {
			return result(a.Type(), spec.Description, false,
				"json_schema requires 'schema' config or a schema file path in 'expr'", nil, nil), nil
		}
		dir, _ := spec.Config["__dir"].(string)
		resolved, err := fsutil.ResolveInside(dir, path, fsutil.AllowExternal(spec.Config), "schema file")
		if err != nil {
			return result(a.Type(), spec.Description, false, err.Error(), nil, nil), nil
		}
		path = resolved
		raw, err := os.ReadFile(path)
		if err != nil {
			return result(a.Type(), spec.Description, false, "read schema file: "+err.Error(), nil, nil), nil
		}
		schemaURL = "file://" + path
		if err := compiler.AddResource(schemaURL, strings.NewReader(string(raw))); err != nil {
			return result(a.Type(), spec.Description, false, "add schema: "+err.Error(), nil, nil), nil
		}
	}

	sch, err := compiler.Compile(schemaURL)
	if err != nil {
		return result(a.Type(), spec.Description, false, "compile schema: "+err.Error(), nil, nil), nil
	}
	doc, err := parseJSON(actx.Response.Body)
	if err != nil {
		return result(a.Type(), spec.Description, false, "response is not valid JSON: "+err.Error(), nil, nil), nil
	}
	if err := sch.Validate(doc); err != nil {
		return result(a.Type(), spec.Description, false,
			actx.Redactor.Redact("schema validation failed: "+err.Error()), "valid against schema", "invalid"), nil
	}
	return result(a.Type(), spec.Description, true, "response is valid against JSON Schema", "valid", "valid"), nil
}

func toSchemaBytes(v any) ([]byte, error) {
	switch t := v.(type) {
	case string:
		return []byte(t), nil
	default:
		return json.Marshal(t)
	}
}

// xsdAssertion checks that the response is well-formed XML and, when
// config.rootElement is set, that the document element has that name. It
// does not read or apply an XSD: model.Validate rejects expr and
// config.schemaFile on an xsd assertion so a schema is never silently ignored.
//
// NOTE: Full W3C XSD validation requires a CGO libxml2 binding, which conflicts
// with the no-JVM/lean, pure-Go cross-compilation goal. Full XSD validation is
// documented as a plugin extension point (see docs/extending-assertions.md).
type xsdAssertion struct{}

func (xsdAssertion) Type() string { return "xsd" }
func (a xsdAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	doc, err := xmlquery.Parse(strings.NewReader(string(actx.Response.Body)))
	if err != nil {
		return result(a.Type(), spec.Description, false,
			actx.Redactor.Redact("response is not well-formed XML: "+err.Error()), "well-formed XML", "malformed"), nil
	}
	// Optional structural check: expected root element name.
	if root, ok := spec.Config["rootElement"].(string); ok && root != "" {
		node := doc
		var found *xmlquery.Node
		for n := node.FirstChild; n != nil; n = n.NextSibling {
			if n.Type == xmlquery.ElementNode {
				found = n
				break
			}
		}
		if found == nil || found.Data != root {
			got := "<none>"
			if found != nil {
				got = found.Data
			}
			return result(a.Type(), spec.Description, false,
				fmt.Sprintf("expected root element %q, got %q", root, got), root, got), nil
		}
	}
	return result(a.Type(), spec.Description, true, "response is well-formed XML", "well-formed", "well-formed"), nil
}
