// Package jsonq evaluates JSONPath expressions for assertions and property
// transfers with one definition of "not found".
package jsonq

import (
	"context"
	"fmt"
	"strings"

	"github.com/PaesslerAG/jsonpath"
)

// Normalize adds the "$." root to a bare path such as "items[0].id".
func Normalize(expr string) string {
	if expr != "" && !strings.HasPrefix(expr, "$") {
		return "$." + expr
	}
	return expr
}

// Get evaluates expr against a decoded JSON document.
//
// An expression that does not compile is an error. An expression that
// compiles but cannot be followed through this document (a missing key, an
// out-of-range index, indexing into a missing or non-container value) is
// "not found": found is false and err is nil. This is decided by where the
// failure happens (compile vs. evaluate), not by matching the library's
// error text, so it does not change with library wording.
func Get(doc any, expr string) (value any, found bool, err error) {
	expr = Normalize(expr)
	eval, err := jsonpath.New(expr)
	if err != nil {
		return nil, false, fmt.Errorf("jsonpath %q: %w", expr, err)
	}
	v, err := eval(context.Background(), doc)
	if err != nil {
		return nil, false, nil
	}
	return v, true, nil
}
