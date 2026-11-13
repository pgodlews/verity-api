// Package importers generates Verity project scaffolding from API contracts
// and from other tools' project files: OpenAPI (REST), WSDL (SOAP), Postman
// collections, and SoapUI projects. Importers produce a model.Project (or
// fragments) with request templates and starter assertions, which the user then
// refines. They intentionally depend only on the standard library plus the
// project's existing XML/JSON tooling to keep the binary lean and
// cross-compilable.
package importers

import (
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/verr"
)

// Import runs the importer for kind: openapi (alias rest), wsdl (alias soap),
// postman, or soapui. envs are Postman environment files; other kinds take
// none. An unknown kind is an import_error; IsKind checks one up front.
func Import(kind, spec string, envs ...string) (*Result, error) {
	switch kind {
	case "openapi", "rest":
		return ImportOpenAPI(spec)
	case "wsdl", "soap":
		return ImportWSDL(spec)
	case "postman":
		return ImportPostman(spec, envs...)
	case "soapui":
		return ImportSoapUI(spec)
	}
	return nil, verr.Newf(verr.CodeImport, "unknown import type %q (want openapi|wsdl|postman|soapui)", kind)
}

// IsKind reports whether Import accepts kind.
func IsKind(kind string) bool {
	switch kind {
	case "openapi", "rest", "wsdl", "soap", "postman", "soapui":
		return true
	}
	return false
}

// Result is the output of an import: a generated project plus any non-fatal
// warnings (e.g. unsupported constructs that were skipped).
type Result struct {
	Project  *model.Project
	Warnings []string
}
