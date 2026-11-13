package assertions

import (
	"encoding/json"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

// eq/ne must not coerce two strings to floats, and numbers must compare
// exactly rather than through float64.
func TestCompareExactAndStringSemantics(t *testing.T) {
	for _, tc := range []struct {
		op       string
		actual   any
		expected any
		want     bool
	}{
		// Two strings compare as text.
		{"eq", "1.10", "1.1", false},
		{"eq", "007", "7", false},
		{"eq", "1e3", "1000", false},
		{"ne", "1.10", "1.1", true},
		{"eq", "1.1", "1.1", true},
		// Large integers are not rounded.
		{"eq", json.Number("9007199254740993"), json.Number("9007199254740992"), false},
		{"ne", json.Number("9007199254740993"), int64(9007199254740992), true},
		{"eq", json.Number("9007199254740993"), "9007199254740993", true},
		{"gt", json.Number("9007199254740993"), int64(9007199254740992), true},
		{"lt", "9007199254740992", json.Number("9007199254740993"), true},
		// One real number compares numerically with a numeric string.
		{"eq", 200, "200", true},
		{"eq", float64(7), "007", true},
		{"eq", float64(0.1), json.Number("0.1"), true},
		{"eq", float64(1000), json.Number("1e3"), true},
		{"eq", 3, "three", false},
		// Ordering ops still parse numeric strings.
		{"lt", "1.5", "2", true},
		{"gte", "abc", 1, false},
	} {
		if got, msg := compare(tc.op, tc.actual, tc.expected); got != tc.want {
			t.Errorf("%s %#v %#v = %v (%s), want %v", tc.op, tc.actual, tc.expected, got, msg, tc.want)
		}
	}
}

func TestJSONPathKeepsLargeIntegerPrecision(t *testing.T) {
	body := `{"id":9007199254740993,"price":1.1,"items":[{"id":1},{"id":2}]}`
	cases := []struct {
		spec spi.AssertionSpec
		want bool
	}{
		{spi.AssertionSpec{Expr: "$.id", Value: json.Number("9007199254740992")}, false},
		{spi.AssertionSpec{Expr: "$.id", Value: json.Number("9007199254740993")}, true},
		{spi.AssertionSpec{Expr: "$.id", Op: "ne", Value: json.Number("9007199254740992")}, true},
		{spi.AssertionSpec{Expr: "$.price", Value: 1.1}, true},
		// Filters still compare float64 numbers.
		{spi.AssertionSpec{Expr: "$.items[?(@.id == 2)].id", Op: "exists"}, true},
	}
	for _, tc := range cases {
		got := eval(t, jsonPathAssertion{}, jsonResp(body), tc.spec)
		if got.Passed != tc.want {
			t.Errorf("%s %s %v = %v (%s), want %v", tc.spec.Expr, tc.spec.Op, tc.spec.Value, got.Passed, got.Message, tc.want)
		}
	}
}
