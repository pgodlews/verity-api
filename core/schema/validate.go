package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// projectSchemaCompiled compiles the embedded project schema once.
var projectSchemaCompiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("project.json", strings.NewReader(projectSchema)); err != nil {
		return nil, fmt.Errorf("load schema: %w", err)
	}
	sch, err := compiler.Compile("project.json")
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return sch, nil
})

// ValidateProjectBytes validates raw project bytes (JSON) against the published
// project JSON Schema. It returns a slice of human-readable problems (empty when
// valid). YAML callers should convert to JSON first via the model package.
//
// This complements model.Validate (which performs semantic checks): the schema
// check catches structural/shape errors and is the same check editors and AI
// agents perform against the published $schema.
func ValidateProjectBytes(projectJSON []byte) ([]string, error) {
	sch, err := projectSchemaCompiled()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(projectJSON))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse project JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse project JSON: multiple values")
		}
		return nil, fmt.Errorf("parse project JSON: %w", err)
	}
	if err := sch.Validate(doc); err != nil {
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			return flatten(ve), nil
		}
		return []string{err.Error()}, nil
	}
	return nil, nil
}

// flatten turns a nested ValidationError tree into a flat list of messages.
func flatten(ve *jsonschema.ValidationError) []string {
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := e.InstanceLocation
			if loc == "" {
				loc = "(root)"
			}
			out = append(out, fmt.Sprintf("%s: %s", loc, e.Message))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return out
}
