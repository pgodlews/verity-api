# verity — the Verity CLI

A thin command-line shell over the Verity core engine. It contains no
protocol/auth/test/reporting logic; it loads projects, drives `core/runner`, and
renders core's structured results.

## Build

```bash
go build -o verity ./cmd/verity          # from the repo root
./verity version
```

With build metadata:

```bash
go build -ldflags "-X main.version=v0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%FT%TZ)" -o verity ./cmd/verity
```

## Commands

```
verity run <project.yaml> [--env NAME] [--junit FILE] [--json FILE] [--html FILE]
                           [--parallel N] [--timeout 30s] [--var k=v ...]
                           [--suite GLOB ...] [--case GLOB ...] [--until-step N]
                           [--env-file FILE ...] [--env-file-override] [--bail]
                           [--watch] [--plugins FILE] [--json-stdout] [--quiet]
verity load <project.yaml> [--env NAME] [--vus N] (--duration 30s | --iterations N)
                           [--threshold 'p95<500ms'] [--max-error-rate 1%] [--json FILE]
verity validate <project.yaml> [--env NAME] [--json]
verity fmt [--check] [project.yaml ...]
verity init [--force] [dir]
verity hooks install [--force] [--fmt] [project.yaml]
verity hooks uninstall [project.yaml]
verity scan-secrets [project.yaml] [--staged] [--json]
verity import <openapi|wsdl> <spec-file> --out <project.yaml> [--json]
verity import postman <collection.json> [env.json ...] --out <project.yaml> [--json]
verity import soapui <project.xml> --out <project.yaml> [--json]
verity mock <spec-or-project> [--port 0] [--validate] [--json]
verity graphql schema <project.yaml> --step NAME [--out FILE]
verity graphql template <schema.graphql|URL> --operation Type.field
verity plugin new <protocol|assertion|reporter|auth|datasource> <name> [--out DIR]
verity list [--json]
verity schema [--kind project|auth]
verity secret set NAME [--force] [--json] # piped/redirected stdin only
verity secret get NAME [--reveal] [--json]
verity secret rm NAME [--json]
verity secret list [--json]               # unsupported by portable backend
verity version [--json]
```

Flags and positional arguments may be freely interspersed.

`secret` manages OS credential store items under service `verity`. Use
`project/NAME` for an explicit scope and `${secret:keychain:project/NAME}` in
projects. Values never come from arguments. `set` reads stdin, trims surrounding
whitespace, and refuses an empty value; replacing an existing item requires
`--force`. `get` masks by default and warns before printing
plaintext with `--reveal`; `rm` deletes. `list` returns a configuration error
because go-keyring cannot enumerate names portably. Store failures exit 3.
See `docs/auth.md` for platform requirements and headless CI alternatives.

`fmt` defaults to `project.yaml` and formats included fragments too. `--check`
writes nothing and exits 1 when a file would change. `init` manages a marked
`.gitignore` block and an empty-valued `.env.example`; it never replaces the
latter without `--force`. Hook installation uses the installed Git CLI and the
repository's configured hooks path. See `docs/git.md` for the complete workflow.

`--env-file` loads a dotenv file into the process environment before the run
(repeatable; later files win; existing process variables win unless
`--env-file-override`). Values are never printed. `--bail` finishes cases
already running after the first failure or error and reports the rest as
skipped. `--timeout` is the default per-attempt HTTP timeout; a step's
`timeoutMs` overrides it. See `docs/ci.md` and `docs/project-format.md`.

`--watch` runs the project, then re-runs when the project file, an included
fragment, a CSV/JSON feed, or a multipart/`bodyFile` upload changes. The
directories of those files are watched, so an editor that saves by renaming
(vim, VS Code) still triggers a run. Events settle for 300ms before the next
run. When stdout is a terminal, the screen is cleared and a one-line delta
is printed (`2 newly failing, 1 fixed, 14 unchanged`, compared by
suite/case/step). A load or validation error is printed and watching
continues. Ctrl-C exits with the last run's exit code. `--junit`, `--json`,
and `--html` are rewritten on every run. `--watch` may be combined with
`--bail`. There is no TTY requirement: when stdout is not a terminal, the
screen is left alone.

## Exit codes (for CI)

| Code | Meaning |
|------|---------|
| 0 | all tests passed |
| 1 | assertion/transport failure, or `fmt --check` found changes |
| 2 | usage error |
| 3 | validation/config/load error |
| 4 | internal error |
| 5 | interrupted; partial run reports are still written |

## Structured I/O

Use `--json` for structured command output where supported. For `run`,
`--json FILE` writes the result and `--json-stdout` emits it on stdout;
`load --json FILE` writes a load report. See [the agent guide](../../AGENTS.md)
and [CI integration](../../docs/ci.md).

## Examples

```bash
# Validate, then run against staging with 4-way parallelism and CI artifacts.
verity validate project.yaml
# Export API_TOKEN for the selected environment first.
verity run project.yaml --env staging --parallel 4 \
  --junit reports/junit.xml --json reports/result.json

# Generate a project from an OpenAPI spec.
verity import openapi api.yaml --out project.yaml

# Scaffold a plugin.
verity plugin new assertion my-check --out ./my-check
```

## Cross-compilation

```bash
GOOS=windows GOARCH=amd64 go build -o verity.exe ./cmd/verity
GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -o verity ./cmd/verity
```
