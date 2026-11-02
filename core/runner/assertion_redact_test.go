package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// Assertion Expected/Actual are raw values from the response. They must be
// masked before the result (or a streamed event) reaches the GUI, the same
// way the response body capture is.
func TestAssertionValuesAreRedacted(t *testing.T) {
	const token, opaque, nested = "tok-abcdef1234567890", "opaque-sid-55667788", "nested-cred-99887766"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "sid": opaque, "id": 7,
			"token": map[string]any{"value": nested}})
	}))
	defer srv.Close()
	step := model.Step{
		Name: "login", Protocol: "rest", Request: map[string]any{"url": srv.URL},
		Assertions: []model.Assertion{
			// Hidden by the structural "access_token" pattern in the body view.
			{Type: "jsonpath", Expr: "$.access_token", Op: "eq", Value: "wrong"},
			// Named like a credential.
			{Type: "jsonpath", Expr: "$.sid", Op: "eq", Value: "x"},
			// Ordinary values stay visible.
			{Type: "jsonpath", Expr: "$.id", Op: "eq", Value: 8},
			// A credential-like path holding an object with ordinary keys.
			{Type: "jsonpath", Expr: "$.token", Op: "eq", Value: "x"},
		},
		// Captured as a session credential after the assertions ran.
		Transfers: []model.TransferSpec{{Name: "session", From: "jsonpath", Expr: "$.sid"}},
	}
	var mu sync.Mutex
	var events []spi.RunEvent
	res := retryRun(t, nil, oneStepProject(step), Options{OnEvent: func(ev spi.RunEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}})
	got := res.Suites[0].Cases[0].Steps[0]
	out, _ := json.Marshal(got)
	ev, _ := json.Marshal(events)
	for _, secret := range []string{token, opaque, nested} {
		if strings.Contains(string(out), secret) {
			t.Errorf("%s leaked into the step result: %s", secret, out)
		}
		if strings.Contains(string(ev), secret) {
			t.Errorf("%s leaked into a run event: %s", secret, ev)
		}
	}
	if a := got.Assertions[2]; a.Actual != float64(7) && a.Actual != 7 {
		t.Errorf("ordinary actual value must stay visible, got %#v", a.Actual)
	}
}
