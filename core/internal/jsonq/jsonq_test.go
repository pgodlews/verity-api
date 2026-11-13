package jsonq

import "testing"

func TestGetFoundNotFoundAndErrors(t *testing.T) {
	doc := map[string]any{"items": []any{map[string]any{"id": 1.0}}, "s": "x"}
	if v, ok, err := Get(doc, "items[0].id"); err != nil || !ok || v != 1.0 {
		t.Fatalf("bare path = %v %v %v", v, ok, err)
	}
	for _, expr := range []string{"$.missing", "$.items[3]", "$.items[3].id", "$.s.deeper", "$.s[0]"} {
		if _, ok, err := Get(doc, expr); ok || err != nil {
			t.Errorf("%s = found %v, err %v; want not found", expr, ok, err)
		}
	}
	if _, _, err := Get(doc, "$.items["); err == nil {
		t.Fatal("syntax error not reported")
	}
}
