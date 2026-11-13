# gRPC unary example

Calls `verity.testpb.Echo/Echo` through the first-party plugin. The method is
the one implemented by `plugins/grpc/internal/testserver` (a fixture, not part
of the product). Point `GRPC_TARGET` at any unary server that serves the same
method, or at that fixture.

Build the plugin (and, for a local try, the fixture):

```bash
# plugins/grpc is its own Go module, so build from inside it
(cd plugins/grpc && go build -o verity-grpc . && go build -o internal/testserver/testserver ./internal/testserver)
```

`examples/grpc/.verity/plugins.json` loads `plugins/grpc/verity-grpc`. Start the
fixture on a unix socket (`SOCK` is that socket's path), export `API_TOKEN`,
then run:

```bash
plugins/grpc/internal/testserver/testserver -listen "$SOCK"
GRPC_TARGET="unix://${SOCK}" verity run examples/grpc/project.yaml --trust-plugins
```

`API_TOKEN` is sent as request metadata (`${env:API_TOKEN}`) and is redacted
in the report. Omit `descriptorSet` to use server reflection. Set it to a
`FileDescriptorSet` path inside the project directory to skip reflection.

`status: 0` is gRPC OK, not HTTP 200. See `docs/project-format.md`.
