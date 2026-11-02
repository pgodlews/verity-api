# Verity core

The engine. Everything Verity does lives here; the CLI and GUI are thin shells
over this library. Importable as `github.com/pgodlews/verity-api/core/...`.

## Packages

| Package | Responsibility |
|---------|----------------|
| `model` | Project data model; YAML/JSON loader (multi-file `include`); semantic `Validate`. |
| `spi` | Stable Service Provider Interfaces (Protocol, Assertion, Reporter, AuthProvider, DataSource) and shared Request/Response/RunResult types. |
| `registry` | Typed lookup table for extensions (no global singletons). |
| `protocols` | REST (including multipart and file bodies), SOAP, GraphQL (full); gRPC stub (unary calls are `plugins/grpc`). |
| `assertions` | status, response_time, jsonpath, xpath, json_schema, xsd, contains, regex, header. |
| `auth` | Providers (none, basic, bearer, apikey, mtls, oauth2 with client-credentials / password / refresh-token; `oauth2_client_credentials` alias) + scope-aware resolver/chain. |
| `runner` | Executes suites→cases→steps; auth application; transfers; data feeds; parallelism; builds RunResult. `CaseExec` runs one case for load tests. |
| `load` | Concurrent virtual users over the runner; latency, error rate, thresholds. |
| `reporters` | console, junit, json, html. |
| `secrets` | `${...}` interpolation + the single `Redactor` used by all output. |
| `importers` | OpenAPI and WSDL → project scaffolding. |
| `schema` | Published JSON Schemas (project + auth) and a schema validator. |
| `plugin` + `plugin/sdk` | Out-of-process plugin host and author SDK (VPP/1). |
| `verr` | Structured, machine-readable errors. |
| `bootstrap` | Wires all first-party extensions into a registry. |

## Embedding core in your own host

```go
import (
    "context"
    "github.com/pgodlews/verity-api/core/bootstrap"
    "github.com/pgodlews/verity-api/core/model"
    "github.com/pgodlews/verity-api/core/runner"
)

reg := bootstrap.Default()            // all built-ins, registered via the SPI
// reg.RegisterAssertion(myAssertion) // add your own in-process extensions

project, err := model.Load("project.yaml")
if err != nil { /* ... */ }
if errs := project.Validate(); len(errs) > 0 { /* ... */ }

result, err := runner.New(reg).Run(context.Background(), project, runner.Options{
    Environment: "staging",
    Parallel:    4,
})
// hand result to a reporter:
rep, _ := reg.Reporter("json")
_ = rep.Report(result, "report.json")
```

## Testing

```bash
go test ./core/...
go test -cover ./core/...
```

Core tests cover: project parse/validate, each protocol client (against local
test servers), each assertion type, JUnit/JSON report shape, plugin
registration/loading (real subprocess), every Tier-1 auth provider (incl. an
mTLS handshake with a generated test CA and OAuth client-credentials caching +
pre-expiry refresh), auth scope inheritance/override and stacking, and a direct
redaction test asserting secrets never appear in any reporter output.

## Design rules

- Small, strongly-typed interfaces; no hidden global state.
- Errors are `*verr.Error` with stable codes.
- All output goes through `secrets.Redactor`.
- Built-ins are reference plugins — no privileged paths.
