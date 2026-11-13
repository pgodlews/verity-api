package datasource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJSONSourceNullAndNestedValues(t *testing.T) {
	dir := t.TempDir()
	body := `[{"id":null,"tags":["a","<b>"],"meta":{"k":1},"ok":true}]`
	if err := os.WriteFile(filepath.Join(dir, "d.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := jsonSource{}.Rows(dir, nil, nil, "d.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"id": "", "tags": `["a","<b>"]`, "meta": `{"k":1}`, "ok": "true"}
	for k, v := range want {
		if rows[0][k] != v {
			t.Errorf("%s = %q, want %q", k, rows[0][k], v)
		}
	}
}
