// Command grpc is Verity's first-party gRPC protocol plugin.
// It speaks VPP/1 and links grpc-go so the core module does not.
//
// Build:
//
//	go build -o grpc .
//
// Register it from a project (see examples/grpc):
//
//	{ "plugins": [ { "name": "grpc", "path": "/path/to/plugins/grpc/grpc" } ] }
//
// The plugin registers as protocol "grpc", which replaces the core stub for
// that run. Unary and all three streaming kinds are supported.
package main

import "github.com/pgodlews/verity-api/core/plugin/sdk"

func main() {
	sdk.ServeProtocol(grpcProtocol{})
}
