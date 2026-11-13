package assertions

import (
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

// A path that does not exist (missing key, out-of-range index, index into a
// missing parent) is "not found": not_exists passes and exists fails.
func TestJSONPathNotFoundCases(t *testing.T) {
	body := `{"items":[{"id":1},{"id":2}],"obj":{"a":1}}`
	for _, expr := range []string{"$.missing", "$.items[5]", "$.items[5].id", "$.missing[0]", "$.obj.b.c", "$.items[-9]"} {
		got := eval(t, jsonPathAssertion{}, jsonResp(body), spi.AssertionSpec{Expr: expr, Op: "not_exists"})
		if !got.Passed {
			t.Errorf("not_exists %s failed: %s", expr, got.Message)
		}
		got = eval(t, jsonPathAssertion{}, jsonResp(body), spi.AssertionSpec{Expr: expr, Op: "exists"})
		if got.Passed {
			t.Errorf("exists %s passed", expr)
		}
	}
	// A syntax error is still an error, not "absent".
	got := eval(t, jsonPathAssertion{}, jsonResp(body), spi.AssertionSpec{Expr: "$.items[", Op: "not_exists"})
	if got.Passed {
		t.Error("invalid JSONPath passed not_exists")
	}
}
