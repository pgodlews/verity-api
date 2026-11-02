# Plugins

First-party example plugins and the scaffolding entry point for new ones.

Verity plugins run **out-of-process** and speak the Verity Plugin Protocol v1
(VPP/1) over stdin/stdout, so they ship as standalone binaries with no host
recompile. See `docs/extending-plugins.md` for the full protocol.

## Contents

- `example-assertion/` — a complete, runnable assertion plugin
  (`json_array_len`) with a unit test. The host's
  `core/plugin/integration_test.go` builds and drives it over the real protocol.
- `grpc/` — unary and streaming gRPC protocol plugin (replaces the core `grpc` stub when
  loaded). See `examples/grpc` and `docs/proposals/grpc.md`.

## Create a new plugin

```bash
verity plugin new <protocol|assertion|reporter|auth|datasource> <name> [--out DIR]
```

This scaffolds a buildable module: `go.mod` (with a `replace` to your core
checkout), `main.go` (the `sdk.Serve*` entrypoint + a stub implementation),
`impl_test.go`, `plugin.json` (manifest), and `README.md`.

```bash
cd <name>
go mod tidy
go test ./...
go build -o <name> .
```

## Register a plugin with a host

Add it to `<project-dir>/.verity/plugins.json` (auto-discovered) or pass
`verity run --plugins path.json`:

```json
{
  "plugins": [
    { "name": "example-assertion",
      "path": "/abs/path/to/plugins/example-assertion/example-assertion",
      "enabled": true }
  ]
}
```

Plugins are opt-in and off the base binary's hot path — the host loads none
unless a plugins config is present.
