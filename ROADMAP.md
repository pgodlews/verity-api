# Verity Roadmap

This roadmap tracks what is shipped, what is planned, and how we work. It is the
single source of truth for feature planning. Keep it updated as part of each
change (a PR that lands a roadmap item should tick its box).

Legend: `[x]` done · `[~]` in progress · `[ ]` planned

---

## 0. Current implementation (including unreleased work)

- [x] Run Windows Git subprocesses with hidden consoles and retain graceful cancellation.
- [x] Recover from Windows WebView2 focus failures without exiting; guard focus during startup/shutdown.
- [x] Correct Windows file URLs and make Git/workspace regression fixtures portable across platforms.
- [x] Core engine: model, registry, SPI interfaces, runner (goroutine
      parallelism, per-step retry and timeout), secrets/redaction, structured
      errors (`verr`).
- [x] Protocols: REST, SOAP, GraphQL (schema-aware); gRPC unary and streaming as `plugins/grpc`
      (core keeps a stub that points at the plugin).
- [x] Assertions: status, response_time, jsonpath, xpath, json_schema, xsd,
      contains, regex, header, graphql_schema.
- [x] Tier-1 auth: none, basic, bearer, apikey, mTLS, OAuth2 client-credentials;
      two-layer model with scope inheritance/override and stacking. OAuth2
      password and refresh-token grants ship on the same `oauth2` provider
      (see §2).
- [x] Reporters: console, JUnit, JSON, HTML.
- [x] Importers: OpenAPI, WSDL, Postman Collection v2.0/v2.1 (+ environments;
      see `docs/proposals/postman-import.md`), and SoapUI projects
      (see `docs/proposals/soapui-import.md`).
- [x] CLI (`verity`): run, validate, fmt, init, hooks, import, plugin new, list,
      schema, version, mock, load, scan-secrets, secret, graphql; structured JSON
      output and CI exit codes; `--env-file`, `--bail`,
      and `--watch`.
- [x] Out-of-process plugin protocol (VPP/1) + SDK + scaffolder + example plugin.
- [x] Wails desktop app: request editor, runner, imports, environments,
      load tests, and Git source control with stashes.
- [x] Supported frontend tooling with refreshed development dependencies and explicit webview targets.
- [x] Default Source control selection follows newly changed project files without overriding user choices.
- [x] Preserve desktop load reports until a new run returns a replacement.
- [x] Git status, selected commit scans, and hooks handle directory aliases consistently.
- [x] Desktop YAML saves preserve unrelated multiline values, comments, and markers.
- [x] Desktop release scenario plans for Windows 11/Linux and historical macOS evidence with privacy guidance.
- [x] JSON Schema (project + auth), Docker image, GitHub Action, AGENTS.md, docs.

---

## 1. Feature: SoapUI project importer

Convert a SoapUI OSS project (single XML file) into a Verity project. Positions
Verity as the clean-room successor that frees users from SoapUI's one-giant-XML
format. **First pass = SOAP + REST steps, property transfers, and the 8 most
common assertions.**

### 1.0 Legal & provenance guardrails (do these first, keep them true)

SoapUI OSS is **EUPL-licensed** (copyleft) and "SoapUI" is a SmartBear
trademark. The importer must be a **clean-room, read-only** converter.

- [x] **No SmartBear code/schema/jars** copied, vendored, or transcribed into
      Verity. Specifically: do **not** copy `config.xsd`, the generated
      `com.eviware.soapui.config.*` classes, or any SoapUI source.
- [x] Author our own Go structs from **observed XML** of real project files plus
      the **public API docs** (element/attribute names are facts, not
      copyrightable expression).
- [x] **Author our own test fixtures.** Do not commit SmartBear's sample
      projects (they are EUPL too). Hand-write minimal `.xml` samples.
- [x] **Trademark hygiene:** use "SoapUI" only nominatively ("imports SoapUI
      projects"). Never name the product/binary/package after it; never imply
      affiliation or endorsement.
- [x] Add a provenance header to `core/importers/soapui*.go` and a docs note:
      "Independent clean-room importer. Not affiliated with or endorsed by
      SmartBear. Reads SoapUI OSS (EUPL) project files; contains no SoapUI code."
- [x] Record this decision in `docs/proposals/soapui-import.md` (design + risk
      analysis) so the rationale is auditable.

### 1.1 Format model (clean-room Go types)

- [x] `core/importers/soapui_types.go` — Go structs with `encoding/xml` tags for
      the subset we read in the SoapUI project XML namespace:
  - [x] `<con:soapui-project>` (name, properties, interfaces, testSuites)
  - [x] `<con:interface>` (type: `wsdl` | `restservice`; endpoints)
  - [x] `<con:testSuite>` → `<con:testCase>` → `<con:testStep>`
  - [x] step types: `request` (SOAP), `restrequest`, `properties`, `transfer`
  - [x] `<con:assertion type="...">` and its config children
  - [x] project/suite/case `<con:properties><con:property>`
- [x] Keep it tolerant: unknown elements ignored, never fatal.

### 1.2 Structural mapping → `model.Project`

- [x] project name + `<con:properties>` → `Project` + a `default` environment
      (project properties become variables).
- [x] interface endpoints → environment variables (e.g. `ENDPOINT`,
      `ENDPOINT_1`), referenced via `${ENDPOINT}`.
- [x] `testSuite` → `Suite`; `testCase` → `TestCase`; `testStep` → `Step`.
- [x] SOAP `request` step → `Step{protocol: soap}` carrying the request envelope
      (`body`) + `soapAction` + endpoint.
- [x] `restrequest` step → `Step{protocol: rest}` with method/url/headers/body.
- [x] `properties` step → case/step `vars`.
- [x] `transfer` step → `transfers[]` on the source step (see 1.4).

### 1.3 Assertion mapping (8 common types, first pass)

Map these SoapUI assertion types; warn on anything else.

- [x] `Valid HTTP Status Codes` → `status` (op `eq`, or membership when a list).
- [x] `Invalid HTTP Status Codes` → `status` with `op: ne` (or warn if a set).
- [x] `Response SLA` → `response_time` (`op: lte`, ms).
- [x] `XPath Match` → `xpath` (expr + expected value).
- [x] `JsonPath Match` → `jsonpath` (expr + expected value).
- [x] `Contains` → `contains`.
- [x] `Not Contains` → `contains` with `op: not_exists`.
- [x] `Schema Compliance` → `xsd` (SOAP) / `json_schema` (REST) best-effort.
- [x] Anything else (Script/Groovy, JDBC, SensitiveInfo, etc.) → add a warning
      and, where it was the only assertion, keep the step but flag it.

### 1.4 Expression / property-reference translation

- [x] Rewrite SoapUI references to Verity `${VAR}`:
  - `${#Project#name}` → `${name}`
  - `${#TestCase#name}` → `${name}`
  - `${#TestSuite#name}` → `${name}`
  - `${#Global#name}` / `${#System#name}` / `${#Env#NAME}` → `${NAME}` (+ warn
    where semantics differ).
  - property-transfer source/target paths → `transfers[].from/expr` + a target
    var name.
- [x] Small, well-tested rewriter (`rewriteExpr`) with a unit test table.

### 1.5 Lossy-conversion report (honesty by default)

- [x] Rich `Result.Warnings` listing exactly what was not converted:
      Groovy/Script steps, JDBC/DataSource steps, MockServices, LoadTests,
      SecurityTests, JMS, unsupported assertions — with the SoapUI path
      (`suite/case/step`) for each.
- [x] Review follow-ups: decoded character data; secret scrubbing for mixed
      templates, endpoints, XML/JSON credential names, and assertion values;
      transfer-order and late-properties warnings; inherited REST parameters;
      warnings for scripts, settings, attachments, and unnamed entries; 50 MB
      input limit.
- [x] `--json` import output includes a structured `warnings` array (already
      supported by the import command shape).

### 1.6 CLI / GUI / wiring

- [x] `core/importers/soapui.go`: `func ImportSoapUI(path string) (*Result, error)`.
- [x] `cmd/verity/import.go`: add kind `soapui` (note: `soap` is already an alias for
      WSDL, so the SoapUI kind is **`soapui`** only).
- [x] Update CLI usage text + `verity` help.
- [x] GUI import: `case "soapui"` is in `core/workspace/importing.go` `runImport` (`app.go` `ImportSpec` no longer exists; `ImportPreview` calls `runImport`). `gui/frontend/src/views/ImportView.tsx` enables the SoapUI source.
- [x] `verity list`/catalog unaffected (importers aren't registry extensions),
      but mention in `--help`.

### 1.7 Tests (clean-room fixtures only)

- [x] `core/importers/soapui_test.go`:
  - [x] hand-authored minimal SOAP project → expected suites/cases/steps.
  - [x] hand-authored minimal REST project → expected protocol/url/method.
  - [x] each of the 8 assertion mappings.
  - [x] property-transfer step → `transfers[]`.
  - [x] expression rewriter table test.
  - [x] warnings emitted for a Groovy/script step.
  - [x] generated projects pass `model.Validate()` (parity with WSDL/OpenAPI
        importer tests).
- [x] `examples/specs/sample.soapui.xml` (our own authorship) for docs/CLI demo.

### 1.8 Docs

- [x] `docs/proposals/soapui-import.md` — design + legal/risk analysis.
- [x] `docs/cookbook/import-soapui.md` — runnable walkthrough + mapping table +
      "what doesn't convert" list.
- [x] `docs/project-format.md` — add SoapUI import to the importer section.
- [x] `README.md` + `AGENTS.md` — mention `verity import soapui`.

### 1.9 Acceptance criteria

- [x] `verity import soapui sample.soapui.xml --out p.yaml` produces a project
      that `verity validate` accepts.
- [x] SOAP and REST steps run against a local mock (extend `local-demo` or add a
      fixture) and assertions evaluate.
- [x] Unsupported constructs appear as warnings, never silent drops.
- [x] No SmartBear code/fixtures in the tree; trademark used nominatively;
      provenance note present.

---

## 1b. Postman importer follow-ups

- [x] Multipart `formdata` / `file` bodies in the REST protocol. `formdata`
      imports as `multipart` (text parts and file parts from `src`; disabled
      parts omitted; a file part with no `src` warns, and the step is
      `skip: true` only when nothing usable remains). `file` imports as
      `bodyFile` when `src` is set, otherwise `skip: true` plus a warning.
- [x] Postman dynamic variables (`{{$guid}}`, `{{$timestamp}}`,
      `{{$randomInt}}`) as built-in interpolation functions (`${fn:…}`).
- [x] Run-scoped transfers (`scope: run` on a transfer) so a login folder can
      feed later folders, as Postman's global variable scope does. The Postman
      and SoapUI importers set it where a later case uses the value.
- [x] Saved example responses → mock-service fixtures (pairs with §5 mocks).
      `item.response[]` becomes top-level `mocks:`. A path segment that is
      only a variable becomes `{name}`. Literal credentials in example
      bodies and headers are replaced with the placeholder `REDACTED` and
      reported as import warnings. See `docs/mocks.md`.

## 2. Tier-2 auth (fast follow, same interface)

- [x] OAuth 2.0 Refresh-token grant (`oauth2` `grant: refresh_token`).
- [x] OAuth 2.0 Password grant (`oauth2` `grant: password`).
- [x] Authorization Code + PKCE (OAuth endpoints from OIDC providers) — interactive browser flow in the GUI;
      CLI/CI consumes a pre-obtained refresh token (no scripted interactive
      login).
- [x] Digest auth (MD5/SHA-256 and sess variants; auth-int unsupported).
- [x] Tests per provider against local servers; redaction parity.
      Includes workspace sessions and importer/UI coverage.

## 3. Tier-3 auth

- [x] AWS SigV4 (`aws_sigv4`) signs the finished request (S3 single-encoding,
      other services double-encoding, session token, unsigned payload).
      Built in. Credential fields may be omitted in favor of
      `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN`.
      No shared-credentials file or profile.
- [x] Generic HMAC (`hmac`): sha1/sha256/sha512, hex or base64, optional
      prefix, timestamp, and nonce. The default message is the raw body.
- [ ] NTLM / Kerberos (SPNEGO).
- [ ] Custom pre-request hook.
- [ ] (Deferred) SOAP/WS-Security — documented as future work only.

## 4. Protocols

- [x] gRPC: real unary support via reflection or a descriptor set, shipped as
      the first-party plugin `plugins/grpc` (core stays free of grpc-go; the
      stub points at the plugin). See `docs/proposals/grpc.md`.
- [x] gRPC server/client/bidirectional streaming in `plugins/grpc`: ordered
      JSON arrays, message/byte limits, receive idle timeout and cancellation.
- [x] GraphQL: schema-aware assertions / introspection-driven templates.
      SDL file or introspection (`schema.file` / `schema.introspect`),
      `verity validate --env` (interpolates, then checks operations and
      required variables), `graphql_schema` (32-bit `Int`, abstract types
      need `__typename` or one matching member), and `verity graphql template`
      (union and interface fragments). Introspection is cached per URL and
      auth identity. See `docs/project-format.md`. Pre-merge review follow-ups
      for this item are done.

## 5. Phase-2 capabilities

- [x] Mock services (stand up SOAP/REST mocks from WSDL/OpenAPI).
      `verity mock` serves OpenAPI 3, WSDL 1.1 (SOAP 1.1 and 1.2), or a
      project's `mocks:` section, including a mocks-only file
      (`core/mock`, `cmd/verity/mock.go`). See `docs/mocks.md`.
- [x] Load testing (lean on goroutines; reuse the runner). `verity load`
      and the desktop **Load test** view run the selected cases concurrently
      through `core/runner` and check latency and error-rate thresholds, with
      `--env-file` / `--env-file-override` / `--var` / `--timeout` parity,
      live stats (`load:live`), Cancel, per-step latency tables, and JSON
      report export. Connect-time dial guard (redirects, feeds, proxies,
      plugins) and a zero-request threshold failure are in `docs/load.md`.
- [x] [Scripting design note](docs/proposals/scripting.md): engine comparison,
      lifecycle, and isolation proposal. No scripting implementation ships.
- [ ] Implement embedded scripting; proposed engine is goja (JavaScript).
- [x] OS keychain integration for secret storage: `keychain` provider,
      `verity secret set|get|rm|list`, desktop store-and-rewrite action and
      in-memory mock. Portable listing is explicitly unsupported (exit 3).
- [ ] Full WS-Security.

## 6. Quality & DX

- [x] Ctrl-C writes a partial report (exit 5) and the GUI streams run events with Cancel.
- [x] Desktop request editor flags plaintext credentials on a dirty step
      (`CheckStep`) and keeps Save disabled until each one is stored as
      `${env:NAME}` or `${secret:keychain:NAME}` (same refusal as `SaveStep`).
- [x] Expand behavior-focused coverage across CLI, watchfs, GraphQL, plugin
      host/SDK, assertions, datasource, mock, model, runner, and secrets.
- [ ] Raise measured core coverage toward 80%; added tests alone do not
      establish the target, and a fresh coverage run is required.
- [x] Golden-file tests for reporter outputs (console/JUnit/JSON/HTML byte-shape),
      with LF checkout enforced for fixtures.
- [x] Desktop JSON bodies and GraphQL variables preserve numeric literals and
      YAML merge semantics through load, editing, formatting, and save; response
      assertions use clone-safe values and strict JSON parsing.
- [x] Digest response hashes use quoted-string-aware masking plus bounded
      attempt-scoped echo redaction.
- [x] Raw URL query bytes are normalized before parsing for the wire and HTTP signers.
- [x] `verity fmt` to normalize/canonicalize project YAML/JSON for clean diffs;
      `verity init` and Git pre-commit hook management ship alongside it.
- [x] Read-only git snapshot in the desktop app: branch, ahead/behind from
      local refs, per-request changes versus HEAD (project metadata, suite and
      case fields, a comment-only "file changed" row, and a fallback modified
      project row), and reload when files change outside Verity. Saves refuse
      with `conflict` when the loaded step hash differs, or when a step has
      no hash and the file hash does not match. `core.fsmonitor` is
      forced off, and read commands blank repository-local clean/smudge
      filters (global filters such as git-lfs stay). `LC_ALL` is `C.UTF-8`
      when that locale exists.
- [x] Git write actions from the desktop app: stage the files you pick, commit
      only those scanned blobs (temporary index, then hooks, no `--no-verify`;
      scan refusal, hook failure, and cancel restore the previous index,
      including unmerged stages), fetch and push with an explicit refspec
      taken from `branch.<name>.remote` and `branch.<name>.merge`, pull
      (fast-forward, then merge only when git refuses the fast-forward, no
      editor), and branch switch/create. Conflicts are resolved in your
      editor. Verity does not store git credentials.
- [x] Desktop stashes: list immutable ids and metadata; stash all tracked or
      selected files, optionally with non-ignored untracked files; redacted
      semantic diff; apply/pop/drop with stale-id checks and pop/drop
      confirmation. Conflicts keep the stash and use the editor flow. Stash
      rewrites preserve draft save baselines and trigger the existing reload
      banner. Stash review regressions cover staged selection and repository
      programs.
- [x] GitHub tag release workflow: CLI/plugin archives, checksums, Linux/macOS/Windows
      desktop apps, multi-arch container publication, and Homebrew tap updates
      (see [releasing](docs/releasing.md)).

---

## 7. Milestones

| Milestone | Contents | Exit criteria |
|-----------|----------|---------------|
| **M1 — SoapUI import (8 assertions)** | §1.0–1.9, done | clean-room importer, tests, docs, and provenance note |
| **M2 — Tier-2 auth** | §2 | refresh/password/PKCE/digest with tests |
| **M3 — gRPC unary** | §4 (unary) | done: `plugins/grpc` + `examples/grpc` |
| **M4 — Mocks & load** | §5 (mocks, load) | done: `verity mock` and `verity load` |

---

## 8. Development workflow

Development takes place on GitHub in `pgodlews/verity-api`.

- Create a focused branch per change and open a PR against `main`.
- Use Conventional Commits for commit messages and PR titles.
- Describe behavior and validation in the PR; review and green CI are required
  before squash merge. Update this roadmap when a feature lands.
- Push `v*` tags to trigger `.github/workflows/release.yml`. Pre-release tags
  contain a `-` suffix and skip Homebrew updates and the image's `:latest` tag.
  See [releasing](docs/releasing.md) for assets and configuration.
