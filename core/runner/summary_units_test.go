package runner

import (
	"testing"

	"github.com/pgodlews/verity-api/core/spi"
)

func TestSummarizeExplicitUnits(t *testing.T) {
	res := &spi.RunResult{Suites: []spi.SuiteResult{{Cases: []spi.CaseResult{
		{Status: spi.StatusPassed, Steps: []spi.StepResult{{Status: spi.StatusPassed, Assertions: []spi.AssertionResult{{Passed: true}, {Passed: true}}}}},
		{Status: spi.StatusFailed, Steps: []spi.StepResult{{Status: spi.StatusFailed, Assertions: []spi.AssertionResult{{Passed: true}, {Passed: false}}}}},
		{Status: spi.StatusError, Steps: []spi.StepResult{{Status: spi.StatusError}}},
		{Status: spi.StatusSkipped}, // never started (--bail): no steps
		{Status: spi.StatusAborted, Steps: []spi.StepResult{{Status: spi.StatusAborted}}},
	}}}}
	s := summarize(res)
	if s.AssertionsPassed != 3 || s.AssertionsFailed != 1 {
		t.Fatalf("assertions %d/%d", s.AssertionsPassed, s.AssertionsFailed)
	}
	if s.CasesPassed != 1 || s.CasesFailed != 1 || s.CasesErrored != 1 || s.CasesSkipped != 1 || s.CasesAborted != 1 {
		t.Fatalf("cases %+v", s)
	}
	// The compatibility fields keep their meaning.
	if s.Passed != 3 || s.Failed != 1 || s.Errors != 1 || s.Skipped != 0 {
		t.Fatalf("legacy fields %+v", s)
	}
}
