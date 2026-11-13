package runner

import (
	"strings"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// captureRequest builds a redacted snapshot of a request for reports/inspector.
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
	return &spi.RequestCapture{
		Method:  req.Method,
		URL:     red.Redact(url),
		Headers: headers,
		Cookies: cookies,
		Body:    red.Redact(body),
	}
}

// captureResponse builds a redacted snapshot of a response.
func captureResponse(resp *spi.Response, red *secrets.Redactor) *spi.ResponseCapture {
	headers := map[string]string{}
	for k, vs := range resp.Headers {
		if len(vs) > 0 {
			headers[k] = red.Redact(strings.Join(vs, ", "))
		}
	}
	return &spi.ResponseCapture{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Headers:    headers,
		Body:       red.Redact(string(resp.Body)),
		DurationMS: resp.Duration.Milliseconds(),
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
