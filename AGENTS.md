# AGENTS.md — Working on Verity

Verity is an alpha API contract-testing tool maintained by Piotr Godlewski,
licensed under MIT. The public repository is `github.com/pgodlews/verity-api`.
Read [CONTRIBUTING.md](CONTRIBUTING.md) for contributor expectations and
[docs/architecture.md](docs/architecture.md) for the design.

## Repository map

| Path | Responsibility |
| --- | --- |
| `core/` | Shared model/schema, SPI/registry, protocols, auth, assertions, runner, reporters, importers, secrets, load/mocks, plugin host/SDK, workspace, and Git integration. |
| `cmd/verity/` | CLI dispatch and flags; usage in `main.go`. |
| `gui/` | Separate Go module: Wails desktop bindings over core. |
| `gui/frontend/` | React/TypeScript views, browser development bridge, and Node tests. |
| `plugins/grpc/` | Separate Go module: first-party unary and streaming gRPC plugin. |
| `plugins/example-assertion/` | Separate Go module: example out-of-process assertion plugin. |
| `examples/` | Sample projects, contracts, and local demo fixtures. |
| `docs/` | User/reference docs, extension guides, cookbook, and proposals. |
| `scripts/`, `.github/` | Build/release helpers, GitHub CI, and the composite action. |

Protocol, auth, assertion, execution, and reporting behavior belongs in core.
CLI and desktop code should adapt inputs/outputs to the same core operations.
Core keeps a gRPC stub until the first-party plugin is loaded. A proposal in
`docs/proposals/` is not evidence that a feature is implemented.

## Build and checks

Use Go 1.26+, Git, and Node.js 20.19+ (20.x) or 22.12+ with npm. Desktop builds
also need Wails v2 and the platform webview dependencies; see [gui/README.md](gui/README.md).
Run commands from the repository root. There are **four Go modules**; root
`go test ./...` does not test the nested modules.

```bash
# Root module: core, CLI, and host integration tests
go build -o verity ./cmd/verity
./verity version --json
go vet ./...
go test ./...
go test -race ./...

# Frontend: dependencies, Node tests, TypeScript, and production bundle
(cd gui/frontend && npm ci && npm test && npm run build)

# GUI backend module (the frontend build supplies embedded assets)
(cd gui && go vet ./... && go test ./...)

# First-party gRPC plugin module
(cd plugins/grpc && go vet ./... && go test ./... && go test -race ./...)
(cd plugins/grpc && go build -o verity-grpc .)

# Example assertion plugin module
(cd plugins/example-assertion && go vet ./... && go test ./...)
(cd plugins/example-assertion && go build -o example-assertion .)

# Desktop bundle; on Linux with WebKitGTK 4.1, add -tags webkit2_41
(cd gui && wails build -o verity-desktop)
```

Run checks for every affected module and frontend area. Format Go changes
with `gofmt`. Add tests for every behavior change, including regression tests
for fixes, cancellation/error cases, and redaction where applicable. Report
exactly which checks ran and which could not run; do not infer test success
from reading code.

## Project rules

- Never store plaintext secrets in project files or add credentials to code,
  fixtures, or docs. Use `${env:NAME}` or `${secret:keychain:project/NAME}`;
  keep `.env.example` values empty and supply test secrets at runtime.
- Do not hardcode hostnames, IP addresses, or personal paths. Use environment
  configuration, temporary directories, and dynamically allocated test
  listeners. Do not publish private infrastructure details.
- Route captures, errors, logs, reports, plugin output, and desktop results
  through `core/secrets`. Redact before values cross an output boundary,
  including failures before a request is sent. Never log auth identities,
  tokens, or secret cache keys.
- Use stable `core/verr` codes with a useful message and project path where
  available. Test the code/path, rather than matching human console text.
- All product Git operations go through `core/vcs`. Preserve its credential,
  hook/filter, index, and conflict protections; never force-push or store Git
  credentials. Preserve unsaved desktop drafts and stale-save detection.
- Preserve JSON numeric literals and YAML comments/order when editing projects.
  Keep secrets and data-file contents out of formatting changes.
- Update docs and [ROADMAP.md](ROADMAP.md) when behavior changes. User-facing
  release notes belong in [CHANGELOG.md](CHANGELOG.md). Verify commands
  against `cmd/verity/main.go` and the corresponding flag handlers.

## Authoring and driving test projects

Read the schema and extension catalog instead of guessing fields or types:

```bash
verity schema
verity schema --kind auth
verity list --json
verity validate project.yaml --json
verity fmt project.yaml
verity fmt --check project.yaml
verity scan-secrets project.yaml --json
verity run project.yaml --json-stdout
```

Use structured JSON output when available. `run --json FILE` writes a report;
`--json-stdout` emits it on stdout. Standard exit codes are `0` success, `1`
test/check failure, `2` usage error, `3` validation/config error, `4` internal
error, and `5` interruption (with partial run reports). Command-specific
behavior is documented in [docs/ci.md](docs/ci.md).

Validate against [the project reference](docs/project-format.md) and follow
[auth scope and secret rules](docs/auth.md). For imports, inspect every
warning: unsupported source behavior must never silently disappear. For
extension work, read [the plugin guide](docs/extending-plugins.md).
[docs/agents-guide.md](docs/agents-guide.md) is the detailed guide for agents
and automation that drive Verity: authoring, running and reading results,
imports, mocks, load tests, the auth model, GraphQL schemas, and gRPC.

## Pull requests

Use one focused branch per change and open a GitHub PR against `main`.
Use Conventional Commits for commit messages and the PR title, such as
`fix(runner): preserve captures after retries` or `docs: clarify installation`.
Explain the problem, resulting behavior, validation, and compatibility impact;
link the issue when applicable. Mark breaking changes with `!` or a
`BREAKING CHANGE:` footer. PRs are squash merged after review and green CI.
Do not commit or publish unless the task authorizes it.
