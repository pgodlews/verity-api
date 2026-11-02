# Out-of-process plugins — the Verity Plugin Protocol v1 (VPP/1)

This document specifies, precisely, the mechanism Verity uses for external
plugins so they ship as standalone binaries **without recompiling the host**.

## Why out-of-process (and not native plugins)

Go's native `-buildmode=plugin` is brittle: a plugin must be built with the
exact same compiler version, dependency versions, and build flags as the host,
and it is unsupported on Windows. That conflicts with Verity's goals (lean,
cross-platform single binaries; plugins shared without host rebuilds).

So Verity runs plugins as **separate OS processes** and talks to them over
**newline-delimited JSON** on stdin/stdout (JSON-RPC in spirit). This is the
HashiCorp go-plugin style, minus the gRPC/protobuf toolchain — keeping both the
host and the plugin SDK dependency-light and trivially cross-compilable.

> Transport note: VPP/1 fixes the *message contracts* (handshake + the per-SPI
> request/response payloads). The *transport* is stdio+JSON today. A future
> VPP/2 may add gRPC or WASM transports reusing the same contracts; the host
> negotiates via the handshake `version`.

## Lifecycle

```
host                                   plugin process
 │  exec(pluginPath, args...)           │
 │ ───────────────────────────────────▶│ start
 │                                      │ print Handshake line to stdout
 │ ◀─────────────────────────────────── │
 │  validate magic + version + kind     │
 │                                      │
 │  {"id":1,"method":"...","params":…}  │
 │ ───────────────────────────────────▶│ read request line from stdin
 │                                      │ run SPI method
 │ ◀─────────────────────────────────── │ {"id":1,"result":…}  (or "error")
 │  … more calls …                      │
 │  {"id":N,"method":"host.shutdown"}   │
 │ ───────────────────────────────────▶│ exit
```

- The plugin's **first stdout line** is the handshake (a JSON `Handshake`).
- Each subsequent line from the host is a `Request`; the plugin replies with one
  `Response` line (matching `id`).
- The host serializes calls (one in flight at a time per plugin process).
- Plugin **stderr** is passed through to the host's stderr for logging.
- On shutdown the host sends `host.shutdown`, closes stdin, and waits (then
  kills after a timeout).

## Handshake

```json
{
  "magic": "VERITY_PLUGIN_V1",
  "version": 1,
  "kind": "assertion",
  "type": "json_array_len",
  "pluginName": "example-assertion",
  "pluginVersion": "0.1.0"
}
```

The host rejects the plugin unless `magic == "VERITY_PLUGIN_V1"` and
`version == 1`. `kind` is one of `protocol|assertion|reporter|auth|datasource`.
`type` is the stable id the extension registers under.

## Methods and payloads

| Kind       | Method                  | params                                  | result |
|------------|-------------------------|-----------------------------------------|--------|
| protocol   | `protocol.buildRequest` | `{request: <stepRequestMap>}`           | `{request: <WireRequest>}` |
| protocol   | `protocol.execute`      | `{request: <WireRequest>}`              | `{response: <WireResponse>}` |
| assertion  | `assertion.evaluate`    | `{response: <WireResponse>, spec: {description,expr,op,value,config}}` | `{result: <AssertionResult>}` |
| auth       | `auth.layer`            | `{config: <map>}`                       | `{layer: 0|1}` (0=message,1=transport) |
| auth       | `auth.apply`            | `{config: <map>, request: <WireRequest>}` | `{request: <WireRequest>}` |
| reporter   | `reporter.report`       | `{result: <RunResult>, out: <path>}`    | `{ok: true}` |
| datasource | `datasource.rows`       | `{dir, config, inline, path}`           | `{rows: [{...}]}` |
| (all)      | `host.shutdown`         | —                                       | — (plugin exits) |

### WireRequest / WireResponse

Bodies are base64-encoded so binary payloads survive JSON transport.

```json
WireRequest  = {"method","url","headers":{k:v},"query":{k:v},"cookies":{k:v},"body":"<base64>","config":{...}}
WireResponse = {"statusCode","status","headers":{k:v},"body":"<base64>","durationMs","contentType"}
```

`protocol.execute` also accepts optional `dir` (project directory),
`allowExternalFiles`, and `deadline` (RFC3339Nano attempt deadline from
`timeoutMs` or the run timeout). Plugins that ignore unknown fields keep
working. `Request.BodySummary` (the capture stand-in for upload bytes) is
still not on this wire format.

When a plugin returns a `*verr.Error`, the SDK copies that `code` onto the
wire. Any other error from `Execute` stays `transport_error`, and any other
error from `BuildRequest` stays `plugin_error`.

### Errors

```json
{"id": 7, "error": {"code": "transport_error", "message": "...", "details": {...}}}
```

`code` mirrors `core/verr` codes (`validation_error`, `config_error`,
`not_found`, `transport_error`, `assertion_failed`, `auth_error`,
`import_error`, `plugin_error`, `internal_error`, `busy`). The host surfaces
these as structured `*verr.Error`. `busy` is the GUI rejecting a second run
while one is already in progress. The SDK decodes generic JSON with
`json.Number`; a malformed request envelope receives a `plugin_error` response
with the request ID instead of being dropped without a reply.

## Writing a plugin (the easy way)

Use the SDK — it handles the handshake and RPC loop. Implement one SPI interface
and call the matching `Serve*`:

```go
package main

import (
    "github.com/pgodlews/verity-api/core/plugin/sdk"
    "github.com/pgodlews/verity-api/core/spi"
)

type myAssertion struct{}
func (myAssertion) Type() string { return "my_check" }
func (myAssertion) Evaluate(/* ... */) (*spi.AssertionResult, error) { /* ... */ }

func main() { sdk.ServeAssertion(myAssertion{}) }
```

`Serve*` functions: `ServeProtocol`, `ServeAssertion`, `ServeReporter`,
`ServeDataSource`, and `ServeAuth(type, factory)`.

Scaffold a complete, buildable, tested plugin module:

```bash
verity plugin new <protocol|assertion|reporter|auth|datasource> <name> [--out DIR]
```

It generates `go.mod` (with a `replace` to your core checkout), `main.go`,
`impl_test.go`, `plugin.json`, and `README.md`.

## Registering plugins with a host

Create a plugins config (auto-discovered at `<project-dir>/.verity/plugins.json`,
or pass `--plugins path.json`):

```json
{
  "plugins": [
    { "name": "example-assertion",
      "path": "/abs/path/to/plugins/example-assertion/example-assertion",
      "args": [],
      "enabled": true }
  ]
}
```

Then run normally:

```bash
verity run project.yaml
```

The host launches each enabled plugin, validates the handshake, and registers a
proxy in the registry. From the runner's perspective, the plugin's extension is
indistinguishable from a built-in.

## A complete worked example

`plugins/example-assertion` is a full, runnable plugin implementing a
`json_array_len` assertion, with a unit test. The host's integration test
(`core/plugin/integration_test.go`) builds that binary and drives it through the
real VPP/1 round-trip — proof the mechanism works end-to-end.

## Performance & leanness

Plugins are **opt-in** and **off the hot boot path**: the base binary loads no
plugin code and pays nothing unless a plugins config is present. Each plugin is
its own process launched lazily at run start and shut down at run end.

## WASM (future)

A WASM transport is a natural VPP/2 addition for sandboxed, language-agnostic
plugins. The SPI contracts above are designed to map onto a WASM component
interface without change to project files.
