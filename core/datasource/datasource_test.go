package datasource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCSVSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(path, []byte("name,qty\nwidget,3\ngadget,5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := csvSource{}.Rows(dir, nil, nil, "data.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0]["name"] != "widget" || rows[1]["qty"] != "5" {
		t.Fatalf("unexpected rows: %v", rows)
	}
}

func TestJSONSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(`[{"id":1,"name":"a"},{"id":2,"name":"b"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := jsonSource{}.Rows(dir, nil, nil, "data.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["name"] != "a" || rows[1]["id"] != "2" {
		t.Fatalf("unexpected rows: %v", rows)
	}
}

func TestInlineRows(t *testing.T) {
	inline := []map[string]string{{"x": "1"}, {"x": "2"}}
	rows, err := csvSource{}.Rows("", nil, inline, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected inline rows passthrough, got %d", len(rows))
	}
}

func TestJSONSourceLargeNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.json")
	if err := os.WriteFile(path, []byte(`[{"id":10000000,"price":19.99}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := jsonSource{}.Rows(dir, nil, nil, "large.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["id"] != "10000000" {
		t.Fatalf("expected id to be preserved as '10000000', got %q", rows[0]["id"])
	}
	if rows[0]["price"] != "19.99" {
		t.Fatalf("expected price to be '19.99', got %q", rows[0]["price"])
	}
}
