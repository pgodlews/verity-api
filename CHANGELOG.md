# Changelog

All notable changes to Verity are documented here, following the Keep a
Changelog format. Entries are ordered newest first. Verity is alpha software;
breaking changes may occur during the `0.x` series.

Public history starts with this first public alpha release. Earlier versions
were developed privately; their notes are kept here for reference. Commands
and distribution names below use the final public names.

## [Unreleased]

### Added

- Desktop release testing plans for Windows 11 and Linux, a shared scenario
  checklist, and historical macOS findings with explicit coverage limits.

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

- Preserve JSON numbers without losing precision when loading, editing, and
  saving project data, including large integer values.
- Escape URL query names and values correctly so reserved characters in
  variables do not change the meaning of the request.
- Redact Digest authentication details consistently in captured output.

### Security

- Apply Git project-file recognition and commit secret checks consistently through
  directory symlink aliases; refuse commits when the project file list cannot be read.

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
