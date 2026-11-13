package scalar

import (
	"encoding/json"
	"testing"
)

func TestString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"x", "x"},
		{json.Number("9007199254740993"), "9007199254740993"},
		{true, "true"},
		{3, "3"},
		{int64(-7), "-7"},
		{uint8(9), "9"},
		{1.5, "1.5"},
		{1e6, "1000000"},
		{map[string]any{"a": nil, "b": "<x>"}, `{"a":null,"b":"<x>"}`},
		{[]any{1, "two", nil}, `[1,"two",null]`},
	}
	for _, tc := range cases {
		if got := String(tc.in); got != tc.want {
			t.Errorf("String(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
