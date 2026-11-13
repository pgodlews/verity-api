package runner

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// transferServer answers /item with a 64-bit id and records the id query
// value of /use calls.
func transferServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var used []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/item" {
			_, _ = w.Write([]byte(`{"id":9007199254740993,"name":"","nested":{"a":[1,2]}}`))
			return
		}
		mu.Lock()
		used = append(used, r.URL.Query().Get("id"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), used...) }
}

func transferProject(base string, transfers []model.TransferSpec, useVar string) *model.Project {
	return &model.Project{
		SchemaVersion: "1.0", Name: "transfers",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{
			{Name: "get", Protocol: "rest", Request: map[string]any{"url": base + "/item"}, Transfers: transfers},
			{Name: "use", Protocol: "rest", Request: map[string]any{"url": base + "/use?id=${" + useVar + "}"}},
		}}}}},
	}
}

func TestTransferMatchingNothingFailsWithoutDefault(t *testing.T) {
	for _, tr := range []model.TransferSpec{
		{Name: "v", From: "jsonpath", Expr: "$.missing"},
		{Name: "v", From: "xpath", Expr: "//missing"},
		{Name: "v", From: "header", Expr: "X-Missing"},
		{Name: "v", From: "regex", Expr: `nomatch-(\d+)`},
	} {
		t.Run(tr.From, func(t *testing.T) {
			srv, _ := transferServer(t)
			if tr.From == "xpath" {
				// xpath needs an XML document; use a body that parses.
				srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`<root/>`))
				}))
				t.Cleanup(srv.Close)
			}
			res := retryRun(t, nil, transferProject(srv.URL, []model.TransferSpec{tr}, "v"), Options{})
			step := res.Suites[0].Cases[0].Steps[0]
			if step.Status != spi.StatusFailed {
				t.Fatalf("status = %s, want failed (assertions %+v)", step.Status, step.Assertions)
			}
			found := false
			for _, a := range step.Assertions {
				if a.Type == "transfer" && a.Description == "v" && !a.Passed {
					found = true
				}
			}
			if !found {
				t.Fatalf("no failed transfer result: %+v", step.Assertions)
			}
		})
	}
}

func TestTransferDefaultAndExactNumbers(t *testing.T) {
	srv, used := transferServer(t)
	res := retryRun(t, nil, transferProject(srv.URL, []model.TransferSpec{
		{Name: "id", From: "jsonpath", Expr: "$.id"},
		{Name: "fallback", From: "jsonpath", Expr: "$.missing", Default: "dflt"},
		// Found but empty is a value, not a failure.
		{Name: "name", From: "jsonpath", Expr: "$.name"},
		{Name: "nested", From: "jsonpath", Expr: "$.nested"},
	}, "id"), Options{})
	if res.Summary.Failed+res.Summary.Errors != 0 {
		t.Fatalf("summary %+v: %+v", res.Summary, res.Suites[0].Cases[0].Steps[0].Assertions)
	}
	if got := used(); len(got) != 1 || got[0] != "9007199254740993" {
		t.Fatalf("id sent = %v, want exact 9007199254740993", got)
	}
	caps := map[string]string{}
	for _, c := range res.Suites[0].Cases[0].Steps[0].Captures {
		caps[c.Name] = c.Value
	}
	if caps["fallback"] != "dflt" || caps["nested"] != `{"a":[1,2]}` {
		t.Fatalf("captures = %v", caps)
	}
}
