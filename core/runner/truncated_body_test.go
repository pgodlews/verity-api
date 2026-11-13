package runner

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/protocols"
)

// A body cut at the size limit must fail body assertions with a clear message
// instead of evaluating the partial body.
func TestTruncatedBodyFailsBodyAssertions(t *testing.T) {
	old := protocols.MaxResponseBody
	protocols.MaxResponseBody = 16
	t.Cleanup(func() { protocols.MaxResponseBody = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"padding":"` + strings.Repeat("x", 64) + `"}`))
	}))
	defer srv.Close()
	p := &model.Project{
		SchemaVersion: "1.0", Name: "trunc",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
			Name: "get", Protocol: "rest", Request: map[string]any{"url": srv.URL},
			Assertions: []model.Assertion{
				{Type: "status", Value: 200},
				{Type: "contains", Value: `"ok":true`}, // inside the kept prefix, still refused
			},
		}}}}}},
	}
	res := retryRun(t, nil, p, Options{})
	as := res.Suites[0].Cases[0].Steps[0].Assertions
	if len(as) != 2 || !as[0].Passed || as[1].Passed || !strings.Contains(as[1].Message, "truncated") {
		t.Fatalf("assertions = %+v", as)
	}
}
