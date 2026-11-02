package graphql

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Options controls schema loading during validate and the schema command.
// Registry applies the step's auth chain. NewClient builds the HTTP client
// from transport TLS; when it is nil, a plain client is used and mTLS is rejected.
// Timeout is the HTTP client budget and is applied even when NewClient is set.
// AuthSessions are process-only desktop OAuth sessions, keyed by auth name.
type Options struct {
	Environment  string
	Registry     *registry.Registry
	Timeout      time.Duration
	NewClient    func(*spi.TLSConfig) (*http.Client, error)
	AuthSessions map[string]*auth.OAuthSession
}

// StepLoc is one step in a loaded project.
type StepLoc struct {
	Suite     *model.Suite
	Case      *model.TestCase
	Step      *model.Step
	Path      string
	SuitePath string
}

// Walk visits setup, case, and teardown steps in project order.
func Walk(p *model.Project, fn func(StepLoc)) {
	if p == nil {
		return
	}
	for si := range p.Suites {
		s := &p.Suites[si]
		sp := fmt.Sprintf("suites[%d]", si)
		walkHook(s, sp, s.Setup, "setup", fn)
		for ci := range s.Cases {
			c := &s.Cases[ci]
			cp := fmt.Sprintf("%s.cases[%d]", sp, ci)
			for sti := range c.Steps {
				fn(StepLoc{
					Suite: s, Case: c, Step: &c.Steps[sti],
					Path: fmt.Sprintf("%s.steps[%d]", cp, sti), SuitePath: sp,
				})
			}
		}
		walkHook(s, sp, s.Teardown, "teardown", fn)
	}
}

func walkHook(s *model.Suite, sp string, h *model.Hook, name string, fn func(StepLoc)) {
	if h == nil {
		return
	}
	for i := range h.Steps {
		fn(StepLoc{
			Suite: s, Step: &h.Steps[i],
			Path: fmt.Sprintf("%s.%s.steps[%d]", sp, name, i), SuitePath: sp,
		})
	}
}

// Find returns steps whose name matches step, optionally narrowed by suite
// and case name.
func Find(p *model.Project, suite, caseName, step string) []StepLoc {
	var out []StepLoc
	Walk(p, func(loc StepLoc) {
		if loc.Step.Name != step {
			return
		}
		if suite != "" && (loc.Suite == nil || loc.Suite.Name != suite) {
			return
		}
		if caseName != "" && (loc.Case == nil || loc.Case.Name != caseName) {
			return
		}
		out = append(out, loc)
	})
	return out
}

// FindAt returns the case step at index.
func FindAt(p *model.Project, suite, caseName string, index int) (StepLoc, error) {
	if p == nil {
		return StepLoc{}, verr.New(verr.CodeNotFound, "project is missing")
	}
	for si := range p.Suites {
		s := &p.Suites[si]
		if s.Name != suite {
			continue
		}
		sp := fmt.Sprintf("suites[%d]", si)
		for ci := range s.Cases {
			c := &s.Cases[ci]
			if c.Name != caseName {
				continue
			}
			if index < 0 || index >= len(c.Steps) {
				return StepLoc{}, verr.New(verr.CodeNotFound, "step index is outside the case")
			}
			return StepLoc{
				Suite: s, Case: c, Step: &c.Steps[index],
				Path:      fmt.Sprintf("%s.cases[%d].steps[%d]", sp, ci, index),
				SuitePath: sp,
			}, nil
		}
		return StepLoc{}, verr.Newf(verr.CodeNotFound, "case %q not found", caseName)
	}
	return StepLoc{}, verr.Newf(verr.CodeNotFound, "suite %q not found", suite)
}

// ValidateProject checks GraphQL operations that have a schema source.
// File schemas are read from the project directory. Introspection uses the
// step URL with the step's auth and headers. Results are cached per URL and
// auth identity. The query, operation name, and variables are interpolated
// with the selected environment before they are checked.
func ValidateProject(ctx context.Context, p *model.Project, opt Options) []*verr.Error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	env, err := p.SelectEnvironment(opt.Environment)
	if err != nil {
		return []*verr.Error{verr.New(verr.CodeValidation, err.Error())}
	}
	cache := NewCache()
	red := secrets.NewRedactor()
	var errs []*verr.Error
	Walk(p, func(loc StepLoc) {
		if loc.Step.Protocol != "graphql" {
			return
		}
		src := Resolve(p.Schema, schemaOf(loc.Suite), loc.Step.Schema)
		if src == nil {
			return
		}
		schema, err := loadStepSchema(ctx, cache, p, loc, src, opt, red)
		if err != nil {
			errs = append(errs, asVerr(err).WithPath(schemaPath(p, loc, src)))
			return
		}
		interp := stepResolver(env, loc)
		q, opName, rawVars, ierr := interpolatedOperation(interp, loc.Step.Request)
		if ierr != nil {
			errs = append(errs, verr.Wrap(verr.CodeValidation, ierr, "interpolate request").WithPath(loc.Path+".request"))
			return
		}
		red.AddAll(interp.SecretValues())
		if strings.TrimSpace(q) == "" {
			errs = append(errs, verr.New(verr.CodeValidation, "GraphQL query is required").WithPath(loc.Path+".request.query"))
			return
		}
		if strings.Contains(q, "${") {
			// An unresolved placeholder that breaks the document is left for
			// run time. A document that still parses is checked.
			if _, problems := loadQuery(schema, q); len(problems) > 0 {
				return
			}
		}
		for _, item := range CheckQuery(schema, q, opName, rawVars) {
			path := loc.Path + ".request.query"
			if item.onVariables {
				path = loc.Path + ".request.variables"
			}
			e := verr.New(verr.CodeValidation, red.Redact(item.text())).WithPath(path)
			if item.line > 0 {
				e = e.WithDetail("line", item.line).WithDetail("column", item.col)
			}
			errs = append(errs, e)
		}
	})
	return errs
}

// interpolatedOperation resolves the query, operation name, and variables the
// way the runner does: non-strict, so an unknown ${...} is left in place.
func interpolatedOperation(interp *secrets.Resolver, raw map[string]any) (string, string, any, error) {
	if interp == nil {
		interp = secrets.NewResolver(nil)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	q, _ := raw["query"].(string)
	resolved, err := interp.Interpolate(q, false)
	if err != nil {
		return "", "", nil, err
	}
	op, _ := raw["operationName"].(string)
	opName, err := interp.Interpolate(op, false)
	if err != nil {
		return "", "", nil, err
	}
	vars, err := interp.InterpolateAny(raw["variables"], false)
	if err != nil {
		return "", "", nil, err
	}
	return resolved, opName, vars, nil
}

func stepResolver(env model.Environment, loc StepLoc) *secrets.Resolver {
	return secrets.NewResolver(scopeVars(env, loc))
}

// scopeVars is the runner's variable order for a step that has not run:
// environment, then suite, case, and step. Later scopes win.
func scopeVars(env model.Environment, loc StepLoc) map[string]string {
	vars := map[string]string{}
	for k, v := range env.Variables {
		vars[k] = v
	}
	if loc.Suite != nil {
		for k, v := range loc.Suite.Vars {
			vars[k] = v
		}
	}
	if loc.Case != nil {
		for k, v := range loc.Case.Vars {
			vars[k] = v
		}
	}
	if loc.Step != nil {
		for k, v := range loc.Step.Vars {
			vars[k] = v
		}
	}
	return vars
}

// StepSDL returns the SDL for one step's schema source.
func StepSDL(ctx context.Context, p *model.Project, loc StepLoc, opt Options) (string, error) {
	if loc.Step == nil || loc.Step.Protocol != "graphql" {
		return "", verr.New(verr.CodeValidation, "step is not a graphql step")
	}
	src := Resolve(p.Schema, schemaOf(loc.Suite), loc.Step.Schema)
	if src == nil {
		return "", verr.New(verr.CodeValidation, "no GraphQL schema configured for this step").WithPath(loc.Path)
	}
	cache := NewCache()
	red := secrets.NewRedactor()
	_, sdl, err := loadStepSDL(ctx, cache, p, loc, src, opt, red)
	if err != nil {
		return "", asVerr(err).WithPath(schemaPath(p, loc, src))
	}
	return red.Redact(sdl), nil
}

// StepTemplates lists operation templates for the step's schema.
func StepTemplates(ctx context.Context, p *model.Project, loc StepLoc, depth int, opt Options) ([]Template, error) {
	if loc.Step == nil {
		return nil, verr.New(verr.CodeValidation, "step is missing")
	}
	src := Resolve(p.Schema, schemaOf(loc.Suite), loc.Step.Schema)
	if src == nil {
		return nil, verr.New(verr.CodeValidation, "no GraphQL schema configured for this step").WithPath(loc.Path)
	}
	cache := NewCache()
	red := secrets.NewRedactor()
	schema, _, err := loadStepSDL(ctx, cache, p, loc, src, opt, red)
	if err != nil {
		return nil, asVerr(err)
	}
	if depth <= 0 {
		depth = 2
	}
	return ListTemplates(schema, depth)
}

func schemaOf(s *model.Suite) *model.GraphQLSchema {
	if s == nil {
		return nil
	}
	return s.Schema
}

func schemaPath(p *model.Project, loc StepLoc, src *model.GraphQLSchema) string {
	if loc.Step != nil && loc.Step.Schema != nil && src == loc.Step.Schema {
		return loc.Path + ".schema"
	}
	if loc.Suite != nil && loc.Suite.Schema != nil && src == loc.Suite.Schema {
		return loc.SuitePath + ".schema"
	}
	if p != nil && p.Schema != nil && src == p.Schema {
		return "schema"
	}
	return loc.Path + ".schema"
}

func loadStepSchema(ctx context.Context, cache *Cache, p *model.Project, loc StepLoc, src *model.GraphQLSchema, opt Options, red *secrets.Redactor) (*ast.Schema, error) {
	schema, _, err := loadStepSDL(ctx, cache, p, loc, src, opt, red)
	return schema, err
}

func loadStepSDL(ctx context.Context, cache *Cache, p *model.Project, loc StepLoc, src *model.GraphQLSchema, opt Options, red *secrets.Redactor) (*ast.Schema, string, error) {
	if src.File != "" && src.Introspect {
		return nil, "", verr.New(verr.CodeValidation, "GraphQL schema sets file and introspect; set one")
	}
	if src.File != "" {
		return cache.LoadFile(p.SourceDir, src.File)
	}
	if !src.Introspect {
		return nil, "", verr.New(verr.CodeValidation, "GraphQL schema requires file or introspect")
	}
	call, err := prepareCall(ctx, p, loc, opt, red)
	if err != nil {
		return nil, "", err
	}
	return cache.LoadIntrospectionSigned(ctx, call.client, call.req, call.sign, call.responders, call.authNames, red)
}

type preparedCall struct {
	client     *http.Client
	req        *spi.Request
	sign       Signer
	responders []spi.ChallengeResponder
	authNames  []string
}

func prepareCall(ctx context.Context, p *model.Project, loc StepLoc, opt Options, red *secrets.Redactor) (preparedCall, error) {
	env, err := p.SelectEnvironment(opt.Environment)
	if err != nil {
		return preparedCall{}, verr.New(verr.CodeValidation, err.Error())
	}
	if red == nil {
		red = secrets.NewRedactor()
	}
	interp := stepResolver(env, loc)
	raw := loc.Step.Request
	if raw == nil {
		raw = map[string]any{}
	}
	resolved, err := interp.InterpolateMap(raw, false)
	if err != nil {
		return preparedCall{}, verr.Wrap(verr.CodeValidation, err, "interpolate request")
	}
	red.AddAll(interp.SecretValues())
	endpoint, _ := resolved["url"].(string)
	if endpoint == "" || strings.Contains(endpoint, "${") {
		return preparedCall{}, verr.New(verr.CodeValidation, "GraphQL URL is unresolved; select an environment or use a schema file")
	}
	sreq := &spi.Request{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: headerMap(resolved["headers"]),
		Query:   map[string]string{},
	}
	var responders []spi.ChallengeResponder
	var authNames []string
	var authHeaders []string
	var sign Signer
	if opt.Registry != nil {
		var caseAuth *model.AuthRef
		if loc.Case != nil {
			caseAuth = loc.Case.Auth
		}
		var suiteAuth *model.AuthRef
		if loc.Suite != nil {
			suiteAuth = loc.Suite.Auth
		}
		resolver := auth.NewResolver(opt.Registry, p, red, interp, p.SourceDir, auth.ResolverOptions{Sessions: opt.AuthSessions})
		chain, err := resolver.Resolve(ctx, auth.ScopeRefs{
			Environment: env.Auth,
			Suite:       suiteAuth,
			Case:        caseAuth,
			Step:        loc.Step.Auth,
		})
		if err != nil {
			return preparedCall{}, verr.Wrap(verr.CodeValidation, err, "resolve auth")
		}
		before := sreq.Headers.Clone()
		if err := chain.Apply(ctx, sreq); err != nil {
			return preparedCall{}, verr.New(verr.CodeAuth, red.Redact("apply auth: "+err.Error()))
		}
		red.AddAll(interp.SecretValues())
		responders = chain.ChallengeResponders()
		authNames = chain.Names()
		authHeaders = auth.AddedHeaders(before, sreq.Headers)
		if chain.Signs() {
			sign = chain.Sign
		}
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var client *http.Client
	if opt.NewClient != nil {
		client, err = opt.NewClient(sreq.TLS)
		if err != nil {
			return preparedCall{}, verr.Wrap(verr.CodeValidation, err, "build HTTP client")
		}
		if client == nil {
			return preparedCall{}, verr.New(verr.CodeValidation, "build HTTP client")
		}
		client.Timeout = timeout
	} else if sreq.TLS != nil {
		return preparedCall{}, verr.New(verr.CodeValidation, "introspection with mTLS needs a TLS client")
	} else {
		client = &http.Client{Timeout: timeout}
	}
	auth.GuardRedirects(client, sign != nil, authHeaders)
	return preparedCall{client: client, req: sreq, sign: sign, responders: responders, authNames: authNames}, nil
}

func headerMap(v any) http.Header {
	h := http.Header{}
	switch m := v.(type) {
	case map[string]any:
		for k, val := range m {
			if val == nil {
				continue
			}
			h.Set(k, fmt.Sprint(val))
		}
	case map[string]string:
		for k, val := range m {
			h.Set(k, val)
		}
	}
	return h
}

// AssertionInput is what the runner passes to a graphql_schema assertion.
type AssertionInput struct {
	Schema    *ast.Schema
	Query     string
	Operation string
	Err       error
}

func asVerr(err error) *verr.Error {
	if err == nil {
		return nil
	}
	if e, ok := verr.As(err); ok {
		return e
	}
	return verr.New(verr.CodeValidation, err.Error())
}
