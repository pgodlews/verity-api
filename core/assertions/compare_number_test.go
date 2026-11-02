package assertions

import (
	"encoding/json"
	"testing"
)

// JSON projects decode numbers with UseNumber, so expected values reach the
// comparators as json.Number and must still compare numerically.
func TestCompareJSONNumber(t *testing.T) {
	for _, tc := range []struct {
		op       string
		actual   any
		expected any
		want     bool
	}{
		{"gt", float64(42), json.Number("41.5"), true},
		{"lt", float64(1), json.Number("5000"), true},
		{"gte", json.Number("42"), json.Number("42"), true},
		{"lte", float64(7), json.Number("6"), false},
		{"eq", float64(42), json.Number("42"), true},
	} {
		if got, msg := compare(tc.op, tc.actual, tc.expected); got != tc.want {
			t.Fatalf("%s %v %v: got %v (%s)", tc.op, tc.actual, tc.expected, got, msg)
		}
	}
}
