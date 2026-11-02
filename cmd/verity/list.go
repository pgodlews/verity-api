package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pgodlews/verity-api/core/bootstrap"
)

func cmdList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit structured JSON catalog")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cat := bootstrap.Default().Catalog()
	if *jsonOut {
		emitJSON(cat)
		return exitOK
	}
	fmt.Println("Registered extensions:")
	fmt.Printf("  protocols:    %s\n", strings.Join(cat.Protocols, ", "))
	fmt.Printf("  assertions:   %s\n", strings.Join(cat.Assertions, ", "))
	fmt.Printf("  reporters:    %s\n", strings.Join(cat.Reporters, ", "))
	fmt.Printf("  auth:         %s\n", strings.Join(cat.Auth, ", "))
	fmt.Printf("  data sources: %s\n", strings.Join(cat.DataSources, ", "))
	return exitOK
}
