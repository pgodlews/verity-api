package reporters

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func sampleResult() *spi.RunResult {
	return &spi.RunResult{
		Project:     "Demo",
		Environment: "dev",
		StartedAt:   time.Now(),
		FinishedAt:  time.Now(),
		DurationMS:  12,
		Redactor:    secrets.NewRedactor(),
		Suites: []spi.SuiteResult{{
			Name: "S1", Status: spi.StatusFailed, DurationMS: 12,
			Cases: []spi.CaseResult{{
				Name: "C1", Status: spi.StatusFailed, Iteration: -1,
				Steps: []spi.StepResult{{
					Name: "step", Protocol: "rest", Status: spi.StatusFailed, DurationMS: 5,
					Assertions: []spi.AssertionResult{
						{Passed: true, Type: "status"},
						{Passed: false, Type: "jsonpath", Description: "name check", Message: "mismatch"},
					},
				}},
			}},
		}},
		Summary: spi.Summary{
			Suites: 1, Cases: 1, Steps: 1, Assertions: 2, Passed: 1, Failed: 1,
			Status: spi.StatusFailed,
		},
	}
}

func render(t *testing.T, rep spi.Reporter, res *spi.RunResult) string {
	t.Helper()
	// Reporters write to stdout for "-", so render to a temp file via a buffer
	// is not directly possible; we use a temp path through the file branch.
	dir := t.TempDir()
	path := dir + "/out"
	if err := rep.Report(res, path); err != nil {
		t.Fatal(err)
	}
	b, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJSONReporterShape(t *testing.T) {
	out := render(t, &JSONReporter{}, sampleResult())
	var decoded spi.RunResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("JSON reporter output is not valid JSON: %v", err)
	}
	if decoded.Summary.Status != spi.StatusFailed {
		t.Fatalf("expected failed status, got %s", decoded.Summary.Status)
	}
	if len(decoded.Suites) != 1 {
		t.Fatalf("expected 1 suite, got %d", len(decoded.Suites))
	}
}

func TestJUnitReporterShape(t *testing.T) {
	out := render(t, &JUnitReporter{}, sampleResult())
	var ts junitTestsuites
	if err := xml.Unmarshal([]byte(out), &ts); err != nil {
		t.Fatalf("JUnit output is not valid XML: %v", err)
	}
	if ts.Tests != 1 || ts.Failures != 1 {
		t.Fatalf("expected tests=1 failures=1, got tests=%d failures=%d", ts.Tests, ts.Failures)
	}
	if !strings.Contains(out, "<failure") {
		t.Fatal("expected a <failure> element")
	}
}

func TestConsoleReporterRuns(t *testing.T) {
	out := render(t, &ConsoleReporter{}, sampleResult())
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "S1") {
		t.Fatalf("console output missing expected content:\n%s", out)
	}
}

func TestConsoleReporterSaysBailed(t *testing.T) {
	res := sampleResult()
	res.Summary.Bailed = true
	out := render(t, &ConsoleReporter{}, res)
	if !strings.Contains(out, "FAILED (bailed)") {
		t.Fatalf("console summary should say the run bailed:\n%s", out)
	}
}

func TestHTMLReporterSaysBailed(t *testing.T) {
	res := sampleResult()
	res.Summary.Bailed = true
	out := render(t, &HTMLReporter{}, res)
	if !strings.Contains(out, "(bailed)") {
		t.Fatalf("html summary should say the run bailed:\n%s", out)
	}
}

func TestReportersShowAborted(t *testing.T) {
	res := sampleResult()
	res.Summary.Status = spi.StatusAborted
	res.Summary.Aborted = true
	res.Summary.StepsAborted = 1
	res.Suites[0].Status = spi.StatusAborted
	res.Suites[0].Cases[0].Status = spi.StatusAborted
	res.Suites[0].Cases[0].Steps[0].Status = spi.StatusAborted
	res.Suites[0].Cases[0].Steps[0].Assertions = nil
	res.Suites[0].Cases = append(res.Suites[0].Cases, spi.CaseResult{
		Name: "never", Status: spi.StatusAborted, Iteration: -1,
	})

	console := render(t, &ConsoleReporter{}, res)
	if !strings.Contains(console, "[ABRT]") || !strings.Contains(console, "ABORTED (aborted)") || !strings.Contains(console, "aborted=1") {
		t.Fatalf("console should show aborted work:\n%s", console)
	}
	html := render(t, &HTMLReporter{}, res)
	if !strings.Contains(html, "aborted") || !strings.Contains(html, "(aborted)") {
		t.Fatalf("html should show aborted work:\n%s", html)
	}
	js := render(t, &JSONReporter{}, res)
	if !strings.Contains(js, `"status": "aborted"`) || !strings.Contains(js, `"aborted": true`) {
		t.Fatalf("json should use status aborted:\n%s", js)
	}
	xmlOut := render(t, &JUnitReporter{}, res)
	if strings.Count(xmlOut, `message="aborted"`) < 2 {
		t.Fatalf("junit should mark aborted steps and cases as skipped with message aborted:\n%s", xmlOut)
	}
	if !strings.Contains(xmlOut, "<skipped") {
		t.Fatalf("junit aborted entries should be skipped elements:\n%s", xmlOut)
	}
}

func TestJUnitSkippedCaseWithoutSteps(t *testing.T) {
	res := sampleResult()
	res.Suites[0].Cases = append(res.Suites[0].Cases, spi.CaseResult{
		Name: "not-run", Status: spi.StatusSkipped, Iteration: -1,
	})
	out := render(t, &JUnitReporter{}, res)
	var ts junitTestsuites
	if err := xml.Unmarshal([]byte(out), &ts); err != nil {
		t.Fatal(err)
	}
	if ts.Tests != 2 || ts.Skipped != 1 || ts.Failures != 1 {
		t.Fatalf("tests=%d skipped=%d failures=%d, want 2, 1, 1", ts.Tests, ts.Skipped, ts.Failures)
	}
	if !strings.Contains(out, `name="not-run"`) || !strings.Contains(out, "<skipped") {
		t.Fatalf("expected a skipped testcase for the case that never ran:\n%s", out)
	}
}

func TestReportersMentionRetries(t *testing.T) {
	res := sampleResult()
	res.Suites[0].Cases[0].Steps[0].Attempts = 3
	res.Suites[0].Cases[0].Steps[0].Status = spi.StatusPassed

	console := render(t, &ConsoleReporter{}, res)
	if !strings.Contains(console, "3 attempts") {
		t.Fatalf("console output should mention retries:\n%s", console)
	}
	once := sampleResult()
	once.Suites[0].Cases[0].Steps[0].Attempts = 1
	consoleOnce := render(t, &ConsoleReporter{}, once)
	if strings.Contains(consoleOnce, "attempts") {
		t.Fatalf("a single attempt should not be labelled as retries:\n%s", consoleOnce)
	}

	html := render(t, &HTMLReporter{}, res)
	if !strings.Contains(html, "3 attempts") {
		t.Fatalf("html output should mention retries:\n%s", html)
	}

	junit := render(t, &JUnitReporter{}, res)
	if !strings.Contains(junit, "<system-out>3 attempts</system-out>") {
		t.Fatalf("junit output should mention retries:\n%s", junit)
	}
	failed := sampleResult()
	failed.Suites[0].Cases[0].Steps[0].Attempts = 4
	junitFailed := render(t, &JUnitReporter{}, failed)
	if !strings.Contains(junitFailed, "after 4 attempts") {
		t.Fatalf("junit failure text should mention retries:\n%s", junitFailed)
	}

	js := render(t, &JSONReporter{}, res)
	if !strings.Contains(js, `"attempts": 3`) {
		t.Fatalf("json output should include attempts:\n%s", js)
	}
}

func TestHTMLReporterRuns(t *testing.T) {
	out := render(t, &HTMLReporter{}, sampleResult())
	if !strings.Contains(out, "<!DOCTYPE html>") || !strings.Contains(out, "Demo") {
		t.Fatal("HTML output missing expected content")
	}
}

// TestReporterCreatesParentDirs verifies reporters create missing parent
// directories for the output path (regression: --junit reports/junit.xml must
// work without a pre-existing reports/ dir).
func TestReporterCreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	nested := dir + "/reports/sub/junit.xml"
	if err := (&JUnitReporter{}).Report(sampleResult(), nested); err != nil {
		t.Fatalf("expected reporter to create parent dirs, got: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("expected output file at %s: %v", nested, err)
	}
}

var _ = bytes.NewBuffer
