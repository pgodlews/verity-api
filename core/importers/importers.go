// Package importers generates Verity project scaffolding from API contracts
// and from other tools' project files: OpenAPI (REST), WSDL (SOAP), Postman
// collections, and SoapUI projects. Importers produce a model.Project (or
// fragments) with request templates and starter assertions, which the user then
// refines. They intentionally depend only on the standard library plus the
// project's existing XML/JSON tooling to keep the binary lean and
// cross-compilable.
package importers

import "github.com/pgodlews/verity-api/core/model"

// Result is the output of an import: a generated project plus any non-fatal
// warnings (e.g. unsupported constructs that were skipped).
type Result struct {
	Project  *model.Project
	Warnings []string
}
