// Package verr defines Verity's structured, machine-readable error type.
//
// Every error surfaced to a user or AI agent should carry a stable Code, a
// human Message, and an optional Path locating the problem within a project.
// These serialize cleanly to JSON so that CLI --json output and the GUI can
// present deterministic, well-typed diagnostics without string-parsing.
package verr

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error category. Codes never change
// meaning across releases; new codes may be added.
type Code string

const (
	// CodeValidation indicates a project/schema validation failure.
	CodeValidation Code = "validation_error"
	// CodeConfig indicates invalid plugin/auth/provider configuration.
	CodeConfig Code = "config_error"
	// CodeNotFound indicates a referenced entity (env, auth, plugin) is missing.
	CodeNotFound Code = "not_found"
	// CodeTransport indicates a network/transport-level failure.
	CodeTransport Code = "transport_error"
	// CodeAssertion indicates an assertion did not hold.
	CodeAssertion Code = "assertion_failed"
	// CodeAuth indicates an authentication/authorization failure.
	CodeAuth Code = "auth_error"
	// CodeImport indicates a spec import (WSDL/OpenAPI) failure.
	CodeImport Code = "import_error"
	// CodePlugin indicates a plugin loading/execution failure.
	CodePlugin Code = "plugin_error"
	// CodeInternal indicates an unexpected internal error.
	CodeInternal Code = "internal_error"
	// CodeBusy indicates the call was rejected because the same kind of
	// operation is already running (the GUI runs one project at a time).
	CodeBusy Code = "busy"
	// CodeConflict indicates a stale save baseline or Git reference.
	// A refused save writes nothing.
	CodeConflict Code = "conflict"
	// CodeSetupFailed marks a case that was not run because a setup step of
	// its suite failed.
	CodeSetupFailed Code = "setup_failed"
	// CodeGit indicates a git command failed. The message is git's own text
	// with URL userinfo masked. Diverged, conflict, dirty, and missing
	// upstream are result statuses, not this code.
	CodeGit Code = "git_error"
)

// Error is Verity's structured error. It implements error and json.Marshaler.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	// Path locates the error within a project (e.g. "suites[0].cases[2].steps[1]").
	Path string `json:"path,omitempty"`
	// Details carries optional structured context (expected/actual, etc.).
	Details map[string]any `json:"details,omitempty"`
	// wrapped supports errors.Unwrap.
	wrapped error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s (at %s)", e.Code, e.Message, e.Path)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped cause, if any.
func (e *Error) Unwrap() error { return e.wrapped }

// MarshalJSON ensures the wrapped error is not emitted (it may be unstructured)
// while keeping the public, typed fields.
func (e *Error) MarshalJSON() ([]byte, error) {
	type alias struct {
		Code    Code           `json:"code"`
		Message string         `json:"message"`
		Path    string         `json:"path,omitempty"`
		Details map[string]any `json:"details,omitempty"`
	}
	return json.Marshal(alias{e.Code, e.Message, e.Path, e.Details})
}

// New builds a structured error.
func New(code Code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// Newf builds a structured error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap wraps an existing error with a code and message.
func Wrap(code Code, cause error, msg string) *Error {
	return &Error{Code: code, Message: msg, wrapped: cause}
}

// Context adds prefix to an error's message. A *Error cause keeps its code,
// path, and details (the most specific classification wins); any other cause
// gets code. The cause stays reachable through errors.Unwrap either way.
func Context(code Code, cause error, prefix string) *Error {
	if cause == nil {
		return New(code, prefix)
	}
	if inner, ok := As(cause); ok {
		cp := *inner
		cp.Message = prefix + ": " + inner.Message
		cp.wrapped = cause
		return &cp
	}
	return &Error{Code: code, Message: prefix + ": " + cause.Error(), wrapped: cause}
}

// WithPath returns a copy of the error annotated with a project path.
func (e *Error) WithPath(path string) *Error {
	cp := *e
	cp.Path = path
	return &cp
}

// WithDetail returns a copy of the error with an added detail key.
func (e *Error) WithDetail(key string, val any) *Error {
	cp := *e
	if cp.Details == nil {
		cp.Details = map[string]any{}
	} else {
		nd := make(map[string]any, len(cp.Details)+1)
		for k, v := range cp.Details {
			nd[k] = v
		}
		cp.Details = nd
	}
	cp.Details[key] = val
	return &cp
}

// As is a convenience wrapper over errors.As for *Error.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
