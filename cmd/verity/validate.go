package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/workspace"
)

func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit structured JSON result")
	envName := fs.String("env", "", "environment used to resolve URLs, queries, and variables")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "validate: missing project file")
		return exitUsage
	}

	project, err := model.Load(pos[0])
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "load", err)
	}
	errs := project.Validate()
	errs = append(errs, graphql.ValidateProject(context.Background(), project, graphqlOptions(*envName))...)
	warnings := validateWarnings(pos[0])
	if *jsonOut {
		emitJSON(map[string]any{
			"ok":       len(errs) == 0,
			"project":  project.Name,
			"errors":   errs,
			"warnings": warnings,
		})
	} else {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w.Message)
		}
		if len(errs) == 0 {
			fmt.Printf("OK: %s is valid\n", pos[0])
		} else {
			fmt.Fprintf(os.Stderr, "INVALID: %s\n", pos[0])
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  - %s\n", e.Error())
			}
		}
	}
	if len(errs) > 0 {
		return exitConfig
	}
	return exitOK
}

// validateWarning is a non-fatal finding of `verity validate`. Code is stable
// and machine-readable.
type validateWarning struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Name    string   `json:"name,omitempty"`
	UsedIn  []string `json:"usedIn,omitempty"`
}

// validateWarnings reports plain ${NAME} references that only the process
// environment defines: they resolve (redacted) from the environment, but the
// author usually meant ${env:NAME}.
func validateWarnings(path string) []validateWarning {
	out := []validateWarning{}
	info, err := workspace.Inspect(path)
	if err != nil {
		return out
	}
	for _, f := range info.EnvFallbacks() {
		out = append(out, validateWarning{
			Code: "env_fallback",
			Message: fmt.Sprintf("${%s} is not defined by the project (missing in %s) and resolves from the process environment; write ${env:%s} if that is intended",
				f.Name, strings.Join(f.MissingIn, ", "), f.Name),
			Name:   f.Name,
			UsedIn: f.UsedIn,
		})
	}
	return out
}
