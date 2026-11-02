# Cookbook: add a protocol

Full interface details: `docs/extending-protocols.md`.

## Scaffold

```bash
verity plugin new protocol myproto
cd myproto && go mod tidy && go test ./... && go build -o myproto .
```

The generated `main.go` implements `spi.Protocol` and calls
`sdk.ServeProtocol(&MyprotoProtocol{})`. Edit `BuildRequest`/`Execute` to do
your real transport.

## Use it

```json
{ "plugins": [ { "name": "myproto", "path": "/abs/path/to/myproto/myproto" } ] }
```

```yaml
steps:
  - name: call
    protocol: myproto
    request:
      url: "${BASE_URL}/thing"
```

## In-process

```go
reg.RegisterProtocol(myproto.MyProtocol{})
```

## gRPC

Unary gRPC is the first-party plugin `plugins/grpc`. Core keeps a stub that
returns `plugin_error` until that plugin is loaded. See
`docs/extending-protocols.md` and `examples/grpc`.
