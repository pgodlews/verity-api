package importers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

// One dispatch serves the CLI and the desktop import, aliases included.
func TestImportDispatch(t *testing.T) {
	for _, k := range []string{"openapi", "rest", "wsdl", "soap", "postman", "soapui"} {
		if !IsKind(k) {
			t.Fatalf("IsKind(%q) = false", k)
		}
	}
	if IsKind("raml") || IsKind("") {
		t.Fatal("unknown kind accepted")
	}
	_, err := Import("raml", "spec.raml")
	if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeImport {
		t.Fatalf("unknown kind error = %v", err)
	}

	spec := filepath.Join(t.TempDir(), "api.yaml")
	doc := "openapi: 3.0.0\ninfo: {title: t, version: '1'}\npaths:\n  /ping:\n    get:\n      responses: {'200': {description: ok}}\n"
	if err := os.WriteFile(spec, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"openapi", "rest"} {
		res, err := Import(k, spec)
		if err != nil || res.Project == nil {
			t.Fatalf("Import(%q) = %v, %v", k, res, err)
		}
	}
}
