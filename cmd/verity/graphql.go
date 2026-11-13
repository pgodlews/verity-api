package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/runner"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func graphqlOptions(env string) graphql.Options {
	const timeout = 30 * time.Second
	return graphql.Options{
		Environment: env,
		Registry:    bootstrap.Default(),
		Timeout:     timeout,
		NewClient: func(tlsCfg *spi.TLSConfig) (*http.Client, error) {
			return runner.NewHTTPClient(tlsCfg, timeout, true)
		},
	}
}

func cmdGraphQL(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "graphql: expected schema or template")
		usage()
		return exitUsage
	}
	switch args[0] {
	case "schema":
		return cmdGraphQLSchema(args[1:])
	case "template":
		return cmdGraphQLTemplate(args[1:])
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "graphql: unknown command %q\n", args[0])
		return exitUsage
	}
}

func cmdGraphQLSchema(args []string) int {
	fs := flag.NewFlagSet("graphql schema", flag.ContinueOnError)
	stepName := fs.String("step", "", "step name")
	suiteName := fs.String("suite", "", "suite name, when the step name is not unique")
	caseName := fs.String("case", "", "case name, when the step name is not unique")
	envName := fs.String("env", "", "environment used to resolve the step URL")
	outPath := fs.String("out", "", "write the SDL to this file")
	jsonOut := fs.Bool("json", false, "emit structured JSON")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 || *stepName == "" {
		fmt.Fprintln(os.Stderr, "graphql schema: usage: verity graphql schema <project> --step NAME [--out FILE]")
		return exitUsage
	}
	project, err := model.Load(pos[0])
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "load", err)
	}
	found := graphql.Find(project, *suiteName, *caseName, *stepName)
	if len(found) == 0 {
		return failJSON(*jsonOut, exitConfig, "step", fmt.Errorf("no graphql step named %q", *stepName))
	}
	if len(found) > 1 {
		return failJSON(*jsonOut, exitConfig, "step", fmt.Errorf("step %q matches %d steps; set --suite and --case", *stepName, len(found)))
	}
	sdl, err := graphql.StepSDL(context.Background(), project, found[0], graphqlOptions(*envName))
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "schema", err)
	}
	red := secrets.NewRedactor()
	sdl = red.Redact(sdl)
	if *outPath != "" {
		if err := fsutil.WriteKeepingMode(*outPath, []byte(sdl)); err != nil {
			return failJSON(*jsonOut, exitConfig, "write", err)
		}
	}
	if *jsonOut {
		emitJSON(map[string]any{"ok": true, "out": *outPath, "sdl": sdl})
		return exitOK
	}
	if *outPath == "" {
		fmt.Fprint(os.Stdout, sdl)
	} else {
		fmt.Fprintf(os.Stderr, "wrote %s\n", *outPath)
	}
	return exitOK
}

func cmdGraphQLTemplate(args []string) int {
	fs := flag.NewFlagSet("graphql template", flag.ContinueOnError)
	operation := fs.String("operation", "", "root field as Type.field, for example Query.user")
	depth := fs.Int("depth", 2, "how many object levels to expand (1-20)")
	jsonOut := fs.Bool("json", false, "emit structured JSON")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 1 || *operation == "" {
		fmt.Fprintln(os.Stderr, "graphql template: usage: verity graphql template <schema.graphql|URL> --operation Type.field [--depth N]")
		return exitUsage
	}
	schema, err := graphql.LoadExternal(context.Background(), pos[0])
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "schema", err)
	}
	tpl, err := graphql.Generate(schema, *operation, *depth)
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "template", err)
	}
	red := secrets.NewRedactor()
	tpl.Query = red.Redact(tpl.Query)
	tpl.Variables = red.Redact(tpl.Variables)
	if *jsonOut {
		var variables any
		if json.Unmarshal([]byte(tpl.Variables), &variables) != nil {
			variables = tpl.Variables
		}
		emitJSON(map[string]any{
			"ok":            true,
			"operation":     tpl.Operation,
			"kind":          tpl.Kind,
			"operationName": tpl.OperationName,
			"query":         tpl.Query,
			"variables":     variables,
		})
		return exitOK
	}
	fmt.Printf("%s\n%s", strings.TrimRight(tpl.Query, "\n"), "\n\n"+strings.TrimRight(tpl.Variables, "\n")+"\n")
	return exitOK
}
