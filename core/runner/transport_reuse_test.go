package runner

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

// Sequential steps without TLS config reuse one keep-alive connection.
func TestStepsReuseConnections(t *testing.T) {
	var newConns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	step := model.Step{Name: "get", Protocol: "rest", Request: map[string]any{"url": srv.URL}}
	p := &model.Project{
		SchemaVersion: "1.0", Name: "reuse",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{
			{Name: "a", Steps: []model.Step{step, step, step}},
			{Name: "b", Steps: []model.Step{step}},
		}}},
	}
	res := retryRun(t, nil, p, Options{})
	if res.Summary.Errors != 0 {
		t.Fatalf("summary %+v", res.Summary)
	}
	if n := newConns.Load(); n != 1 {
		t.Fatalf("opened %d connections for 4 sequential requests, want 1", n)
	}
}
