# Adding a protocol

A protocol translates a step's request config into a concrete call and captures
a response. Implement `spi.Protocol`:

```go
type Protocol interface {
    Type() string                                                  // stable id, e.g. "rest"
    BuildRequest(ctx context.Context, raw map[string]any) (*Request, error)  // no I/O
    Execute(ctx context.Context, pctx *ProtocolContext, req *Request) (*Response, error)
}
```

- `raw` is the step's `request:` map, already variable-interpolated.
- `BuildRequest` must not do network I/O — just shape a `*spi.Request`.
- `Execute` performs the call. For HTTP-based protocols use `pctx.Client`
  (it already has transport-level TLS from mTLS auth applied).
- `pctx.Dir` is the project directory. REST resolves `multipart` file parts
  and `bodyFile` against it. The host sends `dir` (and `allowExternalFiles`)
  on `protocol.execute`, plus `deadline` when the attempt has one, so an
  out-of-process plugin can resolve project files and honour `timeoutMs`.

## Reference: a minimal HTTP-based protocol (in-process)

```go
package myproto

import (
    "context"
    "io"
    "net/http"
    "time"

    "github.com/pgodlews/verity-api/core/spi"
)

type MyProtocol struct{}

func (MyProtocol) Type() string { return "myproto" }

func (MyProtocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
    url, _ := raw["url"].(string)
    return &spi.Request{
        Method:  "GET",
        URL:     url,
        Headers: http.Header{},
        Config:  raw,
    }, nil
}

func (MyProtocol) Execute(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
    httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
    if err != nil {
        return nil, err
    }
    httpReq.Header = req.Headers
    start := time.Now()
    resp, err := pctx.Client.Do(httpReq)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    return &spi.Response{
        StatusCode:  resp.StatusCode,
        Status:      resp.Status,
        Headers:     resp.Header,
        Body:        body,
        Duration:    time.Since(start),
        ContentType: resp.Header.Get("Content-Type"),
    }, nil
}
```

Register it (in-process):

```go
reg.RegisterProtocol(myproto.MyProtocol{})
```

## As an out-of-process plugin

```bash
verity plugin new protocol myproto
```

This scaffolds `main.go` calling `sdk.ServeProtocol(&MyProtocol{})`. For
non-HTTP protocols (gRPC, a message bus), do the real transport inside
`Execute`; the SDK gives you a `http.DefaultClient` in `pctx` which you can
ignore. See `docs/extending-plugins.md` for the wire details.

## gRPC

Core registers protocol `grpc` as a stub (`plugin_error`) so a project that
names the protocol still validates and fails clearly. Unary and streaming
calls use the first-party plugin `plugins/grpc` (its own module; core does not import
grpc-go). Loading it registers the same type id and replaces the stub.
The descriptor determines server, client or bidirectional streaming; see the
plugin README for collection limits.

Request fields, the response mapping (`statusCode` is the gRPC code, body is
protojson), and the `timeoutMs` deadline are documented in
`docs/project-format.md` and `docs/proposals/grpc.md`. An example project is
`examples/grpc`.

A different gRPC transport can still be a protocol plugin of your own: dial in
`Execute`, and return JSON in `*spi.Response` so `jsonpath` and `status` keep
working. Return `*verr.Error` for dial and descriptor failures — the SDK
preserves the code on the wire.

## Notes

- Keep `Type()` stable; it is the value users write under `protocol:`.
- Put protocol-specific options under `req.Config` so assertions/reporters can
  introspect if needed.
- Return `*verr.Error` for clear, machine-readable failures.
