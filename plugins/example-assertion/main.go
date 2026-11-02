// Command example-assertion is a complete, runnable Verity plugin demonstrating
// the out-of-process plugin mechanism (Verity Plugin Protocol v1). It
// implements a custom assertion, "json_array_len", that checks the length of a
// JSON array selected by a JSONPath-ish key.
//
// Build:
//
//	go build -o example-assertion .
//
// Register (in .verity/plugins.json):
//
//	{ "plugins": [ { "name": "example-assertion",
//	                 "path": "./plugins/example-assertion/example-assertion" } ] }
//
// Then use it in a project step:
//
//	assertions:
//	  - type: json_array_len
//	    expr: tags     # top-level key holding an array
//	    op: eq
//	    value: 2
//
// This file is intentionally small and heavily commented to serve as the
// canonical copy-paste starting point referenced by docs/extending-plugins.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pgodlews/verity-api/core/plugin/sdk"
	"github.com/pgodlews/verity-api/core/spi"
)

// arrayLenAssertion checks that response.body[expr] is a JSON array whose length
// compares (op) to value.
type arrayLenAssertion struct{}

func (arrayLenAssertion) Type() string { return "json_array_len" }

func (a arrayLenAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	var doc map[string]any
	if err := json.Unmarshal(actx.Response.Body, &doc); err != nil {
		return &spi.AssertionResult{
			Passed: false, Type: a.Type(), Description: spec.Description,
			Message: "response body is not a JSON object: " + err.Error(),
		}, nil
	}
	raw, ok := doc[spec.Expr]
	if !ok {
		return &spi.AssertionResult{
			Passed: false, Type: a.Type(), Description: spec.Description,
			Message: fmt.Sprintf("key %q not found in response", spec.Expr),
		}, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return &spi.AssertionResult{
			Passed: false, Type: a.Type(), Description: spec.Description,
			Message: fmt.Sprintf("key %q is not an array", spec.Expr),
		}, nil
	}
	got := len(arr)
	want := toInt(spec.Value)
	op := spec.Op
	if op == "" {
		op = "eq"
	}
	passed := false
	switch op {
	case "eq":
		passed = got == want
	case "ne":
		passed = got != want
	case "lt":
		passed = got < want
	case "lte":
		passed = got <= want
	case "gt":
		passed = got > want
	case "gte":
		passed = got >= want
	}
	return &spi.AssertionResult{
		Passed: passed, Type: a.Type(), Description: spec.Description,
		Message:  fmt.Sprintf("array %q length %d %s %d", spec.Expr, got, op, want),
		Expected: want, Actual: got,
	}, nil
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		return toInt(t.String())
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	default:
		return 0
	}
}

func main() {
	// One line wires the assertion into the VPP handshake + RPC loop.
	sdk.ServeAssertion(arrayLenAssertion{})
}
