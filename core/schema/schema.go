// Package schema publishes Verity's versioned JSON Schemas for the project
// format and for auth provider configuration. Editors and AI agents use these
// to author and validate artifacts without guessing.
//
// The *.json files in this directory are the single source of truth. They are
// embedded into the binary, so the CLI (verity schema) and GUI never read the
// filesystem, and editors can reference the same files via $schema.
package schema

import (
	_ "embed"
	"strings"
)

//go:embed project.schema.json
var projectSchemaFile string

//go:embed auth.schema.json
var authSchemaFile string

var (
	projectSchema = strings.TrimSuffix(projectSchemaFile, "\n")
	authSchema    = strings.TrimSuffix(authSchemaFile, "\n")
)

// ProjectSchemaJSON is the JSON Schema (2020-12) for a Verity project.
func ProjectSchemaJSON() string { return projectSchema }

// AuthSchemaJSON is the JSON Schema for the built-in auth provider configs.
func AuthSchemaJSON() string { return authSchema }
