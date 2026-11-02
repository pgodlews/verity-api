# grpc

First-party gRPC protocol plugin with unary, server-streaming, client-streaming
and bidirectional calls. It speaks VPP/1 and is a separate Go module so the
core `verity` binary does not link grpc-go.

```bash
go build -o grpc .
```

Register it as protocol `grpc` (this replaces the core stub for the run).
See `examples/grpc` for registration and `docs/project-format.md` for TLS and
metadata options. When `descriptorSet` is omitted, the plugin uses server
reflection v1, then v1alpha. The descriptor determines the streaming kind.

Use `request.message` for unary and server-streaming calls. It accepts a JSON
object or a string containing one. Client-streaming and bidi calls require
`request.messages`, a list of JSON objects; an empty list is allowed. Setting
both fields or using the wrong field for the method is `config_error`.

```yaml
protocol: grpc
request:
  target: "${GRPC_TARGET}"
  method: "pkg.Service/Collect"
  messages:
    - { name: first }
    - { name: second }
  stream:
    maxMessages: 1000
    idleTimeoutMs: 5000
    sendIntervalMs: 10
    maxBytes: 16777216
  metadata: { authorization: "${env:API_TOKEN}" }
assertions:
  - { type: status, value: 0 }
  - { type: jsonpath, expr: "$[0].name", value: first }
  - { type: header, expr: messageCount, value: "2" }
  - { type: header, expr: truncated, value: "false" }
```

Bidi sends all requests in list order, closes the send side, and collects
responses in receive order. Sending and receiving run concurrently to avoid
flow-control deadlocks; there is no interactive scripting. A server that ends
early can stop further sends. `sendIntervalMs` delays between requests, with
no delay before the first or after the last (default `0`).

Unary reply bodies remain protojson objects. Every streaming reply body is a
JSON array, including client-streaming's single reply and an empty `[]` when
nothing was received. Proto field names are used (`UseProtoNames`,
`EmitUnpopulated` off). JSONPath selects the array directly: `$[0].name`,
`$[1].name`, etc. The built-in JSONPath evaluator does not provide
`$.length()`; use a `header` assertion on `messageCount` for the array count.
An installed array-count assertion plugin can also check the array body.

Streaming response fields `messageCount` and `truncated` are synthetic headers
(case-insensitive), so existing assertions, VPP/1 and reports can consume them.
They override server metadata with those names. `messageCount` is the number
of messages retained. `truncated` is `"true"` when `maxMessages` was reached,
otherwise `"false"`. Reaching the cap stops immediately with status `0` and
cancels the stream; it does not probe for another message. Even an exact-size
reply is marked truncated when it reaches the cap. Server trailers are only
available if received before cancellation. Assert `truncated: "false"` when
completeness matters.

`stream.maxMessages` defaults to `1000` and must be a positive integer.
`stream.maxBytes` defaults to 16 MiB (`16777216`) and caps the total collected
JSON array bytes, including brackets and commas. Exceeding it returns a clear
`transport_error`, with no partial response. The minimum is `2`. The existing
32 MiB per-message receive limit still applies independently; protobuf decoding
and one prefetched message also use memory beyond the collected JSON budget.

`timeoutMs` is the attempt deadline the host passes on `protocol.execute`.
`stream.idleTimeoutMs` limits the wait for the first response and the gap
between received messages; sends do not reset it. It defaults to `0` (off).
Both time fields must be nonnegative integer milliseconds. Deadlines and idle
timeouts cancel the stream and return gRPC status `4` (`DEADLINE_EXCEEDED`)
with messages collected so far. Context cancellation closes the stream and
joins its workers promptly; the host handles Ctrl-C as an interrupted run.

A server status is a response (`statusCode` is the gRPC code; `0` is OK),
including an early error after some streamed messages. `status` is the
canonical name; `Grpc-Status` and `Grpc-Message` carry the numeric code and
status message. Headers and trailers are captured as today. Metadata auth
values are sent unchanged and never logged by the plugin; captured headers,
bodies and errors go through the host's `core/secrets` redactor.

Run module checks from the repository root with Go 1.26+:

```bash
(cd plugins/grpc && go vet ./... && go test ./... && go test -race ./...)
```

The host assertion/redaction integration test is in
`core/plugin/grpc_integration_test.go` and runs with the root module's tests.
