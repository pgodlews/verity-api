package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
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
	if *jsonOut {
		emitJSON(map[string]any{
			"ok":      len(errs) == 0,
			"project": project.Name,
			"errors":  errs,
		})
	} else {
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
