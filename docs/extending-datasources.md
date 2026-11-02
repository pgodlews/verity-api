# Adding a data source (feed)

A data source supplies rows for data-driven test cases. Implement
`spi.DataSource`:

```go
type DataSource interface {
    Type() string
    Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error)
}
```

- `dir` is the project directory (resolve relative `path` against it).
- `config` is the feed's `config:` map; `inline` is the feed's `inline:` rows;
  `path` is the feed's `path:`.
- Return rows as `[]map[string]string`. Each row's keys become variables for one
  iteration of the case.

## Reference: a range generator

Generates N rows with an incrementing `i` column.

```go
package rangefeed

import (
    "fmt"
    "github.com/pgodlews/verity-api/core/spi"
)

type Range struct{}

func (Range) Type() string { return "range" }

func (Range) Rows(_ string, config map[string]any, _ []map[string]string, _ string) ([]map[string]string, error) {
    n := 10
    if v, ok := config["count"].(float64); ok {
        n = int(v)
    }
    rows := make([]map[string]string, 0, n)
    for i := 0; i < n; i++ {
        rows = append(rows, map[string]string{"i": fmt.Sprint(i)})
    }
    return rows, nil
}
```

Use it:

```yaml
cases:
  - name: hammer ids
    feed:
      type: range
      config: { count: 25 }
    steps:
      - name: get
        protocol: rest
        request: { url: "${BASE_URL}/items/${i}" }
        assertions:
          - type: status
            value: 200
```

Register / ship:

```go
reg.RegisterDataSource(rangefeed.Range{})  // in-process
```
```bash
verity plugin new datasource range        # out-of-process
```

## Built-in feeds as references

`core/datasource` implements `csv` (header row → columns) and `json` (array of
objects). Inline rows always take precedence over `path`.
