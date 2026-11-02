package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/importers"
	"github.com/pgodlews/verity-api/core/workspace"
)

func cmdImport(args []string) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	out := fs.String("out", "", "output project file (YAML or JSON); default stdout")
	jsonOut := fs.Bool("json", false, "emit structured JSON status")
	fs.SetOutput(os.Stderr)
	pos, err := parsePermuted(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "import: usage: verity import <openapi|wsdl|postman|soapui> <spec-file> [postman-env.json ...] --out project.yaml")
		fmt.Fprintln(os.Stderr, "(`soap` is an alias of wsdl, not a SoapUI project)")
		return exitUsage
	}
	kind, spec, extra := pos[0], pos[1], pos[2:]
	if len(extra) > 0 && kind != "postman" {
		fmt.Fprintf(os.Stderr, "import: unexpected arguments %q (only postman accepts environment files)\n", extra)
		return exitUsage
	}

	var res *importers.Result
	switch kind {
	case "openapi", "rest":
		res, err = importers.ImportOpenAPI(spec)
	case "wsdl", "soap":
		res, err = importers.ImportWSDL(spec)
	case "postman":
		res, err = importers.ImportPostman(spec, extra...)
	case "soapui":
		res, err = importers.ImportSoapUI(spec)
	default:
		fmt.Fprintf(os.Stderr, "import: unknown type %q (want openapi|wsdl|postman|soapui; soap is an alias of wsdl)\n", kind)
		return exitUsage
	}
	if err != nil {
		return failJSON(*jsonOut, exitConfig, "import", err)
	}

	target := *out
	var data []byte
	if target == "" {
		var merr error
		data, merr = res.Project.Marshal("project.yaml")
		if merr != nil {
			return failJSON(*jsonOut, exitInternal, "marshal", merr)
		}
		if !*jsonOut {
			os.Stdout.Write(data)
		}
	} else {
		if werr := res.Project.Save(target); werr != nil {
			return failJSON(*jsonOut, exitInternal, "save", werr)
		}
		if _, ierr := workspace.Init(workspace.InitOptions{Dir: filepath.Dir(target), GitignoreOnly: true}); ierr != nil {
			return failJSON(*jsonOut, exitInternal, "init", ierr)
		}
	}

	if *jsonOut {
		resp := map[string]any{
			"ok":       true,
			"type":     kind,
			"out":      target,
			"suites":   len(res.Project.Suites),
			"warnings": res.Warnings,
		}
		if target == "" {
			resp["content"] = string(data)
		}
		emitJSON(resp)
	} else {
		if target != "" {
			fmt.Printf("Imported %s → %s (%d suite(s))\n", spec, target, len(res.Project.Suites))
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
	}
	return exitOK
}
