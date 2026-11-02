# Adding a reporter

A reporter renders the completed run result. Implement `spi.Reporter`:

```go
type Reporter interface {
    Type() string
    Report(result *RunResult, out string) error  // out: file path or "-" for stdout
}
```

- `result` is the full, **already-redacted** `*spi.RunResult`.
- Route any extra text you derive through `result.Redactor` for defense in
  depth.
- `out` is the destination: a file path, or `""`/`"-"` for stdout. Reporters
  that push elsewhere (Slack, a dashboard) may ignore `out`.

## Reference implementation

A terse TAP-like reporter:

```go
package myreport

import (
    "fmt"
    "os"

    "github.com/pgodlews/verity-api/core/spi"
)

type TAP struct{}

func (TAP) Type() string { return "tap" }

func (TAP) Report(result *spi.RunResult, out string) error {
    w := os.Stdout
    if out != "" && out != "-" {
        f, err := os.Create(out)
        if err != nil {
            return err
        }
        defer f.Close()
        w = f
    }
    n := 0
    for _, s := range result.Suites {
        for _, c := range s.Cases {
            for _, st := range c.Steps {
                n++
                ok := "ok"
                if st.Status != spi.StatusPassed && st.Status != spi.StatusSkipped {
                    ok = "not ok"
                }
                fmt.Fprintf(w, "%s %d - %s/%s/%s\n", ok, n, s.Name, c.Name, st.Name)
            }
        }
    }
    fmt.Fprintf(w, "1..%d\n", n)
    return nil
}
```

Register / ship:

```go
reg.RegisterReporter(myreport.TAP{})   // in-process
```
```bash
verity plugin new reporter tap        # out-of-process
```

## The RunResult shape

See `core/spi/result.go`. Top level: `Project`, `Environment`, `DurationMS`,
`Summary` (counts + overall `Status`, `Bailed` when `--bail` stopped
scheduling new cases, and `Aborted` when the run was cancelled), and
`Suites[] → Cases[] → Steps[]`. Each step has redacted `Request`/`Response`
captures, an `Assertions[]` array, an optional structured `Error`, and
`Attempts` (how many times the request executed; omitted when the step never
ran). `DurationMS` on a step is the total across attempts and retry delays;
the captures are the final attempt. A case that `--bail` never started has
status `skipped` and no steps. A step or case that never started because the
run was cancelled has status `aborted`. The built-in JUnit reporter records
that as `<skipped message="aborted"/>`.

## Built-in reporters as references

`core/reporters` has console (human), junit (JUnit XML), json (canonical
machine format), and html (self-contained, inline CSS). The JUnit reporter shows
how to map the suite/case/step tree onto `<testsuite>/<testcase>` with
`<failure>`/`<error>` children.
