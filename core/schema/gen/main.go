//go:build ignore

// Command gen writes the published JSON Schemas to standalone .json files in the
// schema package directory, sourced from the Go raw-string constants. Run it
// after changing the schemas:
//
//	go run ./core/schema/gen
//
// This keeps a single source of truth (the Go constants used by the CLI/GUI)
// while still producing on-disk *.json files editors and agents can reference
// via $schema.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/schema"
)

func main() {
	out := "core/schema"
	files := map[string]string{
		"project.schema.json": schema.ProjectSchemaJSON(),
		"auth.schema.json":    schema.AuthSchemaJSON(),
	}
	for name, content := range files {
		path := filepath.Join(out, name)
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write", path, ":", err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}
