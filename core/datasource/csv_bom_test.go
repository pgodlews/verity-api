package datasource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestCSVStripsBOMAndRejectsDuplicateHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bom.csv"), []byte("\xef\xbb\xbfid,name\n1,a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := csvSource{}.Rows(dir, nil, nil, "bom.csv")
	if err != nil || len(rows) != 1 || rows[0]["id"] != "1" {
		t.Fatalf("BOM rows = %#v, %v", rows, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dup.csv"), []byte("id,id\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = csvSource{}.Rows(dir, nil, nil, "dup.csv")
	if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
		t.Fatalf("duplicate header error = %v", err)
	}
}
