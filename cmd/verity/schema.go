package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/pgodlews/verity-api/core/schema"
)

func cmdSchema(args []string) int {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	kind := fs.String("kind", "project", "schema to print: project|auth")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	switch *kind {
	case "project":
		fmt.Println(schema.ProjectSchemaJSON())
	case "auth":
		fmt.Println(schema.AuthSchemaJSON())
	default:
		fmt.Fprintf(os.Stderr, "schema: unknown kind %q (want project|auth)\n", *kind)
		return exitUsage
	}
	return exitOK
}
