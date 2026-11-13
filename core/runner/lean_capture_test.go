package runner

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// Lean captures (load testing) keep only the response status, and the step
// records the protocol call's own duration. Transfers still decide what is
// secret the way a full capture would.
func TestLeanCapturesKeepStatusAndProtocolDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"host":"203.0.113.10","token":"lean-secret-token-value"}`))
	}))
	t.Cleanup(srv.Close)
	p := transferProject(srv.URL, []model.TransferSpec{
		{Name: "host", From: "jsonpath", Expr: "$.host"},
		{Name: "tok", From: "jsonpath", Expr: "$.token"},
	}, "host")
	for _, lean := range []bool{false, true} {
		res := retryRun(t, nil, p, Options{LeanCaptures: lean})
		st := res.Suites[0].Cases[0].Steps[0]
		if st.Status != spi.StatusPassed || st.Response == nil || st.Response.StatusCode != 200 {
			t.Fatalf("lean=%v: step %+v", lean, st)
		}
		if st.ResponseDuration <= 0 || st.ResponseDuration > st.Duration {
			t.Fatalf("lean=%v: response %v, step %v", lean, st.ResponseDuration, st.Duration)
		}
		if lean && (st.Request != nil || st.Response.Body != "" || len(st.Response.Headers) != 0) {
			t.Fatalf("lean capture kept request/body/headers: %+v %+v", st.Request, st.Response)
		}
		if !lean && (st.Request == nil || st.Response.Body == "") {
			t.Fatalf("full capture is missing request or body")
		}
		got := map[string]string{}
		for _, c := range st.Captures {
			got[c.Name] = c.Value
		}
		if got["host"] != "203.0.113.10" || got["tok"] != secrets.Mask {
			t.Fatalf("lean=%v: captures %v", lean, got)
		}
	}
}
