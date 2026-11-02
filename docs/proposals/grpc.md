# Proposal: gRPC unary as a first-party plugin

Status: implemented in `plugins/grpc`. Core still registers a stub at
`core/protocols/grpc_stub.go`. Server, client and bidirectional streaming were
added in a follow-up; current request options and limits are documented in
[`project-format.md`](../project-format.md#grpc-protocol-grpc) and
[`plugins/grpc/README.md`](../../plugins/grpc/README.md).

## Decision

Unary gRPC ships as a first-party **out-of-process** VPP/1 protocol plugin
(`plugins/grpc`, its own Go module), not as a dependency of `core/`. The
plugin binary registers protocol id `grpc`. Loading it replaces the stub for
that run. Projects that do not load the plugin still get a structured
`plugin_error` whose message points at the plugin.

`google.golang.org/grpc` stays out of the core module so the `verity` binary
does not grow a protobuf stack it does not use. Streaming is not in this
change.

## Call shape

A step with `protocol: grpc` puts the call under `request`:

| field | meaning |
|-------|---------|
| `target` | gRPC target (`host:port`, or a `unix:///` socket path) |
| `method` | `package.Service/Method` |
| `message` | JSON object, or a string containing one. The host interpolates it first. |
| `metadata` | string map (or list of strings). Copied onto request headers so captures and redaction see it. Message-layer auth headers are sent as metadata too. |
| `plaintext` | `true` for an unencrypted connection |
| `tls` | `{insecure, caFile, serverName}` when `plaintext` is not set. `caFile` is read from the project directory. |
| `descriptorSet` | path to a binary `FileDescriptorSet`, relative to the project directory. Omitted: server reflection v1, then v1alpha. |

`timeoutMs` is the attempt deadline. The host sends that deadline on
`protocol.execute`; the plugin cancels the RPC when it fires. A deadline
during the RPC is a response with gRPC code 4 (`DEADLINE_EXCEEDED`), not a
transport error. Dial failures and a reflection timeout are `transport_error`.
A missing or invalid descriptor, an unknown method, or an invalid message
shape is `config_error`.

## Response mapping

Existing assertions are unchanged. The plugin fills `spi.Response` as follows:

- `statusCode` is the gRPC code number. A `status` assertion compares that
  number (0 is OK), the same way it compares an HTTP status.
- `status` is the canonical code name (`OK`, `NOT_FOUND`, `DEADLINE_EXCEEDED`, …).
- The body is protojson of the reply with `EmitUnpopulated: false` and
  `UseProtoNames: true`, content type `application/json`.
- Response headers, then trailers, are response headers. `Grpc-Status` is the
  decimal code and `Grpc-Message` is the status text. Binary `*-bin` metadata
  is not copied.
- A non-OK status from the server is still a response, so an assertion can
  check it. It is not a transport error, so `retry.until: status` does not
  treat it as one. `until: passed` retries when an assertion fails.

The plugin does not log metadata. Values it returns travel through the host's
normal capture path (`core/runner/capture.go`), which redacts with the run's
`secrets.Redactor`. `${env:…}` values are registered before that capture.

## Wire additions

VPP/1 `protocol.execute` gains optional fields the host now sends: `dir`
(project directory), `allowExternalFiles`, and `deadline` (RFC3339Nano). Older
plugins ignore them. When the plugin returns a `*verr.Error`, the SDK keeps
that code instead of forcing `transport_error`.

## Non-goals

Streaming, client-streaming, and bidi. Connection pooling (each step dials).
Moving grpc-go into core. Generating stubs from `.proto` inside Verity — the
caller supplies a descriptor set or the server serves reflection.
