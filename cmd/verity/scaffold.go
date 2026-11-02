package main

// scaffoldFiles returns the relative-path → template map for a plugin of the
// given type. Templates use text/template with scaffoldData fields. The result
// is a complete, buildable plugin module: main.go (the Serve* entrypoint), the
// extension implementation, a test, a manifest, and a README.
func scaffoldFiles(ptype string) map[string]string {
	files := map[string]string{
		"go.mod":       tmplGoMod,
		"README.md":    tmplReadme,
		"plugin.json":  tmplManifest,
		"main.go":      tmplMain[ptype],
		"impl_test.go": tmplTest[ptype],
	}
	return files
}

const tmplGoMod = `module {{.Module}}

go 1.26

// The plugin SDK provides the Serve* entrypoints and the spi types. Point this
// at your local Verity checkout or a tagged release.
require github.com/pgodlews/verity-api v0.0.0

// During local development, replace with your checkout:
// replace github.com/pgodlews/verity-api => ../..
`

const tmplManifest = `{
  "name": "{{.Name}}",
  "path": "./{{.Name}}",
  "args": [],
  "enabled": true
}
`

const tmplReadme = `# {{.Name}} — Verity {{.Type}} plugin

A starter out-of-process Verity plugin implementing the **{{.Type}}** extension
point via the Verity Plugin Protocol v1 (stdin/stdout JSON-RPC).

## Build

    go mod tidy
    go build -o {{.Name}} .

## Register with a host

Add an entry to your project's plugins config (e.g. ` + "`.verity/plugins.json`" + `):

    {
      "plugins": [
        { "name": "{{.Name}}", "path": "/abs/path/to/{{.Name}}", "enabled": true }
      ]
    }

Then run:

    verity run project.yaml

The host launches this binary, completes the handshake, and registers the
extension under its type id. From the runner's perspective it is identical to a
built-in.

## Test

    go test ./...

See ` + "`docs/extending-plugins.md`" + ` in the Verity repo for the full protocol spec.
`

// tmplMain holds the main.go entrypoint template per plugin type.
var tmplMain = map[string]string{
	"protocol": `package main

import (
	"context"

	"github.com/pgodlews/verity-api/core/plugin/sdk"
	"github.com/pgodlews/verity-api/core/spi"
)

// {{.Title}}Protocol is an example Protocol plugin. Replace the body with your
// own wire protocol (e.g. gRPC, a message bus, a custom transport).
type {{.Title}}Protocol struct{}

func (p *{{.Title}}Protocol) Type() string { return "{{.Name}}" }

func (p *{{.Title}}Protocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	url, _ := raw["url"].(string)
	return &spi.Request{Method: "GET", URL: url, Config: raw}, nil
}

func (p *{{.Title}}Protocol) Execute(_ context.Context, _ *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	// TODO: perform the real call. This stub echoes a 200.
	return &spi.Response{StatusCode: 200, Status: "200 OK", Body: []byte("{}")}, nil
}

func main() { sdk.ServeProtocol(&{{.Title}}Protocol{}) }
`,
	"assertion": `package main

import (
	"context"
	"strings"

	"github.com/pgodlews/verity-api/core/plugin/sdk"
	"github.com/pgodlews/verity-api/core/spi"
)

// {{.Title}}Assertion is an example Assertion plugin that checks the body
// contains spec.Value (case-insensitive). Replace with your own logic.
type {{.Title}}Assertion struct{}

func (a *{{.Title}}Assertion) Type() string { return "{{.Name}}" }

func (a *{{.Title}}Assertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	needle, _ := spec.Value.(string)
	body := strings.ToLower(string(actx.Response.Body))
	passed := strings.Contains(body, strings.ToLower(needle))
	return &spi.AssertionResult{
		Passed: passed, Type: a.Type(), Description: spec.Description,
		Message: "expected body to contain (ci) " + needle, Expected: needle,
	}, nil
}

func main() { sdk.ServeAssertion(&{{.Title}}Assertion{}) }
`,
	"reporter": `package main

import (
	"os"

	"github.com/pgodlews/verity-api/core/plugin/sdk"
	"github.com/pgodlews/verity-api/core/spi"
)

// {{.Title}}Reporter is an example Reporter plugin that writes a one-line
// summary. Replace with your own format (Slack, TeamCity, etc.).
type {{.Title}}Reporter struct{}

func (r *{{.Title}}Reporter) Type() string { return "{{.Name}}" }

func (r *{{.Title}}Reporter) Report(result *spi.RunResult, out string) error {
	line := "Verity: " + string(result.Summary.Status) + "\n"
	if out == "" || out == "-" {
		_, err := os.Stdout.WriteString(line)
		return err
	}
	return os.WriteFile(out, []byte(line), 0o644)
}

func main() { sdk.ServeReporter(&{{.Title}}Reporter{}) }
`,
	"auth": `package main

import (
	"context"

	"github.com/pgodlews/verity-api/core/plugin/sdk"
	"github.com/pgodlews/verity-api/core/spi"
)

// {{.Title}}Auth is an example message-level AuthProvider plugin. It adds a
// custom header from config["value"]. Replace with your own signing logic
// (e.g. AWS SigV4, HMAC). See the cookbook for a SigV4 sketch.
type {{.Title}}Auth struct{ value string }

func (a *{{.Title}}Auth) Type() string         { return "{{.Name}}" }
func (a *{{.Title}}Auth) Layer() spi.AuthLayer  { return spi.LayerMessage }

func (a *{{.Title}}Auth) Apply(_ context.Context, req *spi.Request) error {
	req.Headers.Set("X-{{.Title}}-Auth", a.value)
	return nil
}

func main() {
	sdk.ServeAuth("{{.Name}}", func(config map[string]any) (spi.AuthProvider, error) {
		v, _ := config["value"].(string)
		return &{{.Title}}Auth{value: v}, nil
	})
}
`,
	"datasource": `package main

import (
	"github.com/pgodlews/verity-api/core/plugin/sdk"
)

// {{.Title}}Source is an example DataSource plugin returning two static rows.
// Replace with your own source (a database, an API, a generator).
type {{.Title}}Source struct{}

func (d *{{.Title}}Source) Type() string { return "{{.Name}}" }

func (d *{{.Title}}Source) Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error) {
	return []map[string]string{
		{"id": "1", "name": "alpha"},
		{"id": "2", "name": "beta"},
	}, nil
}

func main() { sdk.ServeDataSource(&{{.Title}}Source{}) }
`,
}

// tmplTest holds a minimal test per plugin type so scaffolds ship green.
var tmplTest = map[string]string{
	"protocol": `package main

import (
	"context"
	"testing"
)

func TestBuildRequest(t *testing.T) {
	p := &{{.Title}}Protocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{"url": "http://x"})
	if err != nil || req.URL != "http://x" {
		t.Fatalf("unexpected: %v %+v", err, req)
	}
}
`,
	"assertion": `package main

import (
	"context"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestEvaluate(t *testing.T) {
	a := &{{.Title}}Assertion{}
	actx := &spi.AssertionContext{Response: &spi.Response{Body: []byte("Hello World")}}
	res, err := a.Evaluate(context.Background(), actx, spi.AssertionSpec{Value: "world"})
	if err != nil || !res.Passed {
		t.Fatalf("expected pass, got %v %+v", err, res)
	}
}
`,
	"reporter": `package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestReport(t *testing.T) {
	r := &{{.Title}}Reporter{}
	out := filepath.Join(t.TempDir(), "r.txt")
	if err := r.Report(&spi.RunResult{Summary: spi.Summary{Status: spi.StatusPassed}}, out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if len(b) == 0 {
		t.Fatal("empty report")
	}
}
`,
	"auth": `package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestApply(t *testing.T) {
	a := &{{.Title}}Auth{value: "abc"}
	req := &spi.Request{Headers: http.Header{}}
	if err := a.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Headers.Get("X-{{.Title}}-Auth") != "abc" {
		t.Fatal("header not set")
	}
}
`,
	"datasource": `package main

import "testing"

func TestRows(t *testing.T) {
	d := &{{.Title}}Source{}
	rows, err := d.Rows("", nil, nil, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("unexpected: %v %d", err, len(rows))
	}
}
`,
}
