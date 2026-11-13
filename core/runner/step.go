package runner

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/graphql"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/protocols"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// stepOpts is per-case execution state that must not live on the shared
// execContext: load testing runs many cases at once, and each one has its
// own HTTP transport handle and its own run-scoped variable sink.
type stepOpts struct {
	// transport, when non-nil, is reused for HTTP steps that do not carry
	// their own TLS config. Nil builds a client per step, as Runner.Run does.
	transport http.RoundTripper
	// onRunVar, when non-nil, receives run-scoped transfers instead of the
	// shared runVars map. The step's resolver is updated either way.
	onRunVar func(name, value string)
}

// runStep executes a single step: resolve request config, build the protocol
// request, apply the auth chain, execute, capture (redacted), run assertions,
// and perform property transfers (which update the shared resolver).
//
// When the step sets retry, execution and assertions repeat until the policy
// matches or the attempt budget is spent. Transfers run only on the final
// attempt. DurationMS is the total, including delays. The request and response
// captures are from the final attempt.
func (e *execContext) runStep(ctx context.Context, scope runScope, resolver *secrets.Resolver, authResolver *auth.Resolver, opts stepOpts) (result spi.StepResult) {
	defer func() {
		e.red.AddAll(resolver.SecretValues())
		e.redactStepMessages(&result)
	}()
	step := scope.step
	start := time.Now()
	base := spi.StepResult{Name: step.Name, Protocol: step.Protocol}

	// Apply step-local vars once, in sorted name order, on a child resolver
	// so they stay local to this step (property transfers still write the
	// case resolver). They are not part of an attempt, and a bad variable is
	// a config error rather than something a retry would fix.
	caseResolver := resolver
	if len(step.Vars) > 0 {
		resolver = resolver.Child()
		names := make([]string, 0, len(step.Vars))
		for k := range step.Vars {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			iv, err := resolver.Interpolate(step.Vars[k], false)
			e.red.AddAll(resolver.SecretValues())
			if err != nil {
				base.Status = spi.StatusError
				base.Error = e.errInfo(verr.Wrap(verr.CodeConfig, err, "interpolate step var "+k))
				stampStep(&base, start)
				return base
			}
			resolver.Set(k, iv)
		}
	}

	proto, err := e.runner.reg.Protocol(step.Protocol)
	if err != nil {
		base.Status = spi.StatusError
		base.Error = e.errInfo(err)
		stampStep(&base, start)
		return base
	}

	attempts := stepAttempts(step)
	delay := time.Duration(0)
	until := ""
	if step.Retry != nil {
		delay = milliseconds(step.Retry.DelayMs)
		until = step.Retry.Until
	}
	timeout := e.timeout
	if step.TimeoutMs > 0 {
		timeout = milliseconds(step.TimeoutMs)
	}

	var last spi.StepResult
	for n := 1; n <= attempts; n++ {
		if err := ctx.Err(); err != nil {
			// A cancel before the first attempt means this step never started.
			// A cancel between attempts keeps the previous attempt and fails
			// the step; it is not reported as aborted.
			if n == 1 {
				base.Status = spi.StatusAborted
				stampStep(&base, start)
				return base
			}
			last.Status = spi.StatusError
			last.Error = e.errInfo(verr.Wrap(verr.CodeTransport, err, "retry cancelled"))
			stampStep(&last, start)
			return last
		}

		attemptCtx, red, done := e.red.AttemptScope(ctx)
		ar := e.doAttempt(attemptCtx, scope, resolver, authResolver, proto, timeout, opts, red)
		last = ar.res
		last.Attempts = n
		if last.Error != nil {
			last.Error.Message = red.Redact(last.Error.Message)
		}
		// Parent cancellation is not a retryable transport error. The HTTP
		// client's own timeout does not cancel this context.
		if n == attempts || ctx.Err() != nil || !shouldRetry(until, ar) {
			if ar.resp != nil {
				e.applyTransfers(&last, ar.resp, step, caseResolver, opts, red)
			}
			if last.Error != nil {
				last.Error.Message = red.Redact(last.Error.Message)
			}
			done()
			stampStep(&last, start)
			return last
		}
		done()
		if err := waitRetry(ctx, delay); err != nil {
			last.Status = spi.StatusError
			last.Error = e.errInfo(verr.Wrap(verr.CodeTransport, err, "retry delay cancelled"))
			stampStep(&last, start)
			return last
		}
	}
	stampStep(&last, start)
	return last
}

// stampStep records the step's elapsed time. DurationMS stays the truncated
// value run reports publish; Duration keeps microseconds for load metrics.
func stampStep(st *spi.StepResult, start time.Time) {
	st.Duration = time.Since(start)
	st.DurationMS = st.Duration.Milliseconds()
}

// attemptResult is one execution of a step, before property transfers.
type attemptResult struct {
	res spi.StepResult
	// resp is set when the protocol returned a response. Transport failures
	// leave it nil.
	resp *spi.Response
	// transport is true when Execute failed for a reason a retry may help
	// (timeout, connection failure). Config and plugin errors are not
	// transport failures, and neither is a cancelled run context.
	transport bool
}

// doAttempt interpolates, builds, authenticates, and executes the step once,
// then evaluates assertions. It does not transfer variables or set DurationMS.
func (e *execContext) doAttempt(ctx context.Context, scope runScope, resolver *secrets.Resolver, authResolver *auth.Resolver, proto spi.Protocol, timeout time.Duration, opts stepOpts, red *secrets.Redactor) (out attemptResult) {
	step := scope.step
	res := spi.StepResult{Name: step.Name, Protocol: step.Protocol}
	defer func() {
		e.red.AddAll(resolver.SecretValues())
		if red != nil {
			redactStepMessagesWith(red, &out.res)
		} else {
			e.redactStepMessages(&out.res)
		}
	}()

	rawReq, err := resolver.InterpolateMap(step.Request, false)
	e.red.AddAll(resolver.SecretValues())
	if err != nil {
		res.Status = spi.StatusError
		res.Error = e.errInfo(verr.Wrap(verr.CodeConfig, err, "interpolate request"))
		out.res = res
		return out
	}

	req, err := proto.BuildRequest(ctx, rawReq)
	if err != nil {
		res.Status = spi.StatusError
		res.Error = e.errInfo(verr.Wrap(verr.CodeConfig, err, "build request"))
		out.res = res
		return out
	}
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	// Uploads are part of the bytes a signer hashes. Finalize them before
	// auth so SigV4 and HMAC see the body that will be sent. Each attempt
	// builds a new request and applies auth again, which re-signs.
	if err := protocols.FinalizeBody(e.project.SourceDir, e.opts.AllowExternalFiles, req); err != nil {
		res.Status = spi.StatusError
		res.Error = e.errInfo(err)
		out.res = res
		return out
	}

	// Resolve and apply the auth chain across scopes (env -> suite -> case -> step).
	chain, err := authResolver.Resolve(ctx, auth.ScopeRefs{
		Environment: e.env.Auth,
		Suite:       scope.suite.Auth,
		Case:        scope.tcase.Auth,
		Step:        step.Auth,
	})
	if err != nil {
		res.Status = spi.StatusError
		res.Error = e.errInfo(verr.Wrap(verr.CodeAuth, err, "resolve auth"))
		out.res = res
		return out
	}
	beforeAuth := req.Headers.Clone()
	if err := chain.Apply(ctx, req); err != nil {
		res.Status = spi.StatusError
		res.Error = e.errInfo(verr.Wrap(verr.CodeAuth, err, "apply auth"))
		out.res = res
		return out
	}
	authHeaders := auth.AddedHeaders(beforeAuth, req.Headers)

	// Register any secrets the resolver gathered (env-resolved values) so they
	// are redacted in captures.
	red.AddAll(resolver.SecretValues())

	// Capture before execution so a transport-setup failure still records the
	// request. Uploads fill the body during Execute; recapture afterwards.
	res.Request = e.captureRequest(req, red)

	// Build an HTTP client honoring transport-level TLS from the auth chain.
	// timeout is this attempt's budget, not a budget shared across retries.
	// A shared transport is only safe when this step has no TLS config of
	// its own: mTLS material must not leak into the pool other steps use.
	shared := opts.transport
	if req.TLS != nil {
		shared = nil
	}
	client, err := buildHTTPClient(req.TLS, timeout, e.opts.FollowRedirects, shared, e.opts.DialContext, e.tlsCache, requestServerName(req.URL))
	if err != nil {
		res.Status = spi.StatusError
		res.Error = e.errInfo(verr.Wrap(verr.CodeTransport, err, "build transport"))
		out.res = res
		return out
	}
	// Auth headers never follow a redirect to another origin, and a signed
	// request does not follow redirects at all.
	auth.GuardRedirects(client, chain.Signs(), authHeaders)

	pctx := &spi.ProtocolContext{ChallengeResponders: chain.ChallengeResponders(), Client: client, Redactor: red, Dir: e.project.SourceDir, AllowExternalFiles: e.opts.AllowExternalFiles}
	resp, err := proto.Execute(ctx, pctx, req)
	res.Request = e.captureRequest(req, red)
	if err != nil {
		res.Status = spi.StatusError
		// A missing upload is a config error. Network failures stay transport,
		// including protocols that already returned some other verr code.
		// plugin_error (the gRPC stub) is reported as transport for compatibility
		// with that wrapping, but it is not retried: another attempt cannot help.
		if ve, ok := verr.As(err); ok && (ve.Code == verr.CodeConfig || ve.Code == verr.CodeAuth) {
			res.Error = e.errInfo(err)
		} else if ve, ok := verr.As(err); ok && ve.Code == verr.CodePlugin {
			res.Error = e.errInfo(verr.Wrap(verr.CodeTransport, err, "execute request"))
		} else {
			res.Error = e.errInfo(verr.Wrap(verr.CodeTransport, err, "execute request"))
			if ctx.Err() == nil {
				out.transport = true
			}
		}
		out.res = res
		return out
	}

	// Strings nested under a secret-named JSON key ({"token": {"value": ...}})
	// are secrets too; register them before anything is captured.
	if leaves := secrets.JSONSecretLeaves(resp.Body); len(leaves) > 0 {
		red.AddAll(leaves)
	}
	res.Response = e.captureResponse(resp, red)
	res.ResponseDuration = resp.Duration

	var gqlIn *graphql.AssertionInput
	if needsGraphQLSchema(step) {
		gqlIn = e.graphqlInput(ctx, scope, rawReq, req, client, chain, chain.ChallengeResponders(), chain.Names(), red)
	}
	shown := e.shownBody(&res, resp, red)
	allPassed := true
	for ai := range step.Assertions {
		var extra map[string]any
		if step.Assertions[ai].Type == "graphql_schema" {
			extra = map[string]any{"__gql": gqlIn}
		}
		ar := e.runAssertion(ctx, step.Assertions[ai], resp, resolver, extra, red)
		e.redactAssertion(ar, step.Assertions[ai], resp, shown, red)
		if !ar.Passed {
			allPassed = false
		}
		res.Assertions = append(res.Assertions, *ar)
	}
	if allPassed {
		res.Status = spi.StatusPassed
	} else {
		res.Status = spi.StatusFailed
	}
	out.res = res
	out.resp = resp
	return out
}

// applyTransfers captures variables from the final attempt and folds transfer
// failures into the step status. Earlier attempts must not call this: a
// transfer writes the shared resolver, which later steps (and later attempts)
// would observe.
func (e *execContext) applyTransfers(res *spi.StepResult, resp *spi.Response, step *model.Step, resolver *secrets.Resolver, opts stepOpts, red *secrets.Redactor) {
	allPassed := res.Status == spi.StatusPassed
	newSecret := false
	shown := e.shownBody(res, resp, red)
	for _, t := range step.Transfers {
		val, terr := extractTransfer(t, resp)
		if terr != nil && t.Default == "" {
			res.Assertions = append(res.Assertions, spi.AssertionResult{
				Passed: false, Type: "transfer", Description: t.Name,
				Message: red.Redact(terr.Error()),
			})
			allPassed = false
			continue
		}
		resolver.Set(t.Name, val)
		if t.Scope == model.TransferScopeRun {
			// A load-test virtual user keeps run-scoped captures to itself.
			// The functional runner records them on the shared map so later
			// cases in the same run can see them.
			if opts.onRunVar != nil {
				opts.onRunVar(t.Name, val)
			} else {
				e.setRunVar(t.Name, val)
			}
		}
		// A captured credential is itself secret: one named like a credential
		// (token, password, Authorization…) or taken from a part of the body
		// that redaction masks. Register it so it is masked wherever later
		// requests send it, and never show it in the capture list.
		sensitive := sensitiveName.MatchString(t.Name) || sensitiveName.MatchString(t.Expr) ||
			(val != "" && strings.Contains(string(resp.Body), val) && !strings.Contains(shown(), val))
		shown := red.Redact(val)
		if sensitive && val != "" {
			red.AddAll([]string{val})
			shown = secrets.Mask
			newSecret = true
		}
		res.Captures = append(res.Captures, spi.Capture{Name: t.Name, Value: shown})
	}

	if newSecret {
		// The response was captured before these values were known secrets;
		// capture it again so other copies of them are masked too, and redact
		// the assertion results again for the same reason.
		res.Response = e.captureResponse(resp, red)
		for i := range res.Assertions {
			ar := &res.Assertions[i]
			ar.Message = red.Redact(ar.Message)
			ar.Expected = red.RedactValue(ar.Expected)
			ar.Actual = red.RedactValue(ar.Actual)
		}
	}
	if allPassed {
		res.Status = spi.StatusPassed
	} else {
		res.Status = spi.StatusFailed
	}
}

// shouldRetry reports whether another attempt is warranted. until "" (no
// policy, or an unknown value that skipped validation) does not retry.
func shouldRetry(until string, ar attemptResult) bool {
	switch until {
	case model.RetryUntilPassed:
		return ar.transport || ar.res.Status == spi.StatusFailed
	case model.RetryUntilStatus:
		if ar.transport {
			return true
		}
		if ar.resp == nil {
			return false
		}
		code := ar.resp.StatusCode
		return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
	default:
		return false
	}
}

// stepAttempts is 1 + retry count, and never more than 1+MaxRetryCount.
// Validation rejects a larger count; the clamp is a backstop for a caller
// that runs a project without validating it.
func stepAttempts(step *model.Step) int {
	if step.Retry == nil || step.Retry.Count <= 0 {
		return 1
	}
	n := step.Retry.Count
	if n > model.MaxRetryCount {
		n = model.MaxRetryCount
	}
	return 1 + n
}

// milliseconds converts a non-negative millisecond count to a duration.
// Values that would overflow time.Duration saturate at the maximum duration.
func milliseconds(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	maxMS := int(time.Duration(math.MaxInt64) / time.Millisecond)
	if ms > maxMS {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ms) * time.Millisecond
}

// waitRetry pauses between attempts. A delay of 0 returns immediately.
// Cancelling the run context ends the pause and returns the context error.
func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// sensitiveName matches transfer names and source expressions that denote
// credentials.
var sensitiveName = regexp.MustCompile(`(?i)(token|secret|passw|api[_-]?key|apikey|authorization|cookie|session|credential|private)`)

// redactAssertion masks secrets in an assertion result before it reaches the
// GUI, events or reports. Expected/Actual are raw values taken from the
// response, so a value is masked outright when the expression names a
// credential ($.token) or when it appears in the raw body but not in the
// redacted body capture (a structural pattern hid it there). Everything else
// goes through the redactor.
func (e *execContext) redactAssertion(ar *spi.AssertionResult, a model.Assertion, resp *spi.Response, shownBody func() string, red *secrets.Redactor) {
	hidden := func(s string) bool {
		return len(s) >= 4 && strings.Contains(string(resp.Body), s) && !strings.Contains(shownBody(), s)
	}
	// maskAll hides v and every string inside it, in Message too.
	maskAll := func(v any) any {
		for _, s := range stringLeaves(v) {
			if s != "" {
				ar.Message = strings.ReplaceAll(ar.Message, s, secrets.Mask)
			}
		}
		return secrets.Mask
	}
	mask := func(v any) any {
		switch t := v.(type) {
		case nil, bool:
			return v
		case string:
			if t != "" && (sensitiveName.MatchString(a.Expr) || hidden(t)) {
				return maskAll(t)
			}
			return red.Redact(t)
		}
		// A credential-like path ($.token) can hold an object or array whose
		// keys look ordinary ({"value": "..."}), so mask it whole.
		if sensitiveName.MatchString(a.Expr) {
			return maskAll(v)
		}
		if b, err := json.Marshal(v); err == nil && hidden(string(b)) {
			return maskAll(v)
		}
		return red.RedactValue(v)
	}
	ar.Actual = mask(ar.Actual)
	ar.Expected = red.RedactValue(ar.Expected)
	ar.Message = red.Redact(ar.Message)
}

// stringLeaves returns every string inside a JSON-like value.
func stringLeaves(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case map[string]any:
		var out []string
		for _, e := range t {
			out = append(out, stringLeaves(e)...)
		}
		return out
	case []any:
		var out []string
		for _, e := range t {
			out = append(out, stringLeaves(e)...)
		}
		return out
	}
	return nil
}

// needsGraphQLSchema reports whether a step has a graphql_schema assertion.
func needsGraphQLSchema(step *model.Step) bool {
	for _, a := range step.Assertions {
		if a.Type == "graphql_schema" {
			return true
		}
	}
	return false
}

// graphqlInput loads the step's schema. Introspection uses the same URL,
// headers, cookies, and client as the step. Headers are not recorded.
func (e *execContext) graphqlInput(ctx context.Context, scope runScope, raw map[string]any, req *spi.Request, client *http.Client, chain *auth.Chain, responders []spi.ChallengeResponder, authNames []string, red *secrets.Redactor) *graphql.AssertionInput {
	q, _ := raw["query"].(string)
	op, _ := raw["operationName"].(string)
	in := &graphql.AssertionInput{Query: q, Operation: op}
	if scope.step.Protocol != "graphql" {
		in.Err = verr.New(verr.CodeValidation, "graphql_schema applies to a graphql step")
		return in
	}
	var suiteSchema *model.GraphQLSchema
	if scope.suite != nil {
		suiteSchema = scope.suite.Schema
	}
	src := graphql.Resolve(e.project.Schema, suiteSchema, scope.step.Schema)
	if src == nil {
		in.Err = verr.New(verr.CodeValidation, "no GraphQL schema configured for this step")
		return in
	}
	if e.gql == nil {
		e.gql = graphql.NewCache()
	}
	switch {
	case src.File != "" && src.Introspect:
		in.Err = verr.New(verr.CodeValidation, "GraphQL schema sets file and introspect; set one")
	case src.File != "":
		schema, _, err := e.gql.LoadFile(e.project.SourceDir, src.File)
		if err != nil {
			in.Err = err
			return in
		}
		in.Schema = schema
	case src.Introspect:
		var sign graphql.Signer
		if chain.Signs() {
			sign = chain.Sign
		}
		schema, _, err := e.gql.LoadIntrospectionSigned(ctx, client, req, sign, responders, authNames, red)
		if err != nil {
			in.Err = err
			return in
		}
		in.Schema = schema
	default:
		in.Err = verr.New(verr.CodeValidation, "GraphQL schema requires file or introspect")
	}
	return in
}

// bodyAssertions are the built-in assertion types that read the response
// body; they cannot give a trustworthy answer on a truncated body.
var bodyAssertions = map[string]bool{
	"jsonpath": true, "xpath": true, "json_schema": true, "xsd": true,
	"contains": true, "regex": true, "graphql_schema": true,
}

// runAssertion resolves the assertion plugin and evaluates it. The expected
// value is interpolated so assertions can reference variables/transfers.
func (e *execContext) runAssertion(ctx context.Context, a model.Assertion, resp *spi.Response, resolver *secrets.Resolver, extra map[string]any, red *secrets.Redactor) *spi.AssertionResult {
	if truncated, _ := resp.Meta[spi.MetaTruncated].(bool); truncated && bodyAssertions[a.Type] {
		return &spi.AssertionResult{Passed: false, Type: a.Type, Description: a.Description,
			Message: "response body exceeded the size limit and was truncated; body assertions cannot be evaluated"}
	}
	impl, err := e.runner.reg.Assertion(a.Type)
	if err != nil {
		return &spi.AssertionResult{Passed: false, Type: a.Type, Description: a.Description,
			Message: err.Error()}
	}
	expr, err := resolver.Interpolate(a.Expr, false)
	e.red.AddAll(resolver.SecretValues())
	if err != nil {
		return &spi.AssertionResult{Passed: false, Type: a.Type, Description: a.Description,
			Message: "interpolate expr: " + err.Error()}
	}
	value := a.Value
	if s, ok := value.(string); ok {
		iv, err := resolver.Interpolate(s, false)
		e.red.AddAll(resolver.SecretValues())
		if err != nil {
			return &spi.AssertionResult{Passed: false, Type: a.Type, Description: a.Description,
				Message: "interpolate value: " + err.Error()}
		}
		value = iv
	}
	cfg := make(map[string]any, len(a.Config)+1)
	for k, v := range a.Config {
		cfg[k] = v
	}
	// Provide the project dir so schema assertions can resolve file paths.
	cfg["__dir"] = e.project.SourceDir
	cfg[fsutil.AllowExternalKey] = e.opts.AllowExternalFiles
	for k, v := range extra {
		cfg[k] = v
	}

	spec := spi.AssertionSpec{
		Description: a.Description,
		Expr:        expr,
		Op:          a.Op,
		Value:       value,
		Config:      cfg,
	}
	actx := &spi.AssertionContext{Response: resp, Redactor: red}
	ar, err := impl.Evaluate(ctx, actx, spec)
	if err != nil {
		return &spi.AssertionResult{Passed: false, Type: a.Type, Description: a.Description,
			Message: red.Redact(err.Error())}
	}
	return ar
}
