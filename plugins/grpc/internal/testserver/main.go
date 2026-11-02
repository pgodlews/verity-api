// Command testserver serves the echo fixture on a unix socket.
// It is a test double for the host integration test, not a product binary.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/pgodlews/verity-api/plugins/grpc/internal/echo"
)

func main() {
	listen := flag.String("listen", "", "unix socket path")
	desc := flag.String("descriptor-out", "", "write a FileDescriptorSet to this path")
	flag.Parse()
	if *desc != "" {
		if err := os.WriteFile(*desc, echo.DescriptorSet(), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if *listen == "" {
		if *desc == "" {
			fmt.Fprintln(os.Stderr, "-listen is required")
			os.Exit(2)
		}
		return
	}
	if err := os.Remove(*listen); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lis, err := net.Listen("unix", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv := echo.NewServer(echo.ReflectionBoth)
	fmt.Println("ready")
	if err := srv.Serve(lis); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
