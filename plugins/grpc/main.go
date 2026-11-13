// Command verity-grpc is Verity's first-party gRPC protocol plugin.
// It speaks VPP/1 and links grpc-go so the core module does not.
//
// Build:
//
//	go build -o verity-grpc .
//
// Register it from a project (see examples/grpc):
//
//	{ "plugins": [ { "name": "grpc", "path": "/path/to/plugins/grpc/verity-grpc" } ] }
//
// The plugin registers as protocol "grpc", which replaces the core stub for
// that run. Unary and all three streaming kinds are supported.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/pgodlews/verity-api/core/plugin/sdk"
)

// Build metadata, set by scripts/release.sh with -ldflags -X.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		printVersion(os.Stdout)
		return
	}
	sdk.ServeProtocol(grpcProtocol{})
}

// printVersion writes the build metadata. The host never passes --version;
// it is for people checking which plugin binary they installed.
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "verity-grpc %s (commit %s, built %s)\n", version, commit, date)
}
