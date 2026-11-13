// Package spi defines Verity's stable Service Provider Interfaces — the small,
// documented Go interfaces behind every extension point: Protocol, Assertion,
// Reporter, AuthProvider, DataSource, and Step.
//
// These interfaces are the public contract for both first-party and third-party
// extensions. First-party protocols, assertions, reporters, and auth providers
// are implemented through exactly these interfaces as canonical examples — the
// host gives built-ins no special privileges.
//
// Design rules (also the AI-agent friendliness contract):
//   - Interfaces are small and strongly typed.
//   - No hidden global state: everything an extension needs is passed in via
//     explicit context structs.
//   - Errors are returned as values and should be *verr.Error for determinism.
//   - Types here carry no behavior beyond the interface methods, so they are
//     trivial to mock and to drive from generated code.
package spi

import (
	"context"
	"net/http"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

// Request is the protocol-neutral, fully-resolved request a Protocol plugin
// executes. Protocol-specific fields live in Config; common HTTP-ish fields are
// promoted for convenience and for AuthProviders to mutate uniformly.
type Request struct {
	// Method is the HTTP method for HTTP-based protocols (REST/SOAP/GraphQL).
	Method string
	// URL is the target endpoint (already interpolated).
	URL string
	// Headers are request headers. AuthProviders mutate these.
	Headers http.Header
	// Query holds URL query parameters (AuthProviders may add API keys here).
	Query map[string]string
	// Cookies holds request cookies (AuthProviders may add API keys here).
	Cookies map[string]string
	// Body is the raw request body bytes.
	Body []byte
	// BodySummary, when set, replaces Body in captures (e.g. upload metadata).
	// It is local to in-process protocols; VPP/1 request encoding is unchanged.
	BodySummary *string `json:"-"`
	// Config carries the protocol-specific, resolved request configuration
	// (e.g. SOAP action, gRPC method, GraphQL variables).
	Config map[string]any
	// TLS carries transport-level TLS configuration contributed by transport
	// AuthProviders (e.g. mTLS). Nil means use defaults.
	TLS *TLSConfig
}

// TLSConfig is the transport-level TLS contribution from auth providers such as
// mTLS. The runner merges these into the HTTP client's transport.
type TLSConfig struct {
	// ClientCertPEM and ClientKeyPEM are the client certificate/key (mTLS).
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	// CACertPEM is a custom CA bundle to trust.
	CACertPEM []byte
	// MinVersion / MaxVersion are tls.Version* constants (0 = default).
	MinVersion uint16
	MaxVersion uint16
	// InsecureSkipVerify disables verification (test-only; warned against).
	InsecureSkipVerify bool
	// CipherSuites optionally restricts cipher suites (tls constants).
	CipherSuites []uint16
}

// Response is the protocol-neutral response captured from a step execution.
// It is the input to Assertions and property Transfers.
type Response struct {
	// StatusCode is the HTTP status (or protocol-mapped status) for the call.
	StatusCode int
	// Status is a human status string.
	Status string
	// Headers are response headers/metadata.
	Headers http.Header
	// Body is the raw response body.
	Body []byte
	// Duration is the wall-clock time the call took.
	Duration time.Duration
	// ContentType is the resolved content type, if known.
	ContentType string
	// Meta carries protocol-specific extras (e.g. gRPC trailers). See
	// MetaTruncated.
	Meta map[string]any
}

// MetaTruncated is the Response.Meta key (value true) a protocol sets when
// it cut the body at its size limit. Body assertions then fail instead of
// evaluating a partial body.
const MetaTruncated = "truncated"

// ProtocolContext carries everything a Protocol plugin needs to execute a
// request without reaching into global state.
type ProtocolContext struct {
	// ChallengeResponders may re-sign a replayable HTTP request after a 401.
	ChallengeResponders []ChallengeResponder
	// Dir is the project source directory for resolving relative input files.
	// Empty means the current working directory. Out-of-process protocol
	// plugins receive it as an optional "dir" field on protocol.execute.
	Dir string
	// AllowExternalFiles lets upload bodies read files outside Dir. Off by
	// default so a downloaded project cannot exfiltrate local files.
	AllowExternalFiles bool
	// Client is the HTTP client the protocol should use for HTTP-based calls.
	// It already has transport-level TLS (from mTLS providers) applied.
	Client *http.Client
	// Redactor masks secrets in any diagnostics the protocol emits.
	Redactor *secrets.Redactor
}

// Protocol is the extension point for a wire protocol (REST, SOAP, gRPC,
// GraphQL, or third-party). Implementations translate a resolved Request into a
// concrete call and capture a Response.
type Protocol interface {
	// Type returns the stable protocol identifier used in project files
	// (e.g. "rest", "soap").
	Type() string
	// BuildRequest converts a step's raw, already-interpolated request config
	// into a protocol Request. It must not perform any network I/O.
	BuildRequest(ctx context.Context, raw map[string]any) (*Request, error)
	// Execute performs the call and returns the Response.
	Execute(ctx context.Context, pctx *ProtocolContext, req *Request) (*Response, error)
}

// AssertionContext gives an Assertion access to the response plus helpers.
type AssertionContext struct {
	Response *Response
	// Redactor for safe messages.
	Redactor *secrets.Redactor
}

// AssertionResult is the typed outcome of evaluating one assertion.
type AssertionResult struct {
	Passed      bool   `json:"passed"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	// Message explains the outcome (already redacted by the assertion).
	Message string `json:"message,omitempty"`
	// Expected/Actual support rich diffs in reporters (redacted).
	Expected any `json:"expected,omitempty"`
	Actual   any `json:"actual,omitempty"`
}

// Assertion is the extension point for a response check. Config comes from the
// model.Assertion fields (Expr/Op/Value/Config), passed via Spec.
type Assertion interface {
	// Type returns the stable assertion identifier (e.g. "status", "jsonpath").
	Type() string
	// Evaluate runs the check against the response.
	Evaluate(ctx context.Context, actx *AssertionContext, spec AssertionSpec) (*AssertionResult, error)
}

// AssertionSpec is the resolved configuration of a single assertion instance.
type AssertionSpec struct {
	Description string
	Expr        string
	Op          string
	Value       any
	Config      map[string]any
}

// AuthScope identifies where an AuthProvider was attached, for diagnostics.
type AuthScope string

const (
	ScopeEnvironment AuthScope = "environment"
	ScopeSuite       AuthScope = "suite"
	ScopeCase        AuthScope = "case"
	ScopeStep        AuthScope = "step"
)

// AuthLayer distinguishes the two composable auth layers.
type AuthLayer int

const (
	// LayerMessage providers mutate the outgoing request (headers, query, body).
	LayerMessage AuthLayer = iota
	// LayerTransport providers contribute TLS configuration (e.g. mTLS).
	LayerTransport
)

// AuthProvider is the extension point for authentication. The two layers
// compose: a single call may stack a transport provider (mTLS) and one or more
// message providers (e.g. OAuth bearer). Providers manage their own token
// lifecycle in memory and must never persist secrets.
type AuthProvider interface {
	// Type returns the stable provider identifier (e.g. "basic", "oauth2").
	Type() string
	// Layer reports whether this provider mutates the request (message) or
	// contributes transport TLS configuration.
	Layer() AuthLayer
	// Apply mutates req to carry credentials. For message providers this sets
	// headers/query/cookies/body; for transport providers it sets req.TLS.
	// Implementations may perform network I/O for token acquisition and must
	// cache/refresh tokens in memory keyed to expiry.
	Apply(ctx context.Context, req *Request) error
}

// ChallengeResponder is an optional in-process HTTP authentication extension.
// RespondChallenge inspects a 401 and mutates the wire request for one replay.
// Returning false preserves the original response. Implementations must be safe
// for concurrent calls. HTTP protocols never retry a second challenge.
type ChallengeResponder interface {
	RespondChallenge(ctx context.Context, req *http.Request, resp *http.Response) (bool, error)
}

// AuthFactory constructs an AuthProvider from resolved config. Factories are
// registered by type in the registry. The redactor must be used to register any
// secret material the provider handles so it is masked in all output.
type AuthFactory func(config map[string]any, red *secrets.Redactor) (AuthProvider, error)

// DataSource is the extension point for data-driven feeds (CSV/JSON/custom).
type DataSource interface {
	// Type returns the stable feed identifier (e.g. "csv", "json").
	Type() string
	// Rows returns the feed rows. dir is the project source directory used to
	// resolve relative paths. Each row maps column name to string value.
	Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error)
}

// Reporter is the extension point for run reporting. A reporter receives the
// completed, structured RunResult and writes it somewhere (console/file).
// Reporters must route all output through the Redactor in the result.
type Reporter interface {
	// Type returns the stable reporter identifier (e.g. "console", "junit").
	Type() string
	// Report writes the run result. out is the destination (file path or "-"
	// for stdout); reporters that don't write files may ignore it.
	Report(result *RunResult, out string) error
}
