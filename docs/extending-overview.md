# Extending Verity — overview

Extensibility is a primary design goal. Verity defines five small, stable Go
interfaces — the Service Provider Interfaces (SPI) in `core/spi`. Every
first-party protocol, assertion, reporter, auth provider, and data source is
implemented through these **same** interfaces, so built-ins are just reference
plugins with no special privileges.

## The five extension points

| Interface           | Purpose                              | Built-in examples |
|---------------------|--------------------------------------|-------------------|
| `spi.Protocol`      | A wire protocol                      | rest, soap, graphql, grpc (stub in core; unary in `plugins/grpc`) |
| `spi.Assertion`     | A response check                     | status, jsonpath, xpath, … |
| `spi.Reporter`      | A run-result output format           | console, junit, json, html |
| `spi.AuthProvider`  | Authentication (transport/message)   | basic, bearer, apikey, mtls, oauth2 (`oauth2_client_credentials` alias), aws_sigv4, hmac |
| `spi.DataSource`    | A data-driven feed                   | csv, json |

Plus `spi.AuthFactory` for constructing auth providers from config.

## Two ways to extend

### 1. In-process (compiled into a custom host)

Implement an interface and register it on a `registry.Registry`. This is how
built-ins work (see `core/bootstrap`). Use this when you build your own binary
embedding core.

```go
reg := bootstrap.Default()
reg.RegisterAssertion(myAssertion{})   // your type implements spi.Assertion
runner.New(reg).Run(ctx, project, opts)
```

### 2. Out-of-process plugin (no host recompile) — recommended for sharing

Ship a separate binary that speaks the Verity Plugin Protocol v1 (VPP/1) over
stdin/stdout. The host launches it and registers a proxy that implements the SPI
by forwarding calls. This avoids Go's brittle native `-buildmode=plugin` and
keeps the base binary lean. See `docs/extending-plugins.md`.

```bash
verity plugin new assertion my-check      # scaffold a buildable plugin
cd my-check && go mod tidy && go build -o my-check .
# register in .verity/plugins.json, then: verity run project.yaml --trust-plugins
```

## Design contract (why this is AI-agent friendly)

- **Small, strongly-typed interfaces.** Each has 1–3 methods.
- **No hidden global state.** Everything an extension needs is passed via
  explicit context structs (`ProtocolContext`, `AssertionContext`).
- **Deterministic, machine-readable errors.** Return `*verr.Error` with a stable
  `code`.
- **Stable type ids.** The string an extension registers under is its public
  contract in project files and `verity list`.

## Per-point guides

- `docs/extending-protocols.md`
- `docs/extending-assertions.md`
- `docs/extending-reporters.md`
- `docs/extending-auth.md`
- `docs/extending-datasources.md`
- `docs/extending-plugins.md` — the out-of-process protocol (VPP/1)
- `docs/cookbook/` — copy-paste runnable recipes
