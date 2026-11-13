package assertions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

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
	var schemaURL string
	var schemaRaw []byte

	switch {
	case spec.Config["schema"] != nil:
		// Inline schema (object or JSON string).
		raw, err := toSchemaBytes(spec.Config["schema"])
		if err != nil {
			return result(a.Type(), spec.Description, false, "invalid inline schema: "+err.Error(), nil, nil), nil
		}
		schemaURL = "inline://schema.json"
		schemaRaw = raw
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
		schemaRaw = raw
	}

	sch, stage, err := compiledSchema(schemaURL, schemaRaw)
	if err != nil {
		return result(a.Type(), spec.Description, false, stage+": "+err.Error(), nil, nil), nil
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

// schemaCache holds compiled JSON Schemas keyed by URL and content hash, so a
// schema is compiled once per run rather than on every evaluation (once per
// request under load). An edited schema file has a new hash.
var schemaCache = struct {
	sync.Mutex
	items map[[32]byte]*jsonschema.Schema
}{items: map[[32]byte]*jsonschema.Schema{}}

// maxCachedSchemas bounds the cache; it is cleared when full.
const maxCachedSchemas = 256

// compiledSchema compiles raw at url, or returns the cached compilation. The
// stage names the failing step ("add schema" or "compile schema").
func compiledSchema(url string, raw []byte) (*jsonschema.Schema, string, error) {
	h := sha256.New()
	h.Write([]byte(url))
	h.Write([]byte{0})
	h.Write(raw)
	var key [32]byte
	copy(key[:], h.Sum(nil))
	schemaCache.Lock()
	sch, ok := schemaCache.items[key]
	schemaCache.Unlock()
	if ok {
		return sch, "", nil
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(url, strings.NewReader(string(raw))); err != nil {
		return nil, "add schema", err
	}
	sch, err := compiler.Compile(url)
	if err != nil {
		return nil, "compile schema", err
	}
	schemaCache.Lock()
	if len(schemaCache.items) >= maxCachedSchemas {
		schemaCache.items = map[[32]byte]*jsonschema.Schema{}
	}
	schemaCache.items[key] = sch
	schemaCache.Unlock()
	return sch, "", nil
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
