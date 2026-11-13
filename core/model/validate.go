package model

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/verr"
)

// Validate performs semantic validation of a loaded project, returning a slice
// of structured errors (empty when valid). It checks required fields, unique
// names, resolvable auth references, and basic step well-formedness.
//
// Validate is intentionally protocol-agnostic: it does not validate protocol-
// specific request bodies (that is the protocol plugin's job at run time).
func (p *Project) Validate() []*verr.Error {
	return p.validate(true)
}

// ValidateForMock is Validate for `verity mock`. A file that declares mocks
// and no suites is servable; `verity run` and `verity validate` still
// require at least one suite.
func (p *Project) ValidateForMock() []*verr.Error {
	return p.validate(false)
}

func (p *Project) validate(requireSuites bool) []*verr.Error {
	var errs []*verr.Error

	if p.SchemaVersion == "" {
		errs = append(errs, verr.New(verr.CodeValidation,
			"schemaVersion is required").WithPath("schemaVersion"))
	}
	if p.Name == "" {
		errs = append(errs, verr.New(verr.CodeValidation,
			"project name is required").WithPath("name"))
	}
	errs = append(errs, validateGraphQLSchema(p.Schema, "schema")...)

	// Environment names must be unique.
	seenEnv := map[string]bool{}
	defaultCount := 0
	for i, e := range p.Environments {
		path := fmt.Sprintf("environments[%d]", i)
		if e.Name == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"environment name is required").WithPath(path))
		}
		if seenEnv[e.Name] {
			errs = append(errs, verr.Newf(verr.CodeValidation,
				"duplicate environment name %q", e.Name).WithPath(path))
		}
		seenEnv[e.Name] = true
		if e.Default {
			defaultCount++
		}
	}
	if defaultCount > 1 {
		errs = append(errs, verr.New(verr.CodeValidation,
			"at most one environment may be marked default").WithPath("environments"))
	}

	// Auth definitions: unique names, type present.
	authNames := map[string]bool{}
	for i, a := range p.Auth {
		path := fmt.Sprintf("auth[%d]", i)
		if a.Name == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"auth name is required").WithPath(path))
		}
		if a.Type == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"auth type is required").WithPath(path))
		}
		if authNames[a.Name] {
			errs = append(errs, verr.Newf(verr.CodeValidation,
				"duplicate auth name %q", a.Name).WithPath(path))
		}
		authNames[a.Name] = true
		errs = append(errs, validateAuthConfig(a, path)...)
	}

	// Validate environment auth references against defined auth defs.
	for i, e := range p.Environments {
		if e.Auth != nil {
			path := fmt.Sprintf("environments[%d].auth", i)
			errs = append(errs, validateAuthRef(e.Auth, authNames, path)...)
		}
	}

	if requireSuites && len(p.Suites) == 0 {
		errs = append(errs, verr.New(verr.CodeValidation,
			"project must declare at least one suite").WithPath("suites"))
	}

	suiteNames := map[string]bool{}
	for si, s := range p.Suites {
		spath := fmt.Sprintf("suites[%d]", si)
		if s.Name == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"suite name is required").WithPath(spath))
		}
		if suiteNames[s.Name] {
			errs = append(errs, verr.Newf(verr.CodeValidation,
				"duplicate suite name %q", s.Name).WithPath(spath))
		}
		suiteNames[s.Name] = true

		errs = append(errs, validateAuthRef(s.Auth, authNames, spath+".auth")...)
		errs = append(errs, validateGraphQLSchema(s.Schema, spath+".schema")...)
		if s.Setup != nil {
			errs = append(errs, validateHook(s.Setup, authNames, spath+".setup")...)
		}
		if s.Teardown != nil {
			errs = append(errs, validateHook(s.Teardown, authNames, spath+".teardown")...)
		}

		if len(s.Cases) == 0 {
			errs = append(errs, verr.New(verr.CodeValidation,
				"suite must declare at least one case").WithPath(spath+".cases"))
		}
		for ci, c := range s.Cases {
			cpath := fmt.Sprintf("%s.cases[%d]", spath, ci)
			if c.Name == "" {
				errs = append(errs, verr.New(verr.CodeValidation,
					"case name is required").WithPath(cpath))
			}
			errs = append(errs, validateAuthRef(c.Auth, authNames, cpath+".auth")...)
			if c.Feed != nil {
				errs = append(errs, validateFeed(c.Feed, cpath+".feed")...)
			}
			if len(c.Steps) == 0 {
				errs = append(errs, verr.New(verr.CodeValidation,
					"case must declare at least one step").WithPath(cpath+".steps"))
			}
			for sti, st := range c.Steps {
				stpath := fmt.Sprintf("%s.steps[%d]", cpath, sti)
				errs = append(errs, validateStep(st, authNames, stpath)...)
			}
		}
	}
	for i, m := range p.Mocks {
		errs = append(errs, validateMock(m, fmt.Sprintf("mocks[%d]", i))...)
	}
	return errs
}

func validateStep(st Step, authNames map[string]bool, path string) []*verr.Error {
	var errs []*verr.Error
	if st.Name == "" {
		errs = append(errs, verr.New(verr.CodeValidation,
			"step name is required").WithPath(path))
	}
	if st.Protocol == "" {
		errs = append(errs, verr.New(verr.CodeValidation,
			"step protocol is required").WithPath(path+".protocol"))
	}
	errs = append(errs, validateAuthRef(st.Auth, authNames, path+".auth")...)
	errs = append(errs, validateGraphQLSchema(st.Schema, path+".schema")...)
	for ai, a := range st.Assertions {
		apath := fmt.Sprintf("%s.assertions[%d]", path, ai)
		if a.Type == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"assertion type is required").WithPath(apath))
		}
		if a.Op != "" && !AssertionOps[a.Op] {
			errs = append(errs, verr.Newf(verr.CodeValidation,
				"assertion op %q is not one of %s", a.Op, strings.Join(sortedKeys(AssertionOps), ", ")).WithPath(apath+".op"))
		}
	}
	for ti, t := range st.Transfers {
		tpath := fmt.Sprintf("%s.transfers[%d]", path, ti)
		if t.Name == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"transfer name is required").WithPath(tpath))
		}
		if t.From == "" {
			errs = append(errs, verr.New(verr.CodeValidation,
				"transfer 'from' is required").WithPath(tpath))
		} else if !TransferSources[t.From] {
			errs = append(errs, verr.Newf(verr.CodeValidation,
				"transfer 'from' %q is not one of %s", t.From, strings.Join(sortedKeys(TransferSources), ", ")).WithPath(tpath+".from"))
		}
		if t.Scope != "" && t.Scope != TransferScopeCase && t.Scope != TransferScopeRun {
			errs = append(errs, verr.New(verr.CodeValidation,
				fmt.Sprintf("transfer scope %q must be %q or %q", t.Scope, TransferScopeCase, TransferScopeRun)).WithPath(tpath))
		}
	}
	errs = append(errs, validateRetry(st, path)...)
	return errs
}

func validateRetry(st Step, path string) []*verr.Error {
	var errs []*verr.Error
	if st.TimeoutMs < 0 {
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"timeoutMs must be >= 0, got %d", st.TimeoutMs).WithPath(path+".timeoutMs"))
	}
	if st.Retry == nil {
		return errs
	}
	rpath := path + ".retry"
	if st.Retry.Count < 0 || st.Retry.Count > MaxRetryCount {
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"retry count must be from 0 to %d (total attempts are 1+count), got %d",
			MaxRetryCount, st.Retry.Count).WithPath(rpath+".count"))
	}
	if st.Retry.DelayMs < 0 {
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"retry delayMs must be >= 0, got %d", st.Retry.DelayMs).WithPath(rpath+".delayMs"))
	}
	switch st.Retry.Until {
	case RetryUntilPassed, RetryUntilStatus:
	default:
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"retry until must be %q or %q, got %q",
			RetryUntilPassed, RetryUntilStatus, st.Retry.Until).WithPath(rpath+".until"))
	}
	return errs
}

// validateGraphQLSchema checks that a schema source sets file or introspect,
// and not both. Reading the file and checking operations happens in core/graphql.
func validateGraphQLSchema(s *GraphQLSchema, path string) []*verr.Error {
	if s == nil {
		return nil
	}
	if s.File != "" && s.Introspect {
		return []*verr.Error{verr.New(verr.CodeValidation,
			"GraphQL schema sets file and introspect; set one").WithPath(path)}
	}
	if s.File == "" && !s.Introspect {
		return []*verr.Error{verr.New(verr.CodeValidation,
			"GraphQL schema requires file or introspect").WithPath(path)}
	}
	if strings.ContainsAny(s.File, "\r\n") {
		return []*verr.Error{verr.New(verr.CodeValidation,
			"GraphQL schema file path must be a single line").WithPath(path + ".file")}
	}
	return nil
}

func validateHook(h *Hook, authNames map[string]bool, path string) []*verr.Error {
	var errs []*verr.Error
	for i, st := range h.Steps {
		errs = append(errs, validateStep(st, authNames, fmt.Sprintf("%s.steps[%d]", path, i))...)
	}
	return errs
}

// validateAuthConfig checks built-in signing providers. Other types, including
// plugins, are left to their factories.
func validateAuthConfig(a AuthDef, path string) []*verr.Error {
	switch a.Type {
	case "aws_sigv4":
		return validateSigV4Config(a.Config, path+".config")
	case "hmac":
		return validateHMACConfig(a.Config, path+".config")
	default:
		return nil
	}
}

func validateSigV4Config(cfg map[string]any, path string) []*verr.Error {
	errs := unknownAuthKeys("aws_sigv4", cfg, path, "accessKeyId", "secretAccessKey", "sessionToken", "region", "service", "unsignedPayload")
	if strings.TrimSpace(authString(cfg, "region")) == "" {
		errs = append(errs, verr.New(verr.CodeValidation, "aws_sigv4 requires region").WithPath(path+".region"))
	}
	if strings.TrimSpace(authString(cfg, "service")) == "" {
		errs = append(errs, verr.New(verr.CodeValidation, "aws_sigv4 requires service").WithPath(path+".service"))
	}
	for _, key := range []string{"accessKeyId", "secretAccessKey", "sessionToken", "region", "service"} {
		if cfg == nil {
			break
		}
		if _, ok := cfg[key]; ok {
			if _, ok := cfg[key].(string); !ok {
				errs = append(errs, verr.Newf(verr.CodeValidation, "aws_sigv4 %s must be a string", key).WithPath(path+"."+key))
			}
		}
	}
	if cfg != nil {
		if v, ok := cfg["unsignedPayload"]; ok && v != nil {
			if _, ok := v.(bool); !ok {
				errs = append(errs, verr.New(verr.CodeValidation, "aws_sigv4 unsignedPayload must be a boolean").WithPath(path+".unsignedPayload"))
			}
		}
	}
	return errs
}

func validateHMACConfig(cfg map[string]any, path string) []*verr.Error {
	errs := unknownAuthKeys("hmac", cfg, path, "secret", "algorithm", "header", "encoding", "prefix", "message", "timestampHeader", "nonceHeader")
	if authString(cfg, "secret") == "" {
		errs = append(errs, verr.New(verr.CodeValidation, "hmac requires secret").WithPath(path+".secret"))
	}
	if alg := strings.ToLower(strings.TrimSpace(authString(cfg, "algorithm"))); alg != "" {
		switch alg {
		case "sha1", "sha256", "sha512":
		default:
			errs = append(errs, verr.New(verr.CodeValidation, "hmac algorithm must be sha1, sha256, or sha512").WithPath(path+".algorithm"))
		}
	}
	if enc := strings.ToLower(strings.TrimSpace(authString(cfg, "encoding"))); enc != "" {
		switch enc {
		case "hex", "base64":
		default:
			errs = append(errs, verr.New(verr.CodeValidation, "hmac encoding must be hex or base64").WithPath(path+".encoding"))
		}
	}
	for _, key := range []string{"secret", "algorithm", "header", "encoding", "prefix", "message", "timestampHeader", "nonceHeader"} {
		if cfg == nil {
			break
		}
		if _, ok := cfg[key]; ok {
			if _, ok := cfg[key].(string); !ok {
				errs = append(errs, verr.Newf(verr.CodeValidation, "hmac %s must be a string", key).WithPath(path+"."+key))
			}
		}
	}
	return errs
}

// unknownAuthKeys reports config keys a built-in provider does not read, so a
// misspelt field (nonceHeaader, or secretAccessKey falling back to the
// environment) is an error instead of being ignored.
func unknownAuthKeys(typ string, cfg map[string]any, path string, known ...string) []*verr.Error {
	var errs []*verr.Error
	allowed := map[string]bool{}
	for _, k := range known {
		allowed[k] = true
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			errs = append(errs, verr.Newf(verr.CodeValidation, "%s has no setting %q", typ, k).WithPath(path+"."+k))
		}
	}
	return errs
}

func authString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	v, ok := cfg[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func validateAuthRef(ref *AuthRef, authNames map[string]bool, path string) []*verr.Error {
	if ref == nil {
		return nil
	}
	var errs []*verr.Error
	for i, name := range ref.Use {
		if !authNames[name] {
			errs = append(errs, verr.Newf(verr.CodeNotFound,
				"auth reference %q does not match any defined auth", name).
				WithPath(fmt.Sprintf("%s.use[%d]", path, i)))
		}
	}
	return errs
}

func validateMock(m Mock, path string) []*verr.Error {
	var errs []*verr.Error
	if !knownMockMethod(m.Method) {
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"mock method must be GET, POST, PUT, PATCH, DELETE, HEAD, or OPTIONS, got %q",
			m.Method).WithPath(path+".method"))
	}
	if err := mockPathError(m.Path); err != "" {
		errs = append(errs, verr.New(verr.CodeValidation, err).WithPath(path+".path"))
	}
	if m.Status != 0 && (m.Status < 100 || m.Status > 599) {
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"mock status must be from 100 to 599, got %d", m.Status).WithPath(path+".status"))
	}
	if m.DelayMs < 0 {
		errs = append(errs, verr.Newf(verr.CodeValidation,
			"mock delayMs must be >= 0, got %d", m.DelayMs).WithPath(path+".delayMs"))
	}
	if m.Body != "" && m.JSON != nil {
		errs = append(errs, verr.New(verr.CodeValidation,
			"mock must set body or json, not both").WithPath(path))
	}
	return errs
}

func knownMockMethod(m string) bool {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

// mockPathError reports why p is not a mock path template, or "" when it is.
func mockPathError(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") {
		return "mock path must start with /"
	}
	rest := p
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			if strings.Contains(rest, "}") {
				return "mock path has an unmatched }"
			}
			return ""
		}
		if strings.Contains(rest[:i], "}") {
			return "mock path has an unmatched }"
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return "mock path has an unmatched {"
		}
		name := rest[i+1 : i+j]
		if name == "" || strings.ContainsAny(name, "{}/ ") {
			return "mock path parameter must be a non-empty name"
		}
		rest = rest[i+j+1:]
	}
}

func validateFeed(f *Feed, path string) []*verr.Error {
	var errs []*verr.Error
	if f.Type == "" {
		errs = append(errs, verr.New(verr.CodeValidation,
			"feed type is required").WithPath(path+".type"))
	}
	if f.Path == "" && len(f.Inline) == 0 {
		errs = append(errs, verr.New(verr.CodeValidation,
			"feed must set either 'path' or 'inline'").WithPath(path))
	}
	return errs
}
