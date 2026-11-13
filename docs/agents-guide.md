# Driving Verity programmatically (guide for agents and automation)

This file is written for AI coding agents and automation. It tells you, cold,
how to author projects, run them, read results, and extend Verity — without
guessing. Humans: see `README.md` and `docs/`.

Verity is a lean, cross-platform API contract testing tool (REST, SOAP,
GraphQL; gRPC unary and streaming via `plugins/grpc`, with a core stub until that plugin
is loaded). The CLI binary is `verity`. Everything is driven
through one core library; the CLI and GUI are thin shells over it.

## Golden rules for agents

1. **Author projects against the JSON Schema.** Get it with `verity schema`
   (project) and `verity schema --kind auth` (auth config). Validate before
   running: `verity validate project.yaml --json`. Read its `warnings`
   array too: `env_fallback` names a plain `${NAME}` that only the process
   environment defines (write `${env:NAME}` if that is intended).
2. **Never put plaintext secrets in project files.** Use `${env:VAR}` or
   `${secret:provider:key}`. Validation warns on literal-looking secrets.
   `${secret:keychain:project/NAME}` reads service `verity`, account
   `project/NAME`; the scope is explicit, never based on the project path.
   `verity secret set NAME --json` reads piped/redirected stdin only.
   `get` masks unless `--reveal` (which warns); `rm` deletes. There is no
   `list` (the portable backend cannot enumerate names). Missing/locked/unavailable items are config
   errors; use environment secrets in headless CI. See `docs/auth.md`.
3. **Drive everything with `--json`.** Every command emits structured JSON for
   machine consumption. Parse that, not the human console text.
4. **Trust exit codes.** `0` success, `1` test failure, `2` usage error,
 `3` validation/config error, `4` internal error, `5` interrupted
 (SIGINT/SIGTERM; partial reports are still written).
5. **Errors are structured.** They carry a stable `code`, a `message`, and
   often a `path` locating the problem in the project.
6. **Keep project diffs stable.** Run `verity fmt project.yaml` after authoring
   and `verity fmt --check project.yaml` in CI. Formatting includes fragments,
   keeps key order and comments, and never rewrites feed or upload files.

## Authoring a project (minimal, valid)

```yaml
schemaVersion: "1.0"
name: My API Tests
environments:
  - name: staging
    default: true
    variables:
      BASE_URL: "https://staging.example.com"
auth:
  - name: token
    type: bearer
    config:
      token: "${env:API_TOKEN}"     # resolved at run time, never stored
suites:
  - name: Smoke
    auth:
      use: [token]                  # applies to all cases/steps below
    cases:
      - name: list widgets
        steps:
          - name: GET /widgets
            protocol: rest
            request:
              method: GET
              url: "${BASE_URL}/widgets"
            assertions:
              - type: status
                value: 200
              - type: jsonpath
                expr: "$.items"
                op: exists
            transfers:
              - name: firstId           # capture for a later step
                from: jsonpath
                expr: "$.items[0].id"
```

## Git setup and checks

```bash
# Create .gitignore, .env.example, and (when absent) a starter project.yaml.
verity init
# Normalize a project and all included fragments; --check never writes.
verity fmt --check project.yaml
# Store this project's repo-relative path in the pre-commit hook.
verity hooks install --fmt project.yaml
```

`init` never overwrites `.env.example` without `--force`. Imports update only
the `.gitignore` block. The hook calls `verity scan-secrets --staged` and,
with `--fmt`, `verity fmt --check`; see `docs/git.md`.

The desktop app shells out to the installed `git` CLI (`core.fsmonitor`
forced off, `--no-pager`, `LC_ALL=C.UTF-8` when that locale exists, else
`C`; read commands blank repository-local clean/smudge/process filters and
set `GIT_NO_LAZY_FETCH=1`, and leave global filters such as git-lfs alone).
It shows branch,
ahead/behind from local refs, and a semantic diff of the working tree
against HEAD. A save that finds the loaded file changed returns `conflict`
and writes nothing. The desktop editor requires a step hash or, when the
step has none, the file hash; the core save functions still allow both to
be empty for CLI and dev-bridge callers.
Source control commits only the paths you pick (the scanned index blobs,
after a scan of those blobs), fetch, pull, and push use an explicit refspec
from `branch.<name>.remote` and `branch.<name>.merge`, and
branch switch uses `git switch` (Git 2.23+). It never force-pushes, never
stores git credentials, and does not resolve merge conflicts — those are
fixed in your editor. Source control also lists stashes by immutable object
id, saves tracked or selected paths (optionally non-ignored untracked files),
and shows redacted semantic stash diffs. Pop and drop require GUI confirmation;
a conflicting apply keeps the stash. Stash rewrites use the same reload and
unsaved-draft conflict flow as pull. The status bar is the local branch,
ahead/behind or "upstream gone", and changed-path count. See `docs/git.md`.

## Running and reading results

```bash
# Human run
verity run project.yaml --env staging

# CI run: JUnit + JSON artifacts, non-zero exit on failure.
# --env-file loads a dotenv file into the process environment before the run
# (later files win; existing process variables win unless --env-file-override).
# --bail finishes in-flight cases after the first failure and skips the rest.
verity run project.yaml --env staging --env-file .env --bail \
  --junit out.xml --json report.json --parallel 4

# Pure machine run: structured JSON to stdout
verity run project.yaml --json-stdout

# Re-run when the project, a fragment, a feed, or an upload file changes
verity run project.yaml --watch

# Load: same cases, many virtual users, latency / error-rate thresholds.
# --iterations is per virtual user. Loopback / RFC1918 / link-local only
# unless --allow-remote, checked again when each HTTP connection is dialed
# (redirects, proxies, feeds, captured hosts). Exit 1 when a threshold
# fails, 3 when a target is refused, 5 on Ctrl-C.
verity load project.yaml --env local --vus 10 --duration 30s \
  --threshold 'p95<500ms' --max-error-rate 1% --json load.json
```

`verity load` reuses the runner (`core/load` calls `runner.CaseExec`). Each
iteration is one case, so transfers stay on that virtual user. Suite setup
hooks run once before the users start; their `scope: run` captures are the
shared baseline. The JSON report shape is `docs/load.md`. Percentiles are
histogram estimates (about 1%, in microseconds, reported as milliseconds
with three decimals); min, mean, and max are exact. A threshold or
`--max-error-rate` fails when no request ran. Output goes
through the same redactor as `verity run`.

The JSON result shape is defined in `core/spi/result.go` (`RunResult`). Key
fields: `summary.status` (`passed|failed|error|skipped|aborted`), `summary.bailed`
(true when `--bail` stopped scheduling new cases; omitted otherwise),
`summary.aborted` (true when the run was cancelled; omitted otherwise),
`summary.passed/failed/errors`, and a `suites[].cases[].steps[]` tree where
each step has redacted `request`/`response` captures, an `assertions[]`
array, and `attempts` (executions of the request; omitted when the step never
ran). `durationMs` on a step is the total across attempts and retry delays;
the captures are the final attempt. Cases that `--bail` never started are
present with status `skipped`.

Digest may replay one HTTP challenge within an attempt; captures show the
response after that replay, and `attempts` still counts step attempts.

A step may set `timeoutMs` (per-attempt HTTP timeout, overriding `--timeout`)
and `retry: {count, delayMs, until}`. `until: passed` retries while an
assertion fails or the attempt is a transport error. `until: status` retries
only on a transport error, HTTP 429, or HTTP 5xx. Total attempts are
`1 + count`, and `count` must be 0–20. Transfers come from the final attempt.
A step that passes after retries does not trip `--bail`. A cancelled run
reports work that never started as `aborted` (exit code 5) and still writes
the requested reporters. See `docs/project-format.md` and `docs/ci.md`.

`--watch` re-runs when the project file, an included fragment, a CSV/JSON
feed, or a multipart/`bodyFile` upload changes (directories are watched, so
rename-saves still trigger). Saves are debounced (300ms). On a TTY the screen
is cleared and a one-line delta is printed (`2 newly failing, 1 fixed, 14
unchanged`, compared by suite/case/step). A load or validation error is
printed and watching continues. Ctrl-C exits with the last run's exit code.
`--junit`, `--json`, and `--html` are rewritten every run. `--watch` may be
combined with `--bail`. `model.Load` records the watched paths on
`Project.SourceFiles` (absolute, not serialized).

## Discovering capabilities

```bash
verity list --json
# => {"protocols":[...],"assertions":[...],"reporters":[...],"auth":[...],"dataSources":[...]}
```

Use this to know which `protocol`, `assertion type`, and `auth type` values are
valid before authoring.

## Importing from a contract

```bash
verity import openapi spec.yaml --out project.yaml --json
verity import wsdl    service.wsdl --out soap.yaml --json
verity import postman collection.json [env.json ...] --out project.yaml --json
verity import soapui  project.soapui.xml --out project.yaml --json
```

`soap` is the WSDL importer. A SoapUI project is `verity import soapui`.

Postman imports convert `{{var}}` to `${var}`, common `pm.*` test idioms to
assertions/transfers, and replace literal secrets with `${env:NAME}`.
`formdata` bodies become REST `multipart` parts and `file` bodies become
`bodyFile` (both read from the project directory at send time). Read the
`warnings` array: it lists every script line, auth type, or body that was not
converted, and which environment variables to export.

OpenAPI path params `{id}` become `${id}` variables. Generated projects always
validate.

Postman saved example responses (`item.response[]`) become a top-level `mocks:`
section. A path segment that is only a variable becomes a `{name}` template.
Literal credentials in an example path, header, or body (URL userinfo and
high-confidence token prefixes included) are replaced with the placeholder
text `REDACTED` (not `${env:…}`) and listed in `warnings`. The warning names
the request item, not the example name.

SoapUI import reads one project XML file (50 MB limit). It maps SOAP `request`
steps and `restrequest` steps, rewrites `${#Project#name}` /
`${#TestCase#name}` / `${#TestSuite#name}` to `${name}`, turns property
transfers into case-scoped `transfers` on the source step (a warning names any
request that will see the value earlier than in SoapUI), and replaces literal
secrets with `${env:NAME}`. Read the `warnings` array. See
`docs/cookbook/import-soapui.md`. The importer is clean-room and is not
affiliated with or endorsed by SmartBear.

## Mock server

```bash
verity mock spec-or-project.yaml [--port 0] [--host 127.0.0.1] \
  [--validate] [--delay MS] [--seed N] [--json]
```

`verity mock` detects OpenAPI 3.x, WSDL 1.1, or a Verity project the same way
import does, and serves it on `127.0.0.1` (pass `--host 0.0.0.0` or `::` to
listen on all interfaces; the printed URL stays loopback). `--port 0` prints
the chosen port. Ctrl-C exits 0. OpenAPI routes use the operation example or
a deterministic schema sample (`--seed`); `--validate` returns 400 with the
schema errors, and a body over 1MiB is 413. WSDL dispatches on SOAPAction or
the SOAP 1.2 content-type `action` parameter and returns a SOAP Fault for an
unknown operation. A project's optional `mocks:` section is served as
declared, including a mocks-only file that has no suites. See `docs/mocks.md`.

## Extending Verity (see docs/extending-*.md)

- Add a protocol/assertion/reporter/auth/datasource as an **out-of-process
  plugin** (no host recompile): `verity plugin new <type> <name>`. This
  scaffolds a buildable module with stubs, a test, a manifest, and a README.
- Register plugins via `<project-dir>/.verity/plugins.json` (run it with
  `--trust-plugins` or `VERITY_TRUST_PLUGINS=1`; otherwise the run exits 3
  with `config_error`) or `verity run --plugins path.json`.
- The plugin wire protocol (VPP/1) is documented in `docs/extending-plugins.md`.

## Two-layer auth model (for correct auth authoring)

- **Transport layer** (`mtls`): client cert/key, custom CA, TLS version control.
- **Message layer** (`basic`, `digest`, `bearer`, `apikey`, `oauth2`,
  `aws_sigv4`, `hmac`): mutate the outgoing request. `oauth2` `grant` is
  `client_credentials` (default), `password`, `refresh_token`, or
  `authorization_code`. Authorization Code uses a pre-obtained `refreshToken`
  in CLI/CI; interactive PKCE sign-in is a desktop GUI action with
  process-only sessions. Digest supports MD5/SHA-256 and sess variants, qop
  auth, and one challenge replay. `oauth2_client_credentials` is an alias of
  the client-credentials grant. `aws_sigv4` and `hmac` sign the finished
  request (after the body is final) and sign again on each retry. Omit
  `aws_sigv4` access-key fields to read `AWS_ACCESS_KEY_ID`,
  `AWS_SECRET_ACCESS_KEY`, and `AWS_SESSION_TOKEN`; there is no profile file.
- They compose: a step may stack `mtls` + `oauth2`.
- Auth is declared once under top-level `auth:` and referenced by name at
  environment/suite/case/step scope via `auth: { use: [name], inherit: bool }`.
- Inheritance: a scope with `inherit: true` (default) **adds** to the parent's
  providers; `inherit: false` **replaces** them. An empty `use` with
  `inherit: false` means "no auth here".

## GraphQL schemas

A project, suite, or step may set `schema: {file: schema.graphql}` or
`schema: {introspect: true}`. A closer scope replaces the parent. `file` stays
inside the project directory. `verity validate --env NAME` interpolates the
query, operation name, and variables with that environment before checking,
and reports operations that do not match the schema, with the step path and
`line N, column M of the query`. A missing non-null variable is reported on
`request.variables`. `graphql_schema` checks the response `data` against the
selection set (`Int` is 32-bit; an abstract value needs `__typename` or a
unique member match). `verity graphql schema <project> --step NAME` writes
the SDL (redacted) so CI can pin it and skip introspection. `verity graphql template <schema-or-url> --operation Query.user`
prints a query and a variables skeleton, with inline fragments for union and
interface fields. Introspection is cached per run, per URL and auth identity.
Do not log auth headers or the cache key; SDL and introspection errors go
through `core/secrets`.

## Don'ts

- Don't hardcode tokens, passwords, client secrets, or private keys in YAML.
- Don't rely on console text formatting; parse `--json` output.
- gRPC is the first-party plugin `plugins/grpc` (protocol `grpc`).
  Core keeps a stub that returns `plugin_error` until that plugin is loaded.
  Descriptors determine unary/server/client/bidi streaming. Use `message` for
  unary/server streams and `messages` for client/bidi streams. Streaming bodies
  are arrays; `header` assertions on `messageCount` and `truncated` check count
  and completeness. `stream` supports `maxMessages` (1000), `maxBytes` (16 MiB),
  `idleTimeoutMs` and `sendIntervalMs` (both 0/off). Server statuses are
  responses (`status` `0` is OK). Metadata is redacted by the host.
```
