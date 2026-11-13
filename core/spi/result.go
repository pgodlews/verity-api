package spi

import (
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

// Status is the outcome of a step/case/suite/run.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
	// StatusAborted is work that never started because the run context was
	// cancelled (Ctrl-C, SIGTERM, or the GUI's Cancel). A step that had
	// already started is not aborted: it finishes or fails on its own.
	StatusAborted Status = "aborted"
)

// Run-progress event kinds. See RunEvent.
const (
	EventCaseStarted = "case_started"
	EventStepDone    = "step_done"
	EventCaseDone    = "case_done"
)

// RunResult is the top-level, machine-readable result of executing a project.
// It is the single structure all reporters consume. Every string it contains is
// already redacted; reporters should additionally pass any derived text through
// Redactor for defense in depth.
type RunResult struct {
	Project     string        `json:"project"`
	Environment string        `json:"environment"`
	StartedAt   time.Time     `json:"startedAt"`
	FinishedAt  time.Time     `json:"finishedAt"`
	DurationMS  int64         `json:"durationMs"`
	Suites      []SuiteResult `json:"suites"`
	Summary     Summary       `json:"summary"`
	// Redactor is the redactor used for this run; reporters use it for any
	// additional text they render. Not serialized.
	Redactor *secrets.Redactor `json:"-"`
}

// Summary aggregates counts across the run.
type Summary struct {
	Suites       int    `json:"suites"`
	Cases        int    `json:"cases"`
	Steps        int    `json:"steps"`
	Assertions   int    `json:"assertions"`
	Passed       int    `json:"passed"`
	Failed       int    `json:"failed"`
	Errors       int    `json:"errors"`
	Skipped      int    `json:"skipped"`
	StepsFailed  int    `json:"stepsFailed,omitempty"`
	StepsErrored int    `json:"stepsErrored,omitempty"`
	StepsSkipped int    `json:"stepsSkipped,omitempty"`
	Status       Status `json:"status"`
	// Bailed is true when --bail stopped the run after a failed or errored
	// case. Cases that never started are reported as skipped. Omitted when
	// the run did not bail, so older reports stay the same shape.
	Bailed bool `json:"bailed,omitempty"`
	// Aborted is true when the run context was cancelled before the walk
	// finished. Work that never started is reported as "aborted", in project
	// order. Omitted when the run was not cancelled.
	Aborted bool `json:"aborted,omitempty"`
	// StepsAborted counts steps (including ones inside cases that never
	// started) whose status is aborted. Omitted when zero.
	StepsAborted int `json:"stepsAborted,omitempty"`

	// Passed and Failed above count assertions while Errors and Skipped
	// count steps; they are kept for compatibility. The fields below name
	// their unit. Case counts include cases with no steps (for example
	// cases --bail never started), which the step counts cannot see. Zero
	// counts are omitted, like the step counts.
	AssertionsPassed int `json:"assertionsPassed,omitempty"`
	AssertionsFailed int `json:"assertionsFailed,omitempty"`
	CasesPassed      int `json:"casesPassed,omitempty"`
	CasesFailed      int `json:"casesFailed,omitempty"`
	CasesErrored     int `json:"casesErrored,omitempty"`
	CasesSkipped     int `json:"casesSkipped,omitempty"`
	CasesAborted     int `json:"casesAborted,omitempty"`
}

// RunEvent is one progress notification from a run. Runner.Options.OnEvent
// may be called from several case goroutines at once; callbacks must be safe
// for concurrent use. The Step pointer, when set, is a redacted copy and is
// safe to retain after the callback returns.
type RunEvent struct {
	Kind      string `json:"kind"` // case_started, step_done, case_done
	Suite     string `json:"suite"`
	Case      string `json:"case"`
	Iteration int    `json:"iteration"`
	// StepIndex is the step's position in the case. Meaningful for step_done.
	StepIndex int `json:"stepIndex,omitempty"`
	// Step is set for step_done. It is already redacted, the same way the
	// final RunResult is.
	Step *StepResult `json:"step,omitempty"`
	// Status and DurationMS are set for case_done.
	Status     Status `json:"status,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

// SuiteResult is the result of one suite.
type SuiteResult struct {
	Name       string       `json:"name"`
	Status     Status       `json:"status"`
	DurationMS int64        `json:"durationMs"`
	Cases      []CaseResult `json:"cases"`
}

// CaseResult is the result of one case (one iteration for data-driven feeds).
type CaseResult struct {
	Name       string `json:"name"`
	Status     Status `json:"status"`
	DurationMS int64  `json:"durationMs"`
	// Iteration is the 0-based data-feed iteration index, or -1 if not fed.
	Iteration int          `json:"iteration"`
	Steps     []StepResult `json:"steps"`
}

// StepResult is the result of one step execution.
type StepResult struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	Status     Status `json:"status"`
	DurationMS int64  `json:"durationMs"`
	// Duration is DurationMS at full resolution (attempts plus the delays
	// between them). Run reports keep publishing durationMs only. Load
	// testing reads Duration so a sub-millisecond sample is not truncated
	// to zero.
	Duration time.Duration `json:"-"`
	// Attempts is how many times this step executed the request, including
	// the first try. Omitted when the step was skipped, aborted, or failed
	// before an attempt (for example a bad step variable or an unknown protocol).
	// DurationMS is the total across every attempt and the delays between
	// them, truncated to milliseconds. Request and Response are the final
	// attempt only.
	Attempts int `json:"attempts,omitempty"`
	// Request/Response are redacted, human-inspectable captures.
	Request    *RequestCapture   `json:"request,omitempty"`
	Response   *ResponseCapture  `json:"response,omitempty"`
	Assertions []AssertionResult `json:"assertions,omitempty"`
	// Captures are the values this step's transfers stored (redacted).
	Captures []Capture `json:"captures,omitempty"`
	// Error is set when the step could not complete (transport/build error).
	Error *ErrorInfo `json:"error,omitempty"`
}

// Capture is one variable set by a property transfer.
type Capture struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RequestCapture is a redacted snapshot of the request, for reports/inspector.
type RequestCapture struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Cookies are the request cookies (from the step and auth), redacted.
	Cookies map[string]string `json:"cookies,omitempty"`
	Body    string            `json:"body,omitempty"`
	// BodyTruncated is true when Body was cut at the capture limit (1 MiB).
	BodyTruncated bool `json:"bodyTruncated,omitempty"`
}

// ResponseCapture is a redacted snapshot of the response.
type ResponseCapture struct {
	StatusCode int               `json:"statusCode"`
	Status     string            `json:"status,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	// BodyTruncated is true when Body was cut at the capture limit (1 MiB);
	// assertions saw the full body.
	BodyTruncated bool  `json:"bodyTruncated,omitempty"`
	DurationMS    int64 `json:"durationMs"`
}

// ErrorInfo is a serialized structured error (mirrors verr.Error fields).
type ErrorInfo struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Path    string         `json:"path,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}
