# Architecture

Verity is a lean, cross-platform API contract testing tool. It is a clean-room,
"back to the roots" alternative to SoapUI: no JVM, no bloat, no paid tier, no
telemetry, no login. Single self-contained binaries; MIT/Apache-2.0.

## One core, two thin shells

```
        ┌─────────────────────────────────────────────┐
        │                   core/                      │
        │  model · registry · spi (interfaces) ·       │
        │  protocols · assertions · auth · runner ·    │
        │  reporters · importers · secrets · plugin    │
        └───────────────▲───────────────▲──────────────┘
                        │               │
              ┌─────────┘               └─────────┐
        ┌─────┴──────┐                       ┌─────┴──────┐
        │ cmd/verity │                       │   gui/     │
        │  verity    │                       │  Wails app │
        │ (thin)     │                       │ (thin)     │
        └────────────┘                       └────────────┘
```

**No protocol, auth, test, or reporting logic lives outside `core/`.** The CLI
and GUI only translate user intent into core calls and render core's structured
results. This guarantees the CLI and GUI always behave identically — including
secret redaction, which is enforced centrally in core.

## Module layout

| Path        | Module                    | Purpose |
|-------------|---------------------------|---------|
| `core/`     | `github.com/pgodlews/verity-api` | The engine: model, interfaces, registry, protocols, assertions, auth, runner, reporters, importers, plugin host + SDK. |
| `cmd/verity/` | (same module)           | `verity` — a thin CLI wrapper. |
| `gui/`      | `.../gui`                 | Wails desktop app (Go ↔ React/TS) over core. Separate module so the base CLI stays free of GUI deps. |
| `plugins/`  | per-plugin modules        | First-party example plugins + a scaffolded template. |
| `docs/`     | —                         | This documentation. |
| `examples/` | —                         | Sample projects and specs. |

Keeping `gui/` and each plugin in their own module means the **base CLI binary
stays lean and fast**: it never compiles Wails, a browser engine, or any plugin
code into itself.

## Core packages

- **`core/model`** — the project data model and YAML/JSON loader (with multi-file
  `include` merging), plus semantic `Validate`.
- **`core/spi`** — the stable Service Provider Interfaces: `Protocol`,
  `Assertion`, `Reporter`, `AuthProvider`, `DataSource`. Also the shared
  `Request`/`Response`/`RunResult` types. This is the public contract.
- **`core/registry`** — a typed lookup table mapping type ids to extensions.
  No singletons; the host constructs a registry explicitly.
- **`core/protocols`** — REST, SOAP, GraphQL (full) and a gRPC stub. Unary
  gRPC is the out-of-process plugin `plugins/grpc`, which replaces the stub
  when a project loads it.
- **`core/graphql`** — SDL and introspection loading, operation checks,
  response checks, and query templates. Parsing uses gqlparser (MIT).
- **`core/assertions`** — status, response_time, jsonpath, xpath, json_schema,
  graphql_schema, xsd, contains, regex, header — all SPI implementations.
- **`core/auth`** — Tier-1 providers and the scope-aware auth resolver/chain.
- **`core/runner`** — walks suites→cases→steps, applies auth, drives protocols,
  evaluates assertions, performs transfers and data feeds, builds `RunResult`.
  `CaseExec` runs one case at a time for load testing.
- **`core/load`** — virtual users over `CaseExec`; latency histogram, thresholds,
  and the private-target check (pre-run screen and connect-time dial guard)
  used by `verity load`.
- **`core/reporters`** — console, JUnit, JSON, HTML, all consuming `RunResult`.
- **`core/secrets`** — interpolation (`${...}`) and the single `Redactor` used
  by every output path.
- **`core/importers`** — OpenAPI, WSDL, Postman, and SoapUI → project scaffolding.
  Postman saved examples become project `mocks`.
- **`core/mock`** — HTTP mock server for OpenAPI 3, WSDL 1.1, and project
  `mocks`. The CLI command is `verity mock`.
- **`core/plugin`** + **`core/plugin/sdk`** — out-of-process plugin host and the
  author-facing SDK (VPP/1).
- **`core/schema`** — published JSON Schemas for the project and auth config.
- **`core/verr`** — structured, machine-readable errors.

## Execution flow (a run)

1. The CLI loads any `--env-file` into the process environment, then loads and
   validates the project (`model.Load` + `Validate`). The GUI loads and
   validates only.
2. The host builds a `registry` (built-ins via `bootstrap.Default`) and
   optionally loads external plugins.
3. `runner.Run` selects the environment, then for each suite:
   - runs setup hooks,
   - runs cases on a bounded goroutine pool (parallelism configurable),
   - runs teardown hooks.
4. For each step: interpolate request → build via the protocol plugin → resolve
   + apply the auth chain (transport TLS first, then message providers) →
   execute → capture (redacted) → evaluate assertions. A step `retry` repeats
   execute and assertions; property transfers run once, from the final attempt.
   See `docs/project-format.md`.
5. The runner produces a single `RunResult`; the host hands it to one or more
   reporters. With `--watch`, the CLI repeats the load, run, and report when
   the project, a fragment, a feed, or an upload file changes. `--env-file`
   is loaded once, before the loop.

## Concurrency

Cases within a suite run concurrently on a semaphore-bounded goroutine pool
(`--parallel N`, or per-suite `parallel:`). Steps within a case run
sequentially because they can depend on each other via property transfers.
With `--bail`, the runner stops scheduling new cases after the first failed
or errored case. Cases already in flight finish; cases that never started are
reported as skipped, and `summary.bailed` is set. A step that passes after its
own retries is a pass and does not trip bail.

Cancelling the run context (CLI SIGINT/SIGTERM, or the GUI's Cancel) is
checked before each suite, case, feed row, step, and retry attempt. Work that
never started is reported as `aborted` and `summary.aborted` is set. A step
already in flight finishes or fails on that context; it is not relabelled.
Teardown of a suite that had started runs on a fresh 10 second context.
`runner.Options.OnEvent` (nil for the CLI) is called from the case goroutines
with `case_started`, `step_done` (the redacted step), and `case_done`. The
callback must be safe for concurrent use.

## Cross-compilation

Core and CLI are pure Go with small, pure-Go dependencies, so they cross-compile
with `GOOS`/`GOARCH` to Linux/macOS/Windows producing single binaries. See
`README.md` → "Cross-compilation". The GUI uses Wails (native webview, not
Electron) and is built with the Wails CLI.

## Design tenets

- **Extensibility is first-class.** Built-ins use the same SPI as third-party
  plugins; there are no privileged code paths.
- **AI-agent friendly.** Strongly-typed public interfaces, no hidden global
  state, deterministic machine-readable errors, JSON Schema for artifacts, and
  structured JSON I/O on every command.
- **Secure by default.** Secrets are never stored; one redactor masks them in
  every output path.
- **Lean base, opt-in extras.** Scripting, the GUI, and plugins are kept off the
  base binary's hot path.
