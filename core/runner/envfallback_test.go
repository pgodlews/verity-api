package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
)

// A plain ${NAME} that falls back to the process environment must be
// redacted like ${env:NAME}.
func TestPlainReferenceEnvFallbackIsRedacted(t *testing.T) {
	const secret = "env-fallback-secret-value-9876"
	t.Setenv("VERITY_TEST_PLAIN_TOKEN", secret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"echo":"` + r.Header.Get("X-Token") + `"}`))
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "fallback",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
			Name: "call", Protocol: "rest",
			Request: map[string]any{
				"url":     srv.URL + "/x?t=${VERITY_TEST_PLAIN_TOKEN}",
				"headers": map[string]any{"X-Token": "${VERITY_TEST_PLAIN_TOKEN}"},
			},
		}}}}}},
	}
	res := retryRun(t, nil, p, Options{})
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), secret) {
		t.Fatalf("fallback value leaked into the result: %s", out)
	}
	reg := bootstrap.Default()
	for _, typ := range []string{"json", "junit", "html"} {
		r, err := reg.Reporter(typ)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "report."+typ)
		if err := r.Report(res, file); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatalf("fallback value leaked into the %s report", typ)
		}
	}
}
