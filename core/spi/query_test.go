package spi

import "testing"

func TestEscapeQuery(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "space and unicode", raw: "a=x y&b=ü", want: "a=x%20y&b=%C3%BC"},
		{name: "controls", raw: "a=\x00\x01\t\n\x1f\x7f", want: "a=%00%01%09%0A%1F%7F"},
		{name: "unsafe ASCII", raw: `a="<>\^` + "`" + `{|}`, want: "a=%22%3C%3E%5C%5E%60%7B%7C%7D"},
		{name: "separators", raw: "a+b=c&d=e;f=g", want: "a+b=c&d=e;f=g"},
		{name: "percent escapes", raw: "lower=%2f&upper=%2F&invalid=%zz&bare=%", want: "lower=%2f&upper=%2F&invalid=%zz&bare=%"},
		{name: "other ASCII", raw: "a=[]:!@$'()*,", want: "a=[]:!@$'()*,"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EscapeQuery(tc.raw); got != tc.want {
				t.Fatalf("EscapeQuery(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeTargetQueryBeforeParse(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"http://host/p?q=a\tb", "http://host/p?q=a%09b"},
		{"http://host/p?q=a\nb#fragment\t", "http://host/p?q=a%0Ab#fragment\t"},
		{"http://host/p#fragment?q=a\tb", "http://host/p#fragment?q=a\tb"},
	} {
		if got := NormalizeTargetQuery(tc.raw); got != tc.want {
			t.Fatalf("NormalizeTargetQuery(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
