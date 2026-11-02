// Package bootstrap wires Verity's first-party extensions into a registry.
// Both the CLI and the GUI call Default (or Registry) to obtain a registry
// preloaded with the built-in protocols, assertions, reporters, auth providers,
// and data sources — all registered through the public SPI, with no special
// privileges over third-party plugins.
//
// Keeping registration here (rather than in package init blocks) means the base
// binary controls exactly what is loaded, supporting the "lean base, opt-in
// plugins" goal: external plugins are layered on top via the plugin loader.
package bootstrap

import (
	"github.com/pgodlews/verity-api/core/assertions"
	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/datasource"
	"github.com/pgodlews/verity-api/core/protocols"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/reporters"
)

// Default returns a registry populated with all first-party extensions.
func Default() *registry.Registry {
	r := registry.New()
	Register(r)
	return r
}

// Register installs all first-party extensions into an existing registry.
func Register(r *registry.Registry) {
	// Protocols: REST, SOAP, and GraphQL are full implementations. gRPC is a
	// stub until plugins/grpc is loaded and replaces it.
	protocols.RegisterREST(r)
	protocols.RegisterSOAP(r)
	protocols.RegisterGraphQL(r)
	protocols.RegisterGRPC(r)

	// Assertions, auth providers, reporters, data sources.
	assertions.Register(r)
	auth.Register(r)
	reporters.Register(r)
	datasource.Register(r)
}
