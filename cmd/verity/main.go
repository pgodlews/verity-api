// Command verity is Verity's CLI — a thin shell over the core engine. It holds
// no protocol, auth, assertion, or reporting logic itself; everything is driven
// through the core library and its extension registry.
//
// Subcommands:
//
//	verity run <project>        execute a project and report results
//	verity load <project>       run the same cases concurrently and check thresholds
//	verity validate <project>   validate a project against the model
//	verity fmt [project ...]    normalize project formatting
//	verity init [dir]           make a project directory git-ready
//	verity hooks <action>       install or uninstall the pre-commit hook
//	verity import <type> <spec> generate a project from a WSDL/OpenAPI spec
//	                             or a Postman collection
//	verity mock <spec>          serve a fake API from OpenAPI, WSDL, or a project
//	verity plugin new <type>    scaffold a new external plugin
//	verity list                 list registered extensions (capabilities)
//	verity schema               print the project JSON Schema
//	verity graphql              schema-aware GraphQL helpers
//	verity secret               manage OS keychain items
//	verity version              print version info
//
// Most commands accept --json for structured, machine-consumable output
// (run and load take --json FILE for a report and run has --json-stdout;
// schema prints JSON already), and every command returns correct exit codes
// for CI (non-zero on validation/assertion/transport failure).
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches to a subcommand and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest)
	case "load":
		return cmdLoad(rest)
	case "validate":
		return cmdValidate(rest)
	case "fmt":
		return cmdFmt(rest)
	case "init":
		return cmdInit(rest)
	case "hooks":
		return cmdHooks(rest)
	case "import":
		return cmdImport(rest)
	case "mock":
		return cmdMock(rest)
	case "plugin":
		return cmdPlugin(rest)
	case "list":
		return cmdList(rest)
	case "schema":
		return cmdSchema(rest)
	case "graphql":
		return cmdGraphQL(rest)
	case "scan-secrets":
		return cmdScanSecrets(rest)
	case "secret":
		return cmdSecret(rest)
	case "version":
		return cmdVersion(rest)
	case "-h", "--help", "help":
		usage()
		return exitOK
	default:
		if f, ok := devCommands[cmd]; ok {
			return f(rest)
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		return exitUsage
	}
}

// devCommands are internal commands compiled in only by build tags: the
// GUI's browser dev bridge (gui-call, -tags devbridge). Release builds have
// none.
var devCommands = map[string]func([]string) int{}

func usage() {
	fmt.Fprint(os.Stderr, `verity — Verity API contract testing

Usage:
  verity run <project.yaml> [--env NAME] [--junit FILE] [--json FILE]
                             [--html FILE] [--json-stdout] [--quiet]
                             [--parallel N] [--timeout 30s] [--var k=v ...]
                             [--suite GLOB ...] [--case GLOB ...] [--until-step N]
                             [--env-file FILE ...] [--env-file-override] [--bail]
                             [--watch] [--allow-external-files]
                             [--follow-redirects=false]
                             [--plugins FILE | --trust-plugins]
                             [--reporter TYPE=FILE ...]
  verity load <project.yaml> [--env NAME] [--suite GLOB ...] [--case GLOB ...]
                             [--var k=v ...] [--env-file FILE ...] [--env-file-override]
                             [--timeout 30s] [--vus N] (--duration 30s | --iterations N)
                             [--ramp-up 10s] [--think 0ms]
                             [--threshold 'p95<500ms' ...] [--max-error-rate 1%]
                             [--json FILE] [--allow-remote]
                             [--plugins FILE | --trust-plugins]
  verity validate <project.yaml> [--env NAME] [--json]
  verity fmt [--check [--staged]] [--json] [project.yaml ...]
  verity init [--force] [dir]
  verity hooks install [--force] [--fmt] [project.yaml]
  verity hooks uninstall [project.yaml]
  verity scan-secrets [project.yaml] [--staged] [--json]
                             find plaintext credentials (exit 1 if any);
                             --staged checks what the next git commit records
  verity import <openapi|wsdl> <spec-file> --out <project.yaml> [--force] [--json]
  verity import postman <collection.json> [env.json ...] --out <project.yaml> [--force] [--json]
  verity import soapui <project.xml> --out <project.yaml> [--force] [--json]
                             --out refuses an existing file unless --force
                        (soap is an alias of wsdl, not a SoapUI project)
  verity mock <spec-or-project> [--port 0] [--host 127.0.0.1]
                        [--validate] [--delay MS] [--seed N] [--json]
  verity plugin new <protocol|assertion|reporter|auth|datasource> <name> [--out DIR]
                             [--force] [--json]
  verity list [--json]
  verity schema [--kind project|auth]
  verity graphql schema <project.yaml> --step NAME [--suite NAME]
                             [--case NAME] [--env NAME] [--out FILE]
                             fetch or print the step's GraphQL SDL
  verity graphql template <schema.graphql|URL> --operation Type.field
                             [--depth N]
                             print a query and a variables JSON skeleton
  verity secret set NAME [--force] [--json]  read value from stdin; --force replaces
  verity secret get NAME [--reveal] [--json]
  verity secret rm NAME [--json]
  verity version [--json]

Run:
  --env-file FILE       load KEY=VALUE pairs before the run (repeatable).
                        Later files win. Variables already in the environment
                        win unless --env-file-override is set. Values are
                        never printed; a bad line is a config error (exit 3)
                        reported at file:line.
  --env-file-override   let --env-file replace variables already set
  --bail                after the first failed or errored case, finish
                        in-flight cases and skip the rest. The summary
                        reports bailed. The process still exits 1.
  --watch               re-run when the project, an included fragment, a
                        feed, a multipart/bodyFile upload, or a GraphQL
                        schema file changes.
                        Saves are debounced. On a TTY, clear the screen and
                        print a one-line delta versus the previous run.
                        A load or validation error is printed and watching
                        continues. Ctrl-C exits with the last run's code.
                        --junit/--json/--html are rewritten every run.
                        May be combined with --bail.
  --allow-external-files  let uploads, ${secret:file:...}, mTLS cert/key/CA
                        files, json_schema files, and feed files be read
                        from outside the project directory (trusted
                        projects only)
  Ctrl-C / SIGTERM      cancel the run. The in-flight step finishes or
                        fails; work that never started is reported as
                        aborted; requested reporters are still written;
                        the process exits 5. A second signal exits
                        immediately.

Mock:
  verity mock serves an OpenAPI 3 document, a WSDL 1.1 document, or a
  Verity project (its mocks: section) on 127.0.0.1. Pass --host 0.0.0.0
  to listen on all interfaces. --port 0 prints the chosen port. Ctrl-C
  stops the server and exits 0. See docs/mocks.md.

Load:
  verity load runs the selected cases from many virtual users and reports
  latency and throughput. Each iteration is one full case run, so transfers
  stay inside that virtual user. Suite setup hooks run once first; their
  run-scoped captures are the baseline for every user. Teardown is not run.
  Pass --duration or --iterations, not both. --iterations is the number of
  case runs each virtual user performs.
  --vus is capped at 1000. A warning is printed above 200.
  By default only loopback, RFC1918, and link-local targets are allowed.
  Hosts are resolved before any request, including each feed row, and
  checked again when each HTTP connection is dialed (redirects, proxies,
  OAuth token requests, captured hosts). dns:// and passthrough:// targets
  are screened on the path and on a non-empty authority. An out-of-process
  protocol step is screened on target, url, address, endpoint, and host;
  a step with none of those is refused. Any other target needs
  --allow-remote. The first refused dial stops the run (exit 3).
  The CLI prints the target hosts and the VU count and
  duration (or iterations) before starting.
  --threshold is repeatable: p50|p90|p95|p99|max|mean <|<= 500ms|1s,
  optionally prefixed with a step name ('GET widget:p95<300ms').
  --max-error-rate fails the run when assertion failures, transport errors,
  and other step errors exceed the rate (1% or a fraction 0.01; a percentage
  above 100 is a usage error). A latency threshold or --max-error-rate fails
  when no request ran.
  With no thresholds and no --max-error-rate, the process exits 0 even when
  requests failed; the report still counts them.
  --json FILE writes the report documented in docs/load.md. Output is
  redacted. On a TTY, a one-line progress update is printed every second.
  All virtual users share one HTTP connection pool.
  Ctrl-C / SIGTERM stops new iterations, waits for the request in flight,
  writes the report, and exits 5. A second signal exits immediately.

Secrets:
  OS credential store service: verity. Use project/NAME for explicit scopes.
  get masks values by default; --reveal prints plaintext and warns on stderr.
  No value arguments or echoing prompts. Missing or locked/unavailable items
  are config errors (exit 3). See docs/auth.md.

Global:
  --json           emit structured JSON to stdout (machine-consumable)

Exit codes:
  0  success
  1  test failures (assertion/transport)
  2  usage error
  3  validation/config error
  4  internal error
  5  interrupted (SIGINT/SIGTERM). Partial JUnit/JSON/HTML reports are
     still written. A second signal exits immediately.
`)
}
