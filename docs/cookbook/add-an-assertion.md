# Cookbook: add an assertion

Two paths. Full interface details: `docs/extending-assertions.md`.

## Fastest: scaffold an out-of-process plugin

```bash
verity plugin new assertion my-check
cd my-check
go mod tidy
go test ./...          # the scaffold ships green
go build -o my-check .
```

Register it (in `.verity/plugins.json` next to your project):

```json
{ "plugins": [ { "name": "my-check", "path": "/abs/path/to/my-check/my-check" } ] }
```

Use it:

```yaml
assertions:
  - type: my-check
    value: world      # the scaffold checks the body contains this (ci)
```

Run:

```bash
verity run project.yaml
```

## Complete worked example

See `plugins/example-assertion` — a `json_array_len` assertion with a test, and
the host integration test in `core/plugin/integration_test.go` that drives it
over the real plugin protocol.

## In-process (custom host)

```go
reg := bootstrap.Default()
reg.RegisterAssertion(myChecks.MyCheck{})   // implements spi.Assertion
```
