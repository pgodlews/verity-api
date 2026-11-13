package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/spi"
)

// Latency is the protocol call; the whole step is reported separately, and a
// step without a response falls back to its step time.
func TestLatencyIsProtocolTimeAndStepTimeIsSeparate(t *testing.T) {
	rec := newRecorder(nil)
	rec.record(spi.StepResult{
		Name: "get", Status: spi.StatusPassed,
		Duration: 50 * time.Millisecond, ResponseDuration: 2 * time.Millisecond,
		Response: &spi.ResponseCapture{StatusCode: 200},
	})
	rec.record(spi.StepResult{
		Name: "get", Status: spi.StatusError,
		Duration: 4 * time.Millisecond,
		Error:    &spi.ErrorInfo{Code: "transport_error", Message: "refused"},
	})
	summary, steps := rec.snapshot(time.Second)
	if summary.Latency.MinMs != 2 || summary.Latency.MaxMs != 4 {
		t.Fatalf("latency %+v", summary.Latency)
	}
	if summary.StepLatency.MinMs != 4 || summary.StepLatency.MaxMs != 50 {
		t.Fatalf("step latency %+v", summary.StepLatency)
	}
	if steps[0].StepLatency != summary.StepLatency {
		t.Fatalf("per-step %+v", steps[0].StepLatency)
	}
}

func TestLoadRunReportsBothLatencies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rep, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{
		VUs: 1, Iterations: 3, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := rep.Summary
	if s.Requests != 3 || s.Latency.MaxMs <= 0 || s.StepLatency.MaxMs < s.Latency.MaxMs {
		t.Fatalf("summary %+v", s)
	}
}
