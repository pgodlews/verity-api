package runner

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// captureRequest builds a redacted snapshot of a request for reports/inspector.
// captureRequest is captureRequest unless the run asked for lean captures.
func (e *execContext) captureRequest(req *spi.Request, red *secrets.Redactor) *spi.RequestCapture {
	if e.opts.LeanCaptures {
		return nil
	}
	return captureRequest(req, red)
}

// captureResponse is captureResponse unless the run asked for lean
// captures; then only the status and duration are kept.
func (e *execContext) captureResponse(resp *spi.Response, red *secrets.Redactor) *spi.ResponseCapture {
	if e.opts.LeanCaptures {
		return &spi.ResponseCapture{
			StatusCode: resp.StatusCode,
			Status:     red.Redact(resp.Status),
			DurationMS: resp.Duration.Milliseconds(),
		}
	}
	return captureResponse(resp, red)
}

// shownBody returns the response body as a full capture shows it, for the
// checks that treat a value redaction masked in the body as secret. Lean
// captures keep no body, so it is redacted on first use instead (and only
// when a value actually occurs in the raw body).
func (e *execContext) shownBody(res *spi.StepResult, resp *spi.Response, red *secrets.Redactor) func() string {
	if !e.opts.LeanCaptures {
		return func() string { return res.Response.Body }
	}
	var (
		body string
		done bool
	)
	return func() string {
		if !done {
			body, _ = captureBody(string(resp.Body), red)
			done = true
		}
		return body
	}
}

func captureRequest(req *spi.Request, red *secrets.Redactor) *spi.RequestCapture {
	headers := map[string]string{}
	for k, vs := range req.Headers {
		if len(vs) > 0 {
			headers[k] = red.Redact(strings.Join(vs, ", "))
		}
	}
	// Query params (the step's query: map and auth-added ones such as an
	// apikey in the query) may carry API keys: fold them into the URL view
	// exactly as they are sent, then redact.
	// BodySummary replaces raw bytes for uploads so file contents stay out of reports.
	url, err := spi.WireURL(req.URL, req.Query)
	if err != nil {
		url = req.URL
	}
	var cookies map[string]string
	if len(req.Cookies) > 0 {
		cookies = make(map[string]string, len(req.Cookies))
		for k := range req.Cookies {
			// Cookie values are masked like Cookie headers: they are
			// session credentials as often as not. Names stay visible.
			cookies[k] = secrets.Mask
		}
	}
	body := string(req.Body)
	if req.BodySummary != nil {
		body = *req.BodySummary
	}
	captured, truncated := captureBody(body, red)
	return &spi.RequestCapture{
		Method:        req.Method,
		URL:           red.Redact(url),
		Headers:       headers,
		Cookies:       cookies,
		Body:          captured,
		BodyTruncated: truncated,
	}
}

// captureMaxBody caps a captured body in results and reports. Assertions
// and transfers still see the full body. A variable so tests can lower it.
var captureMaxBody = 1 << 20

// captureRedactSlack is how far past the cap a body is still redacted, so a
// secret (or PEM block) that straddles the cut is masked before cutting.
const captureRedactSlack = 64 << 10

// captureBody redacts body and caps it at captureMaxBody bytes, noting the
// original size, without redacting (or copying) the rest of a large body.
func captureBody(body string, red *secrets.Redactor) (string, bool) {
	if len(body) <= captureMaxBody {
		return red.Redact(body), false
	}
	window := body
	if len(window) > captureMaxBody+captureRedactSlack {
		window = window[:captureMaxBody+captureRedactSlack]
	}
	redacted := red.Redact(window)
	if len(redacted) > captureMaxBody {
		cut := captureMaxBody
		for cut > 0 && !utf8.RuneStart(redacted[cut]) {
			cut--
		}
		redacted = redacted[:cut]
	}
	return redacted + fmt.Sprintf("\n… [capture truncated: body is %d bytes; reports keep the first %d]", len(body), captureMaxBody), true
}

// captureResponse builds a redacted snapshot of a response.
func captureResponse(resp *spi.Response, red *secrets.Redactor) *spi.ResponseCapture {
	headers := map[string]string{}
	for k, vs := range resp.Headers {
		if len(vs) > 0 {
			headers[k] = red.Redact(strings.Join(vs, ", "))
		}
	}
	body, truncated := captureBody(string(resp.Body), red)
	return &spi.ResponseCapture{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Headers:       headers,
		Body:          body,
		BodyTruncated: truncated,
		DurationMS:    resp.Duration.Milliseconds(),
	}
}

// errInfo converts an error into a serializable spi.ErrorInfo, preserving
// structured fields when the error is a *verr.Error.
func errInfo(err error) *spi.ErrorInfo {
	if err == nil {
		return nil
	}
	if ve, ok := verr.As(err); ok {
		msg := ve.Message
		// Keep the underlying cause ("connection refused", a bad token URL):
		// without it the message only says which phase failed.
		if cause := ve.Unwrap(); cause != nil {
			msg += ": " + cause.Error()
		}
		return &spi.ErrorInfo{
			Code:    string(ve.Code),
			Message: msg,
			Path:    ve.Path,
			Details: ve.Details,
		}
	}
	return &spi.ErrorInfo{Code: string(verr.CodeInternal), Message: err.Error()}
}

func (e *execContext) errInfo(err error) *spi.ErrorInfo {
	return errInfo(e.red.RedactError(err))
}

// Finalize every step diagnostic after all resolver values are registered,
// including values resolved along an error path or by an assertion.
func (e *execContext) redactStepMessages(st *spi.StepResult) {
	redactStepMessagesWith(e.red, st)
}

// redactStepMessagesWith masks step and assertion messages with r. An
// attempt-scoped redactor also covers that attempt's Digest response hashes.
func redactStepMessagesWith(r *secrets.Redactor, st *spi.StepResult) {
	if st.Error != nil {
		st.Error.Message = r.Redact(st.Error.Message)
		st.Error.Path = r.Redact(st.Error.Path)
		st.Error.Details = r.RedactMap(st.Error.Details)
	}
	for i := range st.Assertions {
		a := &st.Assertions[i]
		a.Message = r.Redact(a.Message)
		a.Description = r.Redact(a.Description)
		a.Expected = r.RedactValue(a.Expected)
		a.Actual = r.RedactValue(a.Actual)
	}
}
