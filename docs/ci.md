# CI/CD integration

Verity's CLI is designed for CI from day one: correct exit codes, structured
JSON, JUnit XML, environment/variable injection, and configurable parallelism.

## Exit codes

| Code | Meaning |
|------|---------|
| `0`  | success — all tests passed (or skipped) |
| `1`  | test failure, or `fmt --check` found files that would change |
| `2`  | usage error — bad command line |
| `3`  | validation/config error — project failed to load or validate |
| `4`  | internal error |
| `5`  | interrupted — SIGINT or SIGTERM cancelled the run |

A run with any failed assertion or transport error exits non-zero, so CI fails
the job automatically. An interrupted run exits `5` rather than `1`, including
when a step that had already started failed because of the cancel, or when an
earlier step had failed an assertion. The partial report is the record of
those steps. `5` is still non-zero, so the job fails.

## Mock server in CI

Start `verity mock` in the job (it binds loopback and exits 0 on SIGTERM)
and point the project at the printed URL with `--var BASE_URL=...`. OpenAPI
and WSDL inputs need no project `mocks` section. See [mocks.md](mocks.md).

## Typical CI command

```bash
API_TOKEN="$CI_API_TOKEN" \
verity run project.yaml \
  --env staging \
  --parallel 4 \
  --junit reports/junit.xml \
  --json   reports/result.json
```

- `--env` selects the environment; `--var KEY=VALUE` (repeatable) overrides or
  adds variables.
- `--junit` emits JUnit XML for the CI test reporter.
- `--json` emits the full structured result for archival/inspection.
- `--html FILE` additionally writes a self-contained HTML report artifact.
- `--json-stdout` prints the JSON result to stdout (for piping to other tools).
- `--bail` stops scheduling new cases after the first failed or errored case.
  Cases already running finish; cases that never started are reported as
  `skipped`, and `summary.bailed` is true. The process still exits `1`.
  A step that passes after its own retries is a pass and does not trip bail.
- Ctrl-C (SIGINT) or SIGTERM cancels the run. The step that is already in
  flight finishes or fails on that context. Steps, feed rows, and cases that
  never started are reported as `aborted`, in project order, and
  `summary.aborted` is true. Setup of a suite that never started does not run.
  Teardown of a suite that did start still runs, with a fresh 10 second
  budget shared by its remaining teardown steps, so cleanup is not skipped. A
  cancel that arrives during a teardown step interrupts that step. Without a
  cancel, teardown steps use the normal `--timeout`/`timeoutMs` limits.
  Every reporter that was requested
  (console, `--json-stdout`, `--junit`, `--json`, `--html`) is still written.
  The process then exits `5`. A second SIGINT or SIGTERM exits immediately,
  without waiting for those reporters.
- `--timeout` is the default per-attempt HTTP timeout (30s). A step's
  `timeoutMs` overrides it for that step, and each retry gets a fresh budget.
  See `docs/project-format.md`.

Secrets come from the environment (`${env:API_TOKEN}`), never the repo.

## Environment files

`--env-file FILE` (repeatable) loads a dotenv file into the process
environment before the run, so `${env:NAME}` and redaction work the same way
as a variable the job exported. Later files override earlier ones. A variable
already set in the process — the usual way a CI system injects a secret —
wins over the file. `--env-file-override` reverses that and lets the file
replace existing variables.

```bash
verity run project.yaml --env staging --env-file .env.staging --bail \
  --junit reports/junit.xml --json reports/result.json
```

Accepted lines: blank lines, `#` comments, optional `export `, `KEY=VALUE`,
single or double quotes. Double quotes interpret `\n`, `\r`, `\t`, `\\`, `\"`,
and `\'`; any other `\X` is left as a backslash followed by that character.
Single quotes are literal. A `#` comment after the value must be outside
quotes and preceded by whitespace. Names match `[A-Za-z_][A-Za-z0-9_]*`.

Do not commit the file. Values are never printed. A malformed line fails the
run with exit code 3 (`config_error`) and the location `file:line`, and no
earlier file in the same command is applied. The file is not written into
the project or the report.

## GitHub Actions

Use the bundled composite action (`.github/actions/verity`) or call the CLI
directly. Example workflow:

```yaml
name: API contract tests
on: [push, pull_request]
jobs:
  verity:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: pgodlews/verity-api/.github/actions/verity@main
        with:
          project: project.yaml
          junit: reports/junit.xml
          json: reports/result.json
        env:
          API_TOKEN: ${{ secrets.API_TOKEN }}
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: verity-reports
          path: reports/
```

Pin the action to a public release tag or commit for reproducible builds.
The action builds the `verity` binary from its own checkout, runs your project,
and propagates the exit code so the job fails on test failures. Start any
required test services before invoking it.

| Input | Default / purpose |
| --- | --- |
| `project` | Required path to your project. |
| `env` | Empty: use the project's default environment. |
| `parallel` | `1`: maximum concurrent cases per suite. |
| `junit`, `json`, `html` | Empty: optional report file paths. |
| `vars` | Empty: `KEY=VALUE` overrides, one per line (values may contain spaces); pass secrets through job environment variables. |
| `suite`, `case` | Empty: names or globs to run, one per line (`--suite`, `--case`). |
| `bail` | `false`: `true` stops scheduling new cases after the first failure. |
| `env-file` | Empty: dotenv files to load, one path per line. |
| `plugins` | Empty: optional plugins configuration path; naming the file trusts it. |
| `trust-plugins` | `false`: `true` runs the project's own `.verity/plugins.json`. |
| `go-version` | `1.26`: Go toolchain used to build the CLI. |

## Docker

A multi-stage `Dockerfile` produces a tiny image containing only the static
`verity` binary:

```bash
docker build -t verity/verity .
docker run --rm -v "$PWD:/work" -w /work --user "$(id -u):$(id -g)" \
  -e API_TOKEN="$API_TOKEN" \
  verity/verity run project.yaml --env staging --junit junit.xml
```

The image is based on `scratch` + CA certificates, so it is a few MB and has no
shell or extra surface area. It runs as user 65534 by default; `--user` with
your own uid and gid lets it write reports into the bind-mounted directory,
which that user cannot write to otherwise.

## GitLab CI

Use an image with a shell and install the CLI (the published scratch image
has no shell for a GitLab job script). Pin the Go install version to a public
release tag for reproducible builds.

```yaml
api-tests:
  image: golang:1.26
  before_script:
    - go install github.com/pgodlews/verity-api/cmd/verity@latest
  script:
    - verity run project.yaml --env staging --junit junit.xml --json result.json
  artifacts:
    when: always
    reports:
      junit: junit.xml
    paths:
      - result.json
```

## Load thresholds

`verity load` uses the same exit codes. A latency threshold or
`--max-error-rate` that fails exits `1`, including when no request ran.
A refused remote target exits `3`. The default allow-list is loopback,
RFC1918, and link-local, applied before the run and again to every HTTP
dial (redirects, proxies, feeds, captured hosts). Ctrl-C still exits `5`
and writes `--json`. With no threshold and no error-rate cap, request failures
are counted in the report and the process still exits `0` — set a threshold
when the job should go red. See `docs/load.md`.

```bash
verity load project.yaml --env local --vus 10 --duration 30s \
  --threshold 'p95<500ms' --max-error-rate 1% \
  --json reports/load.json
```

`--iterations N` is the number of case runs **each** virtual user performs,
so the total is `vus * N` and is exact. `--duration` stops starting new
iterations at the deadline; in-flight requests finish.

## Tips

- Use `verity validate project.yaml --env staging --json` as a pre-check.
  With only SDL schema files it stays local. `schema.introspect: true` needs
  the network and sends that step's credentials. For offline CI, commit the
  SDL from `verity graphql schema project.yaml --step NAME --env staging --out schema.graphql`
  and point `schema.file` at it.
- Use `verity fmt --check project.yaml` to fail on formatting drift without
  rewriting the checkout. It checks included fragments too; see `docs/git.md`.
- Use `verity list --json` in a setup step to assert required plugins are
  present.
- Files a project names (`${secret:file:...}`, mTLS cert/key/CA files,
  `json_schema` files, feeds, and uploads) must be inside the project
  directory. A secret mounted outside it, such as `/run/secrets/token`, needs
  `verity run --allow-external-files`; otherwise the step fails with a
  `config_error`. Prefer `${env:NAME}` for CI secrets.
- A project's own `.verity/plugins.json` runs only when trusted: pass
  `--plugins .verity/plugins.json` or `--trust-plugins`, or set
  `VERITY_TRUST_PLUGINS=1` in the job. Without one of these, `run` and `load`
  exit `3` with a `config_error` naming the file and start no plugin. Trust
  only repositories whose plugin binaries you control; pull-request jobs from
  forks should not set it. See [the plugin guide](extending-plugins.md#trusting-project-plugins).
- Pin parallelism to your runner's resources with `--parallel`.
- Use `--bail` when a long suite should stop spending runners after the first
  real failure. Reports still list the cases that did not run.
