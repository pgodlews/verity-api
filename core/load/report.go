package load

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

// ThresholdResult is one evaluated latency bound.
type ThresholdResult struct {
	Expr     string `json:"expr"`
	Passed   bool   `json:"passed"`
	Step     string `json:"step,omitempty"`
	Metric   string `json:"metric,omitempty"`
	ActualMs millis `json:"actualMs"`
	LimitMs  millis `json:"limitMs"`
	Message  string `json:"message,omitempty"`
}

// ErrorRateResult is the evaluated --max-error-rate, when one was set.
// Max and Actual are fractions (0.01 means 1%).
type ErrorRateResult struct {
	Max     float64 `json:"max"`
	Actual  float64 `json:"actual"`
	Passed  bool    `json:"passed"`
	Message string  `json:"message,omitempty"`
}

// SetupStep is one setup-hook step that ran once before the virtual users.
type SetupStep struct {
	Suite  string `json:"suite"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Report is the JSON document written by `verity load --json`. Every string
// has already been passed through the run redactor. See docs/load.md.
type Report struct {
	SchemaVersion   string            `json:"schemaVersion"`
	Project         string            `json:"project"`
	Environment     string            `json:"environment"`
	StartedAt       time.Time         `json:"startedAt"`
	FinishedAt      time.Time         `json:"finishedAt"`
	DurationMs      int64             `json:"durationMs"`
	Interrupted     bool              `json:"interrupted"`
	VUs             int               `json:"vus"`
	Iterations      int64             `json:"iterations"`
	RampUpMs        int64             `json:"rampUpMs,omitempty"`
	ThinkMs         int64             `json:"thinkMs,omitempty"`
	ConfiguredMs    int64             `json:"configuredDurationMs,omitempty"`
	ConfiguredIters int               `json:"configuredIterations,omitempty"`
	Targets         []string          `json:"targets"`
	Summary         Metrics           `json:"summary"`
	Steps           []Metrics         `json:"steps"`
	Thresholds      []ThresholdResult `json:"thresholds"`
	ErrorRate       *ErrorRateResult  `json:"errorRateThreshold,omitempty"`
	Setup           []SetupStep       `json:"setup,omitempty"`
	SetupFailed     bool              `json:"setupFailed,omitempty"`
	// SetupErrorCode is the verr code of the first failed setup step, so a
	// config or validation problem can be told from a failed check.
	SetupErrorCode string            `json:"setupErrorCode,omitempty"`
	Teardown       []SetupStep       `json:"teardown,omitempty"`
	TeardownFailed bool              `json:"teardownFailed,omitempty"`
	Passed         bool              `json:"passed"`
	red            *secrets.Redactor `json:"-"`
}

// Failures lists the threshold, error-rate, and setup checks that did not pass.
func (r *Report) Failures() []string {
	if r == nil {
		return nil
	}
	var out []string
	if r.SetupFailed {
		out = append(out, "setup failed; load did not start")
	}
	if r.TeardownFailed {
		out = append(out, "teardown failed")
	}
	for _, th := range r.Thresholds {
		if !th.Passed {
			if th.Message != "" {
				out = append(out, th.Message)
				continue
			}
			out = append(out, th.Expr)
		}
	}
	if r.ErrorRate != nil && !r.ErrorRate.Passed {
		if r.ErrorRate.Message != "" {
			out = append(out, r.ErrorRate.Message)
		} else {
			out = append(out, fmt.Sprintf("error rate %.2f%% exceeds max %.2f%%",
				r.ErrorRate.Actual*100, r.ErrorRate.Max*100))
		}
	}
	return out
}

// FormatLive is the one-line progress text printed once a second on a TTY.
func FormatLive(vus int, rps, p95 float64, errors int64) string {
	return fmt.Sprintf("vus=%d rps=%.1f p95=%.1fms errors=%d", vus, rps, p95, errors)
}

// FormatStart is the line printed before any virtual user runs.
func FormatStart(targets []string, vus int, iterations int, duration, ramp time.Duration) string {
	mode := fmt.Sprintf("iterations=%d", iterations)
	if duration > 0 {
		mode = "duration=" + duration.String()
	}
	hosts := "(none)"
	if len(targets) > 0 {
		hosts = strings.Join(targets, ", ")
	}
	s := fmt.Sprintf("load: targets %s; vus=%d %s", hosts, vus, mode)
	if ramp > 0 {
		s += " ramp-up=" + ramp.String()
	}
	return s
}

// WriteJSON writes the report as indented JSON. The bytes are redacted again
// before they are written.
func WriteJSON(w io.Writer, rep *Report) error {
	if rep == nil {
		return fmt.Errorf("nil load report")
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	red := rep.red
	if red == nil {
		red = secrets.NewRedactor()
	}
	b = red.RedactBytes(b)
	_, err = w.Write(b)
	return err
}

// WriteConsole writes the human summary. Text is redacted before it is written.
func WriteConsole(w io.Writer, rep *Report) error {
	if rep == nil {
		return fmt.Errorf("nil load report")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Verity load: %s", rep.Project)
	if rep.Environment != "" {
		fmt.Fprintf(&b, " [env: %s]", rep.Environment)
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "vus=%d iterations=%d duration=%s", rep.VUs, rep.Iterations, time.Duration(rep.DurationMs)*time.Millisecond)
	if len(rep.Targets) > 0 {
		fmt.Fprintf(&b, " targets=%s", strings.Join(rep.Targets, ","))
	}
	if rep.Interrupted {
		fmt.Fprintf(&b, " interrupted")
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("=", 72))
	if len(rep.Setup) > 0 {
		for _, s := range rep.Setup {
			fmt.Fprintf(&b, "setup %s %s %s\n", s.Suite, s.Name, s.Status)
			if s.Error != "" {
				fmt.Fprintf(&b, "  %s\n", s.Error)
			}
		}
	}
	writeMetricsTable(&b, append([]Metrics{summaryRow(rep.Summary)}, rep.Steps...))
	fmt.Fprintf(&b, "\n")
	if len(rep.Thresholds) == 0 && rep.ErrorRate == nil {
		fmt.Fprintf(&b, "thresholds: (none)\n")
	}
	for _, th := range rep.Thresholds {
		mark := "pass"
		if !th.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "threshold %s %s", mark, th.Expr)
		if th.Message != "" && !th.Passed {
			fmt.Fprintf(&b, " (%s)", th.Message)
		} else if float64(th.ActualMs) > 0 || th.Passed {
			fmt.Fprintf(&b, " (%.2fms)", float64(th.ActualMs))
		}
		fmt.Fprintf(&b, "\n")
	}
	if rep.ErrorRate != nil {
		mark := "pass"
		if !rep.ErrorRate.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "error rate %s actual %.2f%% max %.2f%%\n",
			mark, rep.ErrorRate.Actual*100, rep.ErrorRate.Max*100)
	}
	if rep.Summary.Requests > 0 && len(rep.Summary.Errors) > 0 {
		fmt.Fprintf(&b, "errors:\n")
		for _, e := range rep.Summary.Errors {
			fmt.Fprintf(&b, "  %s\n", e)
		}
	}
	text := b.String()
	if rep.red != nil {
		text = rep.red.Redact(text)
	}
	_, err := io.WriteString(w, text)
	return err
}

func summaryRow(m Metrics) Metrics {
	m.Name = "overall"
	return m
}

func writeMetricsTable(b *strings.Builder, rows []Metrics) {
	fmt.Fprintf(b, "%-24s %8s %6s %6s %8s %8s %8s %8s %8s %8s %8s %8s\n",
		"STEP", "REQS", "FAIL", "ERR", "RPS", "MIN", "MEAN", "P50", "P90", "P95", "P99", "MAX")
	for _, m := range rows {
		name := m.Name
		if name == "" {
			name = "overall"
		}
		if len(name) > 24 {
			name = name[:21] + "..."
		}
		errN := m.TransportErrors + m.OtherErrors
		fmt.Fprintf(b, "%-24s %8d %6d %6d %8.1f %8.2f %8.2f %8.2f %8.2f %8.2f %8.2f %8.2f\n",
			name, m.Requests, m.AssertionFailures, errN, m.RPS,
			float64(m.Latency.MinMs), float64(m.Latency.MeanMs), float64(m.Latency.P50Ms), float64(m.Latency.P90Ms),
			float64(m.Latency.P95Ms), float64(m.Latency.P99Ms), float64(m.Latency.MaxMs))
	}
}
