# Changelog

All notable changes to Verity are documented here, following the Keep a
Changelog format. Entries are ordered newest first. Verity is alpha software;
breaking changes may occur during the `0.x` series.

Public history starts with this first public alpha release. Earlier versions
were developed privately; their notes are kept here for reference. Commands
and distribution names below use the final public names.

## [Unreleased]

### Security

- Build with a patched Go standard library and dependencies: every module
  now declares `toolchain go1.26.11` (the `go` command fetches it when an
  older 1.26 patch is installed), and `golang.org/x/net` (v0.62.0),
  `x/crypto`, `x/sys`, and `x/text` are bumped and aligned across the root,
  desktop, and plugin modules. `govulncheck` reported reachable `net/http`,
  `crypto/tls`, `crypto/x509`, `html/template`, `mime/multipart`, `net/url`,
  and `x/net` issues before; it now reports none reachable. Dependabot keeps
  the Go modules, npm packages, and GitHub Actions up to date.
- Apply Git project-file recognition and commit secret checks consistently through
  directory symlink aliases; refuse commits when the project file list cannot be read.

### Added

- `verity validate` warns (`no_assertions`, with the step's path) about
  steps that have no assertions: they pass on any response, including an
  HTTP 500. Warnings do not change the exit code.
- Run summaries add explicitly named counts: `assertionsPassed`,
  `assertionsFailed`, `casesPassed`, `casesFailed`, `casesErrored`,
  `casesSkipped`, and `casesAborted`. The existing `passed`/`failed` (which
  count assertions) and `errors`/`skipped` (which count steps) are unchanged.
  Case counts include cases with no steps, such as cases `--bail` never
  started, which no existing field counted.
- `verity run --reporter TYPE=FILE` (repeatable, also with `--watch`) writes
  a report with any registered reporter, so `reporter` plugins
  (`verity plugin new reporter`, `sdk.ServeReporter`) can finally be used.
- Desktop release testing plans for Windows 11 and Linux, a shared scenario
  checklist, and historical macOS findings with explicit coverage limits.

### Changed

- **BREAKING:** the composite action's `vars` input takes one `KEY=VALUE`
  per line (values may contain spaces); several space-separated pairs on one
  line are now a single value. Inputs reach the script through environment
  variables, so quotes or `$(...)` in an input can no longer break or inject
  into the shell. New inputs: `suite`, `case`, `bail`, `env-file`, and
  `trust-plugins`.
- `verity-grpc --version` prints the plugin's version, commit, and build
  date; release archives now stamp them like the CLI.
- Release workflow: a test job must pass before anything is built; the
  container image (and `:latest`) is pushed only after the GitHub release
  exists; each job gets only the token permissions it needs; a re-run
  replaces the assets of an existing release; desktop builds install
  frontend dependencies with `npm ci`.
- The frontend now requires Node.js 22.12+ (`engines`), and CI and release
  builds use Node.js 22: Node.js 20 reached end of life in April 2026.
- Removed `verity secret list`: the portable keychain backend cannot list
  names, so the command could only ever fail. `verity secret list` is now a
  usage error (exit 2).
- **BREAKING:** a plugin can no longer silently replace a built-in or
  already-loaded extension type (for example `rest`, `bearer`, `json`); add
  `"override": true` to its plugins entry to allow it. The first-party gRPC
  plugin still replaces the core gRPC stub without the flag.
- **BREAKING:** duplicate case names within a suite are a validation error.
  The name is the case's identity, so a desktop save could overwrite the
  wrong case and `--case`, watch deltas, and run events were ambiguous.
  OpenAPI, WSDL, and Postman imports rename duplicates to `name (2)`.
- **BREAKING:** a failed or errored setup step now skips the suite's cases;
  each is reported `skipped` with a `setup` step carrying the new
  `setup_failed` error code. Before, cases ran anyway (unless `--bail`)
  against missing preconditions. Teardown still runs.
- **BREAKING:** `steps[].vars` are now local to their step, as documented,
  and are applied in sorted name order. Before, they were set on the case
  and leaked into every later step, in random map order. Use case `vars` or a
  transfer to share a value with later steps. The SoapUI importer repeats a
  properties step's values on each later step so imported projects keep
  their behaviour.
- **BREAKING:** loading a project or fragment rejects keys the format does
  not define with a `validation_error` naming the key and its path. A typo
  such as `asertions:` or `retrys:` was silently ignored, so the step ran
  without its checks and passed. Projects with stray keys must remove them.
- **BREAKING:** every file a project names at run time is confined to the
  project directory (after resolving symlinks) unless the run uses
  `--allow-external-files`: `${secret:file:PATH}`, mTLS `certFile`/`keyFile`/
  `caFile`, `json_schema` schema files, and data-feed paths, joining uploads.
  `${secret:file:...}` relative paths now resolve against the project
  directory instead of the working directory. CI jobs that read mounted
  secrets such as `${secret:file:/run/secrets/token}` must add
  `--allow-external-files` (or switch to `${env:NAME}`). Uploads from a
  project without a known directory are refused instead of unchecked.
  Previously `${secret:file:/home/user/.ssh/id_rsa}` in a header sent that
  file to the project's server.
- **BREAKING:** a property transfer whose `jsonpath`, `xpath`, `header`, or
  `regex` source matches nothing now fails the step unless it has a non-empty
  `default`. Previously it captured an empty string and later steps silently
  sent empty values. Captured JSON numbers keep their exact text (a 64-bit id
  `9007199254740993` was captured as `9007199254740992`), and objects or
  arrays are captured as compact JSON instead of Go formatting.
- **BREAKING:** a project's own `.verity/plugins.json` no longer runs
  automatically. `verity run`, `run --watch`, and `verity load` exit 3 with a
  `config_error` naming the file, without starting any plugin, unless you pass
  `--trust-plugins` or `--plugins FILE`, or set `VERITY_TRUST_PLUGINS=1` (CI).
  The desktop app asks once per plugins file and remembers the answer outside
  the project until the file changes. Cloning or opening a project can no
  longer execute programs it ships.
- Upgrade frontend development tooling to Vite 7 and refresh audited dependencies.
  Building the frontend now requires Node.js 20.19+ (20.x) or 22.12+.

### Fixed

- Desktop runs, load tests, and load-target checks report every project
  validation error, not only the first, and the desktop `ValidateProject`
  result adds `issues` (each with `code`, `message`, and `path`) alongside
  the existing `errors` strings.
- OpenAPI, WSDL, and Postman imports (collections and environments) refuse
  input over 50 MiB with an `import_error`, as SoapUI imports already did,
  so a huge file cannot exhaust memory in the CLI or the desktop preview.
- The desktop footer lists the protocols the app actually supports (from the
  extension catalog, including gRPC) instead of a fixed "REST · SOAP ·
  GraphQL".
- Closing the desktop window cancels an in-flight run, load test, or Git
  operation instead of only stopping the file watcher.
- Desktop release builds carry their version, commit, and date (a new
  `Version` binding, the Windows file version, and the macOS bundle
  version), and the Wails project is named `Verity`, so the macOS bundle no
  longer has to be renamed after the build.
- `verity load` treats IPv6 unique-local addresses (`fc00::/7`) as private
  targets, like RFC 1918 IPv4, instead of requiring `--allow-remote` for them.
- Project loading, auth configuration, data feeds, and secret files return
  structured errors with codes (`validation_error` for unparsable or
  invalid project files, `config_error` for unreadable files and bad auth or
  feed settings, `not_found` for an unknown environment or auth definition,
  `auth_error` when applying auth fails) instead of uncoded errors, so JSON
  output and the desktop app can classify them. Messages are unchanged.
- `verity hooks install` for a second project in the same repository adds it
  to the pre-commit hook instead of replacing the first project, and
  `hooks uninstall` removes only that project. Hook paths are resolved
  through Verity's Git layer (`core/vcs`).
- `verity fmt`, imports (`--out`), `graphql schema --out`, report files
  (`--junit`, `--json`, `--html`), and desktop load-report saves write
  atomically (temporary file + rename), keep an existing file's permissions,
  and write through a symlink instead of replacing it.
- An `xsd` assertion with `expr`, `config.schemaFile`, or `config.schema` is
  a validation error: the assertion only checks well-formedness (and
  `config.rootElement`) and would silently ignore the schema.
- `verity version` reports the module version, commit, and commit date that
  the Go toolchain records when the binary was built without release
  `-ldflags` (for example `go install github.com/pgodlews/verity-api/cmd/verity@v0.6.0`),
  instead of `dev`, `none`, and `unknown`.
- Release archives are reproducible: zip archives use fixed timestamps,
  modes, and sorted entries, tar.gz archives sorted entries and no gzip time
  stamp. `scripts/release.sh` stops with a clear message when GNU tar,
  `sha256sum`, or python3 is missing (macOS) instead of failing midway. The
  image no longer gets an empty `org.opencontainers.image.source` label when
  `IMAGE_SOURCE` is unset, and the `docker run` examples pass `--user` so
  reports can be written to the mounted directory.
- JSON project saves (desktop step and environment saves, imports) and
  `verity fmt` no longer HTML-escape `<`, `>`, and `&`, so SOAP/XML bodies
  and URLs stay readable instead of becoming `\u003c…`. Desktop saves to a
  symlinked project file now write the link's target instead of replacing
  the link with a regular file.
- OAuth2: an invalid `refreshSkewSeconds` (not an integer, negative, or
  over a day) is a `config_error` instead of being ignored silently.
- mTLS: an unknown `minVersion`/`maxVersion` (or a minimum above the
  maximum) is a `config_error` instead of silently using Go's default.
- `${fn:randomHex:N}` is limited to N <= 4096; a larger N is a
  `config_error` instead of an unbounded allocation.
- `verity` usage lists the `run` flags `--json-stdout`, `--quiet`,
  `--timeout`, and `--follow-redirects`, and `fmt --staged`/`--json`; the
  package comment no longer claims every command supports `--json`.
- `verity run` rejects `--parallel` below 1 and a negative `--until-step`
  with a usage error (exit 2) instead of accepting them.
- `verity import --out FILE` no longer overwrites an existing file unless
  the new `--force` is given (the desktop import already refused), and a
  failure to save the project exits 3 instead of 4.
- A request or response body that is not valid base64 on the plugin wire is
  a `plugin_error` (host and SDK) instead of being dropped silently.
- Plugin SDK: `ServeAuth` keeps one provider per distinct config for the
  plugin process instead of rebuilding it (and losing any cached token) on
  every request.
- Plugin SDK: `AssertionContext` and `ProtocolContext` carry a working
  (plugin-local) redactor instead of nil, so plugin code that calls
  `Redactor.Redact` like a built-in no longer panics.
- Plugin host: a plugin that fails its handshake is killed and reaped (no
  zombie process or leaked pipes), and a response whose `id` does not match
  the request stops that plugin with a `plugin_error` instead of being used
  as the answer to the wrong call.
- Steps of one setup or teardown hook share a variable scope, so a token a
  setup step captures reaches the next setup step; before, each hook step ran
  with a fresh resolver and case-scoped captures were lost.
- YAML projects keep exact JSON number literals in setup and teardown step
  `json`/`variables` and in mock `json` bodies, as they already did for case
  steps.
- A response body over 32 MiB is no longer cut silently: the response is
  marked truncated and body assertions fail with a clear message instead of
  running on a partial body. `response_time` is documented as time to
  response headers (including a 401 challenge replay).
- Request captures in reports and the desktop app show the URL that was sent,
  including the step's `query:` map and auth-added query parameters such as
  an API key (redacted), and a new `request.cookies` map (additive; values
  masked) lists the cookies sent.
- A data feed with no rows now reports its case as `skipped` (with a `feed`
  step explaining why) instead of running it once with literal `${column}`
  placeholders. CSV feeds strip a UTF-8 byte-order mark (Excel exports) and
  reject duplicate column names.
- Assertions no longer ignore `op`: `contains` supports `contains` and the
  new `not_contains` (`not_exists` remains a deprecated alias), `regex`
  supports `matches` and the new `not_matches`, and any other op fails the
  assertion instead of silently running a positive match. `verity validate`
  rejects unknown assertion ops and transfer `from` values.
- JSONPath "not found" no longer depends on the library's error wording: a
  path that compiles but cannot be followed through the response (missing
  key, out-of-range index such as `$.items[5]`) is absent, so `op:
  not_exists` passes and a transfer falls back to its default, while a
  JSONPath syntax error is still an error. Assertions and transfers share
  one implementation.
- REST/GraphQL request checks: setting more than one of `json`, `form`,
  `body`, `multipart`, `bodyFile` is a `config_error` (before, `json`
  silently won); a non-map `form`, `query`, or `headers` and an empty `url`
  (or empty GraphQL `query`) are rejected instead of sending nothing; and
  multipart validation no longer modifies the project's request map.
- SOAP: a generated SOAP 1.2 envelope uses the 1.2 namespace
  (`http://www.w3.org/2003/05/soap-envelope`) instead of 1.1's, SOAP 1.1
  always sends `SOAPAction` (`""` when unset, as the spec requires), and a
  `version` other than `1.1`/`1.2` is a `config_error` instead of silently
  meaning 1.1.
- `verity plugin new` validates the plugin name (`^[a-z][a-z0-9_-]*$`, so no
  `../` paths), generates valid Go identifiers (`my-plugin` becomes
  `MyPlugin`, not `My-Plugin`), refuses to overwrite a non-empty directory
  without the new `--force`, and accepts `--json`.
- The gRPC plugin binary is `verity-grpc` everywhere: the plugin README,
  build comment, and `examples/grpc` (whose `.verity/plugins.json` now loads
  `plugins/grpc/verity-grpc`) matched neither the release archives nor
  `AGENTS.md`. Built plugin and fixture binaries are git-ignored.
- `verity run` reuses one keep-alive HTTP transport for steps without a TLS
  config instead of building a new transport for every attempt, so
  `response_time` no longer includes a fresh TCP/TLS handshake per step and
  idle connections are closed when the run ends.
- An auth config that still contains a `${...}` reference after resolution
  (for example a bearer `token: ${accessToken}` captured by a login step)
  now fails the step with `auth_error` naming the auth definition and the
  reference, instead of sending `Authorization: Bearer ${accessToken}`.
- Header, query, and cookie values added by an auth plugin are now
  registered for redaction, and plugin stderr (previously discarded) is
  shown on the host's stderr as `[plugin NAME] ...` lines, redacted with the
  run's redactor.
- `verity load` without `--allow-remote` ignores `HTTP_PROXY`/`HTTPS_PROXY`,
  so a loopback or private proxy can no longer carry a redirect or captured
  host to a public address past the private-address guard. Embedding code
  that passes a `load.Options.Transport` with its own dialer (or a
  non-`*http.Transport`) now gets a `config_error` instead of an unguarded run.
- A plain `${NAME}` that falls back to the process environment is now
  redacted like `${env:NAME}`; before, a forgotten `env:` prefix printed the
  value in console, JSON, JUnit, and HTML reports and in the desktop app.
  `verity validate` adds a `warnings` array (additive) and reports such
  references as `env_fallback`.
- Suite teardown no longer runs under a shared 10-second deadline in every
  run: teardown steps use the normal `--timeout`/`timeoutMs`/retry limits and
  Ctrl-C interrupts them. The 10-second grace budget applies only to the
  teardown that remains after a run is cancelled.
- Assertion `eq`/`ne` no longer turn two strings into floats (`"1.10"` was
  equal to `"1.1"`, `"007"` to `"7"`), and numbers compare exactly instead of
  through float64, so large JSON integers such as `9007199254740993` are no
  longer equal to their rounded neighbour. A string compared with a real
  number still compares numerically. This can turn previously passing
  string-to-string checks into failures.
- A `null` configuration value (for example `token:` with no value) no longer
  becomes the text `<nil>`: auth fields count as missing (`bearer auth
  requires 'token'`), null headers are omitted, null SOAP parameters are empty,
  and JSON data-feed nulls are empty strings. Nested JSON feed objects and
  arrays become compact JSON text instead of Go formatting.
- The frontend development bridge (`npm run dev`) refuses cross-site
  requests: it needs a loopback `Host`, the `X-Verity-Bridge: 1` header, and
  a same-origin `Origin`, so another page in the browser can no longer save,
  commit, push, or run through it. The dev server binds to `localhost`.
- Avoid opening Windows console windows for Git subprocesses during desktop operations.
- Let Windows Git clean up staging locks on cancellation by sending CTRL_C
  in an isolated hidden console instead of CTRL_BREAK.
- Keep the Windows desktop open when a WebView2 focus request fails, including
  during window switching and native dialogs; log only the numeric error code.
- Generate valid Windows file URLs for Source control's system file opener.
- Select newly changed project files in Source control while preserving explicit checkbox choices.
- Keep the previous desktop load report when validation or starting a new run fails.
- Preserve unrelated folded YAML scalar values when saving requests and environments.

## [0.6.0-alpha.1] - 2026-11-02

### Added

- Server, client, and bidirectional streaming in the first-party gRPC plugin,
  with message and byte limits, idle timeouts, send intervals, and captured
  message counts and truncation status.
- OS keychain secrets through `${secret:keychain:NAME}` and
  `${secret:keychain:project/NAME}`. Manage them with `verity secret set`,
  `verity secret get`, and `verity secret rm`, or store a credential from the
  desktop editor. CLI writes read from stdin and reads are masked by default.
- Git stashes in desktop Source control: save tracked or selected files,
  optionally include untracked files, inspect redacted diffs, and apply, pop,
  or drop a stash. Conflicting applies retain the stash; unsaved editor drafts
  keep their reload and save-conflict protection.
- A desktop **Load test** view for configuring and running load tests.
- `verity load --env-file`, `--var`, and `--timeout` to load environment values,
  override project variables, and set the request timeout for load tests.

### Changed

- **BREAKING:** renamed the CLI from `apitest` to `verity`, built from
  `./cmd/verity`. Update scripts and CI commands. After installing `verity`,
  reinstall an existing pre-commit hook from the repository root with
  `verity hooks install --fmt project.yaml` (substitute your project's
  repository-relative path). If the hook did not check formatting, use
  `verity hooks install project.yaml` instead.
- **BREAKING:** the Go module path is now `github.com/pgodlews/verity-api`.
  Update Go imports and module requirements, including GUI and plugin
  references to the core module.
- Public distribution names are `verity-desktop` for the desktop binary,
  `ghcr.io/pgodlews/verity` for the container image, and `pgodlews/verity` for
  the Homebrew tap (`brew install pgodlews/verity/verity`).

### Fixed

- Preserve JSON numbers without losing precision when loading, editing, and
  saving project data, including large integer values.
- Escape URL query names and values correctly so reserved characters in
  variables do not change the meaning of the request.
- Redact Digest authentication details consistently in captured output.

### Security

- Fixed disclosure of values resolved from `${env:NAME}` and
  `${secret:provider:key}` in JSON reports and `verity load` output when
  request construction or target validation failed. Secrets are registered
  for redaction before those errors are produced; the problem affected 0.5.1.
- Fixed execution of repository-local Git merge drivers during pull with
  merge and stash apply. Verity replaces repository-local and worktree merge
  drivers with a fixed text merge command, and disables hooks for stash
  operations. The pull exposure existed from 0.4.0.

## [0.5.1] - 2026-11-01

### Added

- AWS Signature Version 4 (`aws_sigv4`) and generic HMAC (`hmac`) request
  signing. Requests are signed after the body is prepared and again on retry;
  GraphQL introspection is signed for its own body.

### Changed

- Signed requests return their redirect response instead of following it.

### Security

- Auth-added headers, including API keys, bearer tokens, and session tokens,
  are removed when a redirect leaves the original origin (scheme, host, or
  port). Version 0.5.0 forwarded them to the redirect destination. Upgrade if
  an API you test can redirect to a different origin.

## [0.5.0] - 2026-10-31

### Added

- `verity mock` serves mock APIs from OpenAPI 3, WSDL with SOAP 1.1/1.2, or a
  project's `mocks:` section. Postman saved responses import as mock examples
  with credentials scrubbed.
- `verity load` runs cases across virtual users with latency and error-rate
  thresholds for CI. Targets default to loopback, private, or link-local
  destinations, checked on every connection; `--allow-remote` opts out.
- Schema-aware GraphQL operation and variable validation from SDL files or
  introspection, the `graphql_schema` response assertion, query templates,
  and **Insert from schema** in the desktop app.

### Security

- Secret scanning also detects well-known token patterns inside free text.

## [0.4.0] - 2026-10-31

### Added

- Desktop Git branch status and per-request changes against the last commit.
- Reload on external file changes, with conflict-safe saves and protection
  for unsaved editor drafts.
- Desktop Source control for selected-file commits with secret scanning,
  fetch, fast-forward pull, push, and branch management. Git uses the
  installed CLI, never force-pushes or stores credentials, and leaves merge
  conflict resolution to your editor.

## [0.3.0] - 2026-10-31

### Added

- `verity scan-secrets`, including `--staged`, and a pre-commit hook installed
  with `verity hooks install`.
- `verity init` for a starter project, `.gitignore`, and `.env.example`.
- `verity fmt` and `verity fmt --check` for stable project formatting.

### Security

- The desktop editor refuses to save plaintext credentials and offers
  replacement with `${env:NAME}` references.

## [0.2.0] - 2026-10-31

### Added

- Run-scoped captures and `verity run --watch` for automatic reruns.
- Partial reports on interruption and live desktop runs with Cancel.
- Digest authentication and OAuth2 Authorization Code with PKCE sign-in.
- A first-party unary gRPC plugin in its own module.
- Desktop timeout and retry settings, a Windows desktop app, a container
  image, and a Homebrew tap.

## [0.1.0] - 2026-10-31

### Added

- First private release of the shared core, CLI, and desktop API testing tool.
- A redesigned desktop workspace with a request editor, Send that runs
  earlier steps to populate captures, comment-preserving YAML saves, expected
  and actual results, rerun failed tests, an import wizard, and editable
  environments with secret status.
- Postman collections and environments, SoapUI projects, OpenAPI, and WSDL
  importers. Imported secrets become `${env:NAME}` references; unconverted
  items produce warnings.
- Suite and case filters, `--until-step`, `--bail`, `--env-file`, per-step
  retry and timeout, and multipart and file uploads confined to the project
  directory.
- OAuth2 client-credentials, password, and refresh-token grants, plus dynamic
  UUID, timestamp, ISO timestamp, random integer, and random hexadecimal values.
- Reproducible release archives with `SHA256SUMS`.

### Fixed

- Deterministic case ordering in parallel reports, duration units, data-feed
  number handling, header and cookie serialization, form encoding, nested
  interpolation, and assertion edge cases.
- Import JSON output, GUI validation consistency, recursive includes,
  environment auth inheritance, plugin RPC timeouts, and run-scoped OAuth
  token caching.
