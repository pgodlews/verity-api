# Changelog

All notable changes to Verity are documented here, following the Keep a
Changelog format. Entries are ordered newest first. Verity is alpha software;
breaking changes may occur during the `0.x` series.

Public history starts with this first public alpha release. Earlier versions
were developed privately; their notes are kept here for reference. Commands
and distribution names below use the final public names.

## [Unreleased]

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
