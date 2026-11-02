// Package model defines the Verity project data model: the protocol-neutral,
// git-friendly representation of API contract tests.
//
// The model is intentionally serialization-format agnostic. It is loaded from
// YAML or JSON (see Load) and can be split across multiple files. No protocol,
// auth, or assertion behavior lives here — only declarative data. Behavior is
// implemented by plugins registered in the registry and driven by the runner.
package model

// Project is the root document of a Verity test project.
//
// A project references environments (variable sets per deployment target),
// test suites (which contain cases and steps), and optional auth definitions
// that can be referenced by name and composed across scopes.
type Project struct {
	// SchemaVersion pins the project-format version, e.g. "1.0". Tools and AI
	// agents use this together with the published JSON Schema to validate.
	SchemaVersion string `json:"schemaVersion" yaml:"schemaVersion"`

	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Environments are selectable variable sets (dev/staging/prod). Exactly one
	// is active per run, selected via --env. Variables interpolate as ${name}.
	Environments []Environment `json:"environments,omitempty" yaml:"environments,omitempty"`

	// Auth defines named auth providers that suites/cases/steps reference. Each
	// entry composes transport-level and/or message-level providers.
	Auth []AuthDef `json:"auth,omitempty" yaml:"auth,omitempty"`

	// Suites are the executable test suites.
	Suites []Suite `json:"suites,omitempty" yaml:"suites,omitempty"`

	// Mocks are optional responses served by `verity mock`. They are not
	// executed by `verity run`.
	Mocks []Mock `json:"mocks,omitempty" yaml:"mocks,omitempty"`

	// Include lists additional project fragment files (YAML/JSON) merged into
	// this project at load time, enabling a git-friendly split layout.
	Include []string `json:"include,omitempty" yaml:"include,omitempty"`

	// Schema is the project-wide GraphQL schema source. A suite or step schema
	// replaces it. Nil means GraphQL steps are not checked against a schema.
	Schema *GraphQLSchema `json:"schema,omitempty" yaml:"schema,omitempty"`

	// SourceDir is the directory the project was loaded from (set by Load).
	// Used to resolve relative includes and data feeds. Not serialized.
	SourceDir string `json:"-" yaml:"-"`

	// SourceFiles is every file Load read or that the loaded project points at
	// for data: the project file, included fragments, feed files (CSV/JSON and
	// any other feed with a path and no inline rows), multipart/bodyFile
	// uploads from cases and hooks, and GraphQL schema files. Paths are
	// absolute, deduplicated, and sorted. Not serialized. Paths are not
	// variable-interpolated; that happens when a step runs. The CLI watch
	// mode uses this list.
	SourceFiles []string `json:"-" yaml:"-"`
}

// GraphQLSchema is a GraphQL schema source. Set File or Introspect, not both.
// File is an SDL document. Introspect fetches the schema from the step URL
// with that step's auth and headers. A step schema replaces the suite schema,
// which replaces the project schema.
type GraphQLSchema struct {
	// File is an SDL path relative to the project directory. It must stay
	// inside that directory.
	File string `json:"file,omitempty" yaml:"file,omitempty"`
	// Introspect, when true, runs an introspection query. The result is cached
	// for the run, once per URL.
	Introspect bool `json:"introspect,omitempty" yaml:"introspect,omitempty"`
}

// Environment is a named variable set, e.g. dev/staging/prod.
type Environment struct {
	Name string `json:"name" yaml:"name"`
	// Variables are simple key/value pairs. Values may themselves contain
	// ${VAR} or ${env:VAR} references resolved at run time (see core/secrets).
	Variables map[string]string `json:"variables,omitempty" yaml:"variables,omitempty"`
	// Default marks the environment used when --env is not supplied.
	Default bool `json:"default,omitempty" yaml:"default,omitempty"`
	// Auth sets default authentication for this environment, inherited by all
	// suites/cases/steps unless overridden.
	Auth *AuthRef `json:"auth,omitempty" yaml:"auth,omitempty"`
}

// AuthRef references a named AuthDef, optionally stacking several providers
// (e.g. mTLS + OAuth bearer). When multiple are listed they all apply, in order.
type AuthRef struct {
	// Use lists AuthDef names to apply at this scope. Order is preserved.
	Use []string `json:"use,omitempty" yaml:"use,omitempty"`
	// Inherit controls whether parent-scope auth is inherited. Defaults true.
	// Set false to fully override the parent scope.
	Inherit *bool `json:"inherit,omitempty" yaml:"inherit,omitempty"`
}

// AuthDef is a named, reusable auth configuration. Type selects the registered
// AuthProvider factory (e.g. "basic", "digest", "bearer", "apikey", "mtls", "oauth2",
// "oauth2_client_credentials", "aws_sigv4", "hmac"). oauth2 selects a grant (client_credentials,
// password, refresh_token, or authorization_code); oauth2_client_credentials is an alias of the
// client-credentials grant. aws_sigv4 and hmac sign the finished request.
// Config is provider-specific.
type AuthDef struct {
	Name   string         `json:"name" yaml:"name"`
	Type   string         `json:"type" yaml:"type"`
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// TransferSpec captures a value from a step's response (via a source
// expression) into a variable for use by later steps (property transfer).
type TransferSpec struct {
	// Name is the variable the captured value is stored under.
	Name string `json:"name" yaml:"name"`
	// From selects the extraction source: "jsonpath", "xpath", "header",
	// "status", "body", or "regex".
	From string `json:"from" yaml:"from"`
	// Expr is the source expression (e.g. a JSONPath, XPath, header name,
	// or regex with a capture group).
	Expr string `json:"expr,omitempty" yaml:"expr,omitempty"`
	// Default is used when extraction yields no value.
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	// Scope is "case" (default: later steps of the same case see the value)
	// or "run": the value is also visible to every case that starts after
	// the capture, in any suite, so a login suite can feed the rest of the
	// run (Postman's global/environment variable scope).
	Scope string `json:"scope,omitempty" yaml:"scope,omitempty"`
}

// Transfer scopes (TransferSpec.Scope).
const (
	TransferScopeCase = "case"
	TransferScopeRun  = "run"
)

// Assertion is a declarative check against a step response. Type selects the
// registered Assertion plugin; the remaining fields are interpreted by it.
type Assertion struct {
	// Type selects the assertion plugin: "status", "response_time", "jsonpath",
	// "xpath", "json_schema", "xsd", "contains", "regex", "header",
	// "graphql_schema".
	Type string `json:"type" yaml:"type"`
	// Description is an optional human label shown in reports.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// Expr is the primary expression/selector (e.g. JSONPath, header name).
	Expr string `json:"expr,omitempty" yaml:"expr,omitempty"`
	// Op is the comparison operator where applicable: "eq","ne","lt","lte",
	// "gt","gte","contains","matches","exists","not_exists".
	Op string `json:"op,omitempty" yaml:"op,omitempty"`
	// Value is the expected value (string form; coerced by the assertion).
	Value any `json:"value,omitempty" yaml:"value,omitempty"`
	// Config carries any additional assertion-specific options.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// Retry condition names for Step.Retry.Until.
const (
	// RetryUntilPassed retries while any assertion fails or the attempt
	// ends in a transport error (timeout, connection failure).
	RetryUntilPassed = "passed"
	// RetryUntilStatus retries only on a transport error, HTTP 429, or an
	// HTTP status in 500–599. Other statuses are not retried, even when an
	// assertion fails.
	RetryUntilStatus = "status"
)

// MaxRetryCount is the maximum value of Retry.Count. Total executions of a
// step are 1 + Count, so a step runs at most 21 times.
const MaxRetryCount = 20

// Retry is a per-step re-execution policy. The first execution always runs;
// Count is how many further attempts may follow it.
type Retry struct {
	// Count is the number of extra attempts after the first, from 0 to
	// MaxRetryCount.
	Count int `json:"count" yaml:"count"`
	// DelayMs is the pause between attempts, in milliseconds. The pause ends
	// early when the run context is cancelled.
	DelayMs int `json:"delayMs,omitempty" yaml:"delayMs,omitempty"`
	// Until selects the retry condition: RetryUntilPassed or RetryUntilStatus.
	Until string `json:"until" yaml:"until"`
}

// Step is a single executable action within a test case.
type Step struct {
	Name string `json:"name" yaml:"name"`
	// Protocol selects the registered Protocol plugin: "rest","soap","grpc",
	// "graphql", or a third-party type.
	Protocol string `json:"protocol" yaml:"protocol"`
	// Request is the protocol-specific request configuration. It is variable-
	// interpolated before being handed to the protocol plugin.
	Request map[string]any `json:"request,omitempty" yaml:"request,omitempty"`
	// Auth applies/overrides auth at step scope (stacked, with inheritance).
	Auth *AuthRef `json:"auth,omitempty" yaml:"auth,omitempty"`
	// Assertions are checks run against the response.
	Assertions []Assertion `json:"assertions,omitempty" yaml:"assertions,omitempty"`
	// Transfers capture response values into variables for later steps.
	// When Retry is set, transfers run only on the final attempt.
	Transfers []TransferSpec `json:"transfers,omitempty" yaml:"transfers,omitempty"`
	// Vars are step-local variable overrides applied before execution.
	Vars map[string]string `json:"vars,omitempty" yaml:"vars,omitempty"`
	// Skip, when true, marks the step as skipped (reported, not executed).
	Skip bool `json:"skip,omitempty" yaml:"skip,omitempty"`
	// TimeoutMs, when > 0, is the HTTP timeout for each attempt of this step,
	// in milliseconds. It overrides the run-level timeout. Each attempt gets
	// a fresh budget. 0 uses the run timeout.
	TimeoutMs int `json:"timeoutMs,omitempty" yaml:"timeoutMs,omitempty"`
	// Retry re-executes the request when Until matches. Nil means one attempt.
	Retry *Retry `json:"retry,omitempty" yaml:"retry,omitempty"`
	// Schema overrides the suite and project GraphQL schema for this step.
	Schema *GraphQLSchema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// Feed is a data-driven source that iterates a test case over rows.
type Feed struct {
	// Type is "csv" or "json" (or a registered DataSource plugin type).
	Type string `json:"type" yaml:"type"`
	// Path is the feed file, relative to the project source directory.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// Inline holds rows directly in the project (alternative to Path).
	Inline []map[string]string `json:"inline,omitempty" yaml:"inline,omitempty"`
	// Config carries DataSource-specific options.
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
}

// TestCase groups steps that share variables and (optionally) a data feed.
type TestCase struct {
	Name string `json:"name" yaml:"name"`
	// Vars are case-scoped variables visible to all steps.
	Vars map[string]string `json:"vars,omitempty" yaml:"vars,omitempty"`
	// Auth applies/overrides auth at case scope.
	Auth *AuthRef `json:"auth,omitempty" yaml:"auth,omitempty"`
	// Feed, when present, repeats the case once per data row; each row's
	// columns are injected as variables.
	Feed *Feed `json:"feed,omitempty" yaml:"feed,omitempty"`
	// Steps run in order. A failed assertion fails the case but later steps
	// still run unless ContinueOnError is false.
	Steps []Step `json:"steps" yaml:"steps"`
	// ContinueOnError, when false, stops the case at the first failing step.
	// Defaults to true (run all steps, aggregate failures).
	ContinueOnError *bool `json:"continueOnError,omitempty" yaml:"continueOnError,omitempty"`
}

// Hook is a setup/teardown action: a step executed before/after a scope.
type Hook struct {
	Name  string `json:"name" yaml:"name"`
	Steps []Step `json:"steps,omitempty" yaml:"steps,omitempty"`
}

// Mock is one HTTP response the mock server can return. Path is a template:
// `{name}` matches a single segment. Status 0 means 200. Set Body or JSON,
// not both. DelayMs overrides the server --delay when greater than zero.
type Mock struct {
	Method  string            `json:"method" yaml:"method"`
	Path    string            `json:"path" yaml:"path"`
	Status  int               `json:"status,omitempty" yaml:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Body    string            `json:"body,omitempty" yaml:"body,omitempty"`
	JSON    any               `json:"json,omitempty" yaml:"json,omitempty"`
	DelayMs int               `json:"delayMs,omitempty" yaml:"delayMs,omitempty"`
	Match   *MockMatch        `json:"match,omitempty" yaml:"match,omitempty"`
}

// MockMatch narrows which request a Mock answers. Every set field must match.
// Header names are compared case-insensitively; values are exact.
type MockMatch struct {
	Query        map[string]string `json:"query,omitempty" yaml:"query,omitempty"`
	Headers      map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	BodyContains string            `json:"bodyContains,omitempty" yaml:"bodyContains,omitempty"`
}

// Suite is a collection of test cases sharing variables, auth, and hooks.
type Suite struct {
	Name string `json:"name" yaml:"name"`
	// Vars are suite-scoped variables.
	Vars map[string]string `json:"vars,omitempty" yaml:"vars,omitempty"`
	// Auth applies/overrides auth at suite scope.
	Auth *AuthRef `json:"auth,omitempty" yaml:"auth,omitempty"`
	// Setup runs before any case; Teardown runs after all cases.
	Setup    *Hook      `json:"setup,omitempty" yaml:"setup,omitempty"`
	Teardown *Hook      `json:"teardown,omitempty" yaml:"teardown,omitempty"`
	Cases    []TestCase `json:"cases" yaml:"cases"`
	// Parallel sets the per-suite case parallelism; 0 means inherit the
	// run-level setting.
	Parallel int `json:"parallel,omitempty" yaml:"parallel,omitempty"`
	// Schema overrides the project GraphQL schema for graphql steps in this
	// suite. A step schema replaces it.
	Schema *GraphQLSchema `json:"schema,omitempty" yaml:"schema,omitempty"`
}
