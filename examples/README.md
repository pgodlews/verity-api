# Examples

Sample projects and API contracts.

## `local-demo/`

A fully offline demo (exercised by CI). Start the bundled mock server, then run
the project. It covers REST + SOAP, bearer auth, property transfer, and proves
secret redaction.

```bash
# Terminal 1
go run ./examples/local-demo/mockserver

# Terminal 2: first export API_TOKEN with the fixture server's expected test token.
verity run examples/local-demo/project.yaml --junit out.xml --json report.json
```

The test-only token is defined in [mockserver/main.go](local-demo/mockserver/main.go).
Do not use a real credential for this demo.

`mockserver/` is a tiny local API used only as a test fixture — it is not part
of the product.

## `rest-httpbin/`

A REST project targeting an httpbin-compatible server. Shows environments,
JSONPath assertions, property transfer between steps, and bearer auth with a
secret pulled from `${env:API_TOKEN}`.

```bash
# Export API_TOKEN and BASE_URL for your test service first.
verity run examples/rest-httpbin/project.yaml --var BASE_URL="$BASE_URL"
```

## `grpc/`

A unary gRPC project. Build `plugins/grpc` first; `.verity/plugins.json` loads
it. Set `GRPC_TARGET` to the server. See `examples/grpc/README.md`.

## `specs/`

Sample API contracts for the importers:

```bash
verity import openapi examples/specs/petstore.openapi.yaml --out /tmp/petstore.yaml
verity import wsdl    examples/specs/calculator.wsdl       --out /tmp/calc.yaml
```

OpenAPI path params (`{petId}`) become `${petId}` variables; WSDL operations
become SOAP steps with generated envelopes. Generated projects always validate.
