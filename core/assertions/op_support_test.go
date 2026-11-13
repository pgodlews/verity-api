package assertions

import (
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestContainsAndRegexHonourOp(t *testing.T) {
	body := `{"status":"ok"}`
	cases := []struct {
		a    spi.Assertion
		spec spi.AssertionSpec
		want bool
	}{
		{containsAssertion{}, spi.AssertionSpec{Value: "ok"}, true},
		{containsAssertion{}, spi.AssertionSpec{Value: "ok", Op: "not_contains"}, false},
		{containsAssertion{}, spi.AssertionSpec{Value: "error", Op: "not_contains"}, true},
		{containsAssertion{}, spi.AssertionSpec{Value: "error", Op: "not_exists"}, true}, // deprecated alias
		{containsAssertion{}, spi.AssertionSpec{Value: "ok", Op: "ne"}, false},           // unsupported: fails, never a silent positive match
		{regexAssertion{}, spi.AssertionSpec{Expr: `"ok"`}, true},
		{regexAssertion{}, spi.AssertionSpec{Expr: `"ok"`, Op: "not_matches"}, false},
		{regexAssertion{}, spi.AssertionSpec{Expr: `error`, Op: "not_matches"}, true},
		{regexAssertion{}, spi.AssertionSpec{Expr: `"ok"`, Op: "ne"}, false},
	}
	for _, tc := range cases {
		got := eval(t, tc.a, jsonResp(body), tc.spec)
		if got.Passed != tc.want {
			t.Errorf("%s op=%q %v%s: passed=%v (%s), want %v", tc.a.Type(), tc.spec.Op, tc.spec.Value, tc.spec.Expr, got.Passed, got.Message, tc.want)
		}
	}
}
