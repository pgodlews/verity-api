# Adding an assertion

An assertion checks a response. Implement `spi.Assertion`:

```go
type Assertion interface {
    Type() string
    Evaluate(ctx context.Context, actx *AssertionContext, spec AssertionSpec) (*AssertionResult, error)
}
```

- `actx.Response` is the captured `*spi.Response`.
- `actx.Redactor` masks secrets in any message you build.
- `spec` carries the resolved `Expr`, `Op`, `Value`, and `Config` from the
  project's `assertions:` entry (already interpolated).
- Return an `*AssertionResult` with `Passed`, a human `Message`, and optionally
  `Expected`/`Actual` for rich diffs. **Return `error` only for evaluation
  faults** (e.g. a bad regex), not for a failed check — set `Passed: false`.

## Reference implementation

A "body byte size" assertion:

```go
package mychecks

import (
    "context"
    "fmt"

    "github.com/pgodlews/verity-api/core/spi"
)

type BodySize struct{}

func (BodySize) Type() string { return "body_size" }

func (a BodySize) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
    got := len(actx.Response.Body)
    want, _ := spec.Value.(float64) // YAML/JSON numbers arrive as float64
    op := spec.Op
    if op == "" {
        op = "lte"
    }
    passed := false
    switch op {
    case "eq":  passed = got == int(want)
    case "lt":  passed = got < int(want)
    case "lte": passed = got <= int(want)
    case "gt":  passed = got > int(want)
    case "gte": passed = got >= int(want)
    }
    return &spi.AssertionResult{
        Passed:   passed,
        Type:     a.Type(),
        Description: spec.Description,
        Message:  fmt.Sprintf("body size %d bytes %s %d", got, op, int(want)),
        Expected: int(want),
        Actual:   got,
    }, nil
}
```

Use it in a project:

```yaml
assertions:
  - type: body_size
    op: lte
    value: 4096
```

## Register / ship

In-process:
```go
reg.RegisterAssertion(mychecks.BodySize{})
```

As a plugin:
```bash
verity plugin new assertion body-size
# main.go calls sdk.ServeAssertion(BodySize{})
```

See the complete, runnable example in `plugins/example-assertion` (a
`json_array_len` assertion driven over the plugin protocol with a test).

## Built-in assertions as references

`core/assertions` contains canonical implementations for status, response_time,
jsonpath, xpath, contains, regex, header, json_schema, graphql_schema, and xsd. Read them as
templates.

## A note on full XSD validation

The built-in `xsd` assertion validates **well-formedness** plus an optional root
element check. Full W3C XSD validation needs a CGO libxml2 binding, which would
break pure-Go cross-compilation. If you need it, implement an `xsd_full`
assertion as a plugin that links libxml2 — the seam is exactly this interface.
