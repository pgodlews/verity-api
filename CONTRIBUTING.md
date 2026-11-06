# Contributing to Verity

Verity is a personal project and portfolio piece maintained by Piotr Godlewski
(`@pgodlews`) in the `pgodlews/verity-api` GitHub repository. Bug reports,
documentation improvements, and focused code contributions are welcome.
The project is MIT licensed, copyright Piotr Godlewski; contributions are
provided under the same license.

Read the [code of conduct](CODE_OF_CONDUCT.md) before participating. Report
vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## Prerequisites

- Go 1.26 or newer for the core, CLI, GUI backend, and Go plugin modules.
- Git; desktop branch switching requires Git 2.23 or newer.
- Node.js 20.19+ (20.x) or 22.12+ and npm for `gui/frontend`.
- Wails v2 for desktop development (the release workflows use v2.12.0), with
  its platform dependencies: GTK and WebKitGTK development packages on Linux,
  WebView2 on Windows, and the macOS development tools on macOS. The Linux
  release build uses WebKitGTK 4.1 and the `webkit2_41` build tag.

The Go module is `github.com/pgodlews/verity-api`. The CLI is `verity`, built
from `./cmd/verity`, and the desktop binary is `verity-desktop`.

## Build and check

Run these commands from the repository root. Core and CLI checks are:

```bash
go build -o verity ./cmd/verity
./verity version --json
go vet ./...
go test ./...
go test -race ./...
```

The GUI and plugins are separate Go modules, so the root test command does
not cover them. Check the GUI backend and frontend with:

```bash
(cd gui && go test .)
(cd gui/frontend && npm ci && npm test && npx tsc --noEmit && npx vite build)
```

On Windows also run the GUI's patched WebView2 dependency tests:

```bash
(cd gui/third_party/go-webview2 && go test ./pkg/edge -run '^TestFocus')
```

This local dependency is separate from the four application/plugin modules; see
[its provenance and patch notes](gui/third_party/go-webview2/VERITY.md).

Build or develop the desktop app with Wails from `gui/`:

```bash
(cd gui && wails dev)
(cd gui && wails build -o verity-desktop)
# Linux with WebKitGTK 4.1:
(cd gui && wails build -tags webkit2_41 -o verity-desktop)
```

Check and build the first-party gRPC plugin in its own module:

```bash
(cd plugins/grpc && go vet ./... && go test ./... && go test -race ./...)
(cd plugins/grpc && go build -o verity-grpc .)
(cd plugins/example-assertion && go build -o example-assertion .)
```

The gRPC host integration tests also run with the root module's tests. Use
the [CI workflows](.github/workflows/ci.yml) and
[release workflow](.github/workflows/release.yml) for the automated build
configuration. Run the checks for each module and frontend area you change;
all required CI checks must pass before merge.

For project YAML/JSON changes, validate and check formatting with the CLI:

```bash
verity validate project.yaml --json
verity fmt --check project.yaml
verity scan-secrets project.yaml --json
```

An optional pre-commit hook checks staged secrets and formatting:

```bash
verity hooks install --fmt project.yaml
```

See [docs/git.md](docs/git.md) for hook behavior and project formatting.

For native release validation, use the [desktop test plans](docs/testing/README.md)
and record actual platform results separately from automated checks.

## Project rules

- Keep protocol, authentication, assertion, runner, and reporting behavior in
  `core/`. The CLI and GUI are thin shells; see
  [the architecture](docs/architecture.md).
- Every behavior change needs tests that demonstrate the behavior, including
  a regression test for a bug fix. Cover errors and redaction where relevant.
- Use structured `core/verr` error codes. Preserve stable machine-readable
  errors and test the code and relevant path, rather than console formatting.
- Route all Git integration through `core/vcs`; do not add shell-outs or
  independent Git implementations in the CLI or GUI.
- Never put plaintext secrets in project files, tests, or documentation.
  Use `${env:NAME}` references and supply test values at runtime; OS keychain
  references are also supported. Keep `.env.example` values empty and all
  output behind the shared `core/secrets` redactor.
- Do not hardcode hostnames, IP addresses, or personal filesystem paths in
  code, fixtures, or documentation. Use configuration, variables, temporary
  directories, and dynamically allocated test listeners.
- Format Go code with `gofmt`. Keep project file diffs stable with
  `verity fmt`, and update user documentation when behavior changes.

## Issues, commits, and pull requests

For a bug, include the version, a sanitized minimal reproduction, expected
behavior, and actual behavior. Discuss substantial features in a GitHub issue
before starting implementation so the scope fits the project.

Fork the repository, create a focused branch, and open a small pull request
against `main`. Describe the problem, resulting behavior, and checks you ran.
Link the relevant issue and call out compatibility changes. Keep unrelated
cleanup in a separate pull request and respond to review feedback.

Use Conventional Commits for commit messages and the pull request title,
for example `fix(secrets): redact errors before reporting` or
`docs: clarify plugin setup`. Use `!` or a `BREAKING CHANGE:` footer for
incompatible changes. Pull requests are squash merged after review and
passing CI; the pull request title becomes the squash commit subject.
Reviews are best effort from a single maintainer.

AI-assisted contributions are welcome under the same review and testing bar.
Contributors remain responsible for understanding, verifying, and explaining
their changes. [AGENTS.md](AGENTS.md) contains instructions for coding agents.
