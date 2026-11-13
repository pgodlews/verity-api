package importers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestImportersRefuseOversizedInput(t *testing.T) {
	old := importMaxBytes
	importMaxBytes = 64
	t.Cleanup(func() { importMaxBytes = old })
	path := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 65)), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"openapi": func() error { _, err := ImportOpenAPI(path); return err },
		"wsdl":    func() error { _, err := ImportWSDL(path); return err },
		"postman": func() error { _, err := ImportPostman(path); return err },
	} {
		err := run()
		got, ok := verr.As(err)
		if !ok || got.Code != verr.CodeImport || !strings.Contains(got.Message, "limit") {
			t.Errorf("%s: error = %v, want an import_error about the size limit", name, err)
		}
	}
}
