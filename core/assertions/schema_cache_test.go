package assertions

import "testing"

func TestCompiledSchemaIsCachedByContent(t *testing.T) {
	a, _, err := compiledSchema("inline://t.json", []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := compiledSchema("inline://t.json", []byte(`{"type":"object"}`))
	if a != b {
		t.Fatal("same schema compiled twice")
	}
	c, _, _ := compiledSchema("inline://t.json", []byte(`{"type":"array"}`))
	if c == a {
		t.Fatal("changed schema served from the cache")
	}
	if _, stage, err := compiledSchema("inline://bad.json", []byte(`{"type":12}`)); err == nil || stage != "compile schema" {
		t.Fatalf("bad schema: stage %q err %v", stage, err)
	}
}
