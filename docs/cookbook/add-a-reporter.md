# Cookbook: add a reporter

Full interface details: `docs/extending-reporters.md`.

## Scaffold

```bash
verity plugin new reporter md
cd md && go mod tidy && go test ./... && go build -o md .
```

Replace `Report` with a Markdown summary:

```go
func (MdReporter) Report(result *spi.RunResult, out string) error {
    var b strings.Builder
    fmt.Fprintf(&b, "# %s — %s\n\n", result.Project, result.Summary.Status)
    for _, s := range result.Suites {
        fmt.Fprintf(&b, "## %s (%s)\n", s.Name, s.Status)
        for _, c := range s.Cases {
            fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Status)
        }
    }
    if out == "" || out == "-" {
        _, err := os.Stdout.WriteString(b.String())
        return err
    }
    return os.WriteFile(out, []byte(b.String()), 0o644)
}
```

## In-process

```go
reg.RegisterReporter(md.MdReporter{})
```

Built-in reporters (console/junit/json/html) live in `core/reporters` as
references — the JUnit one shows the suite→case→step mapping.
