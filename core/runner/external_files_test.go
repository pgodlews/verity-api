package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// Every project-named file read is confined to the project directory unless
// AllowExternalFiles (--allow-external-files) is set.
func TestProjectFileReadsAreConfined(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	secretFile := write("token.txt", "outside-secret-file-value-4242\n")
	feedFile := write("feed.csv", "id\n7\n")
	schemaFile := write("schema.json", `{"type":"object"}`)
	certFile := write("cert.pem", "not a cert")

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Token"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	step := func(name string) model.Step {
		return model.Step{Name: name, Protocol: "rest", Request: map[string]any{"url": srv.URL}}
	}
	secretStep := step("secret")
	secretStep.Request["headers"] = map[string]any{"X-Token": "${secret:file:" + secretFile + "}"}
	schemaStep := step("schema")
	schemaStep.Assertions = []model.Assertion{{Type: "json_schema", Expr: schemaFile}}
	mtlsStep := step("mtls")
	mtlsStep.Auth = &model.AuthRef{Use: []string{"client"}}

	cases := map[string]model.TestCase{
		"secret": {Name: "secret", Steps: []model.Step{secretStep}},
		"feed":   {Name: "feed", Feed: &model.Feed{Type: "csv", Path: feedFile}, Steps: []model.Step{step("feed")}},
		"schema": {Name: "schema", Steps: []model.Step{schemaStep}},
		"mtls":   {Name: "mtls", Steps: []model.Step{mtlsStep}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := &model.Project{
				SchemaVersion: "1.0", Name: "confine", SourceDir: projectDir,
				Environments: []model.Environment{{Name: "default", Default: true}},
				Auth:         []model.AuthDef{{Name: "client", Type: "mtls", Config: map[string]any{"certFile": certFile, "keyFile": certFile}}},
				Suites:       []model.Suite{{Name: "s", Cases: []model.TestCase{c}}},
			}
			refused := retryRun(t, nil, p, Options{})
			if st := refused.Suites[0].Cases[0].Status; st == spi.StatusPassed {
				t.Fatalf("outside file was read without --allow-external-files")
			}
			if out, _ := json.Marshal(refused); !strings.Contains(string(out), "outside the project directory") {
				t.Fatalf("refusal does not explain the confinement: %s", out)
			}
			allowed := retryRun(t, nil, p, Options{AllowExternalFiles: true})
			st := allowed.Suites[0].Cases[0].Status
			if name == "mtls" {
				// The file is read, then rejected as a certificate.
				msg := allowed.Suites[0].Cases[0].Steps[0].Error
				if msg == nil || strings.Contains(msg.Message, "outside the project directory") {
					t.Fatalf("mtls with opt-in = %+v", msg)
				}
				return
			}
			if st != spi.StatusPassed {
				t.Fatalf("with --allow-external-files: status %s: %+v", st, allowed.Suites[0].Cases[0].Steps)
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	for _, v := range seen {
		if v == "outside-secret-file-value-4242" {
			return
		}
	}
	t.Fatalf("secret file value never sent with opt-in: %v", seen)
}
