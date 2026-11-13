package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Chain is an ordered, resolved set of AuthProviders to apply to a request.
// Transport providers are applied first (they set up TLS), then message
// providers in declared order, so e.g. mTLS + OAuth bearer compose correctly.
type Chain struct {
	Transport []spi.AuthProvider
	Message   []spi.AuthProvider
	names     []string
}

// Names returns the auth definition names in this chain, in apply order.
func (c *Chain) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.names))
	copy(out, c.names)
	return out
}

// Apply runs every provider in the chain against req, transport layer first.
func (c *Chain) Apply(ctx context.Context, req *spi.Request) error {
	for _, p := range c.Transport {
		if err := p.Apply(ctx, req); err != nil {
			return verr.Context(verr.CodeAuth, err, fmt.Sprintf("transport auth %q", p.Type()))
		}
	}
	for _, p := range c.Message {
		if err := p.Apply(ctx, req); err != nil {
			return verr.Context(verr.CodeAuth, err, fmt.Sprintf("message auth %q", p.Type()))
		}
	}
	return nil
}

// ChallengeResponders returns the optional HTTP challenge handlers in order.
func (c *Chain) ChallengeResponders() []spi.ChallengeResponder {
	var out []spi.ChallengeResponder
	for _, p := range c.Message {
		if responder, ok := p.(spi.ChallengeResponder); ok {
			out = append(out, responder)
		}
	}
	return out
}

// Resolver builds auth chains from the project's named AuthDefs, honoring scope
// inheritance and override. It instantiates each provider once and caches it so
// OAuth token caches are shared across steps that reference the same auth.
type Resolver struct {
	reg     *registry.Registry
	project *model.Project
	red     *secrets.Redactor
	interp  *secrets.Resolver
	dir     string
	// allowExternal lets file-based credentials read outside dir.
	allowExternal bool

	mu       sync.Mutex
	cache    map[string]spi.AuthProvider
	sessions map[string]*OAuthSession
	// perCase records auth names whose config references values that only
	// a case knows (a token a login step captured). Their providers are
	// cached by name plus a hash of the resolved config, never by name.
	perCase map[string]bool
	// tokenClient, when set, is installed on providers that open their own
	// HTTP connections (OAuth2 token requests).
	tokenClient TokenClient
}

// TokenClient is how providers that open their own HTTP connections (OAuth2
// token requests) reach the network: the run's transport settings.
type TokenClient struct {
	// Transport returns the round tripper for a request. tlsCfg is the TLS
	// contribution of the step's transport-layer auth (mTLS), or nil. The
	// runner returns its shared transport for that config, so token requests
	// get the same proxy handling, CA bundle, client certificate, and
	// dial guard (the load private-address check) as the step itself.
	Transport func(tlsCfg *spi.TLSConfig) (http.RoundTripper, error)
	// Timeout bounds one token request (the run's --timeout). Zero keeps
	// the provider default (30s).
	Timeout time.Duration
}

// tokenClientSetter is implemented by providers that open their own HTTP
// connections. The method is unexported; only this package installs it.
type tokenClientSetter interface {
	useTokenClient(tc TokenClient)
}

// SetTokenClient installs the network settings for providers that dial on
// their own. Call it before Resolve.
func (r *Resolver) SetTokenClient(tc TokenClient) {
	r.mu.Lock()
	r.tokenClient = tc
	r.mu.Unlock()
}

// SetHTTPTransport makes providers that dial on their own use rt for every
// token request, whatever the step's TLS config. A nil transport keeps the
// current settings.
func (r *Resolver) SetHTTPTransport(rt http.RoundTripper) {
	if rt == nil {
		return
	}
	r.mu.Lock()
	r.tokenClient.Transport = func(*spi.TLSConfig) (http.RoundTripper, error) { return rt, nil }
	r.mu.Unlock()
}

func (r *Resolver) attachTransport(prov spi.AuthProvider) {
	if r.tokenClient.Transport == nil || prov == nil {
		return
	}
	if s, ok := prov.(tokenClientSetter); ok {
		s.useTokenClient(r.tokenClient)
	}
}

// ResolverOptions supplies optional process-only workspace sessions.
type ResolverOptions struct {
	Sessions map[string]*OAuthSession
	// AllowExternalFiles lets file-based credentials (mTLS certFile/keyFile/
	// caFile) read outside the project directory.
	AllowExternalFiles bool
}

// NewResolver creates an auth resolver. interp interpolates ${...} in config
// values (so secrets resolve from env at run time); dir is the project source
// directory used to resolve file-based credentials (mTLS certs).
func NewResolver(reg *registry.Registry, project *model.Project, red *secrets.Redactor, interp *secrets.Resolver, dir string, opts ...ResolverOptions) *Resolver {
	var sessions map[string]*OAuthSession
	allowExternal := false
	if len(opts) > 0 {
		sessions = opts[0].Sessions
		allowExternal = opts[0].AllowExternalFiles
	}
	return &Resolver{
		reg: reg, project: project, red: red, interp: interp, dir: dir,
		cache: map[string]spi.AuthProvider{}, sessions: sessions, allowExternal: allowExternal,
		perCase: map[string]bool{},
	}
}

// ScopeRefs holds the AuthRef declared at each scope, outermost first. Nil
// entries mean "no auth declared at this scope".
type ScopeRefs struct {
	Environment *model.AuthRef
	Suite       *model.AuthRef
	Case        *model.AuthRef
	Step        *model.AuthRef
	// Vars resolves auth config references that the run-level variables
	// (environment, --var, secrets) leave open, such as a token captured by
	// an earlier step of the case. Nil means only run-level variables.
	Vars *secrets.Resolver
}

// Resolve computes the effective ordered list of auth names for the given
// scopes, applying inheritance and override rules, then builds the provider
// chain.
//
// Inheritance rules (per scope, from outer to inner): a scope without an
// AuthRef inherits the parent's list; an AuthRef stacks its Use onto the
// inherited list (Inherit unset or true) or replaces it (Inherit false). See
// effectiveNames.
func (r *Resolver) Resolve(ctx context.Context, scopes ScopeRefs) (*Chain, error) {
	names := effectiveNames(scopes)
	return r.build(ctx, names, scopes.Vars)
}

// effectiveNames computes the ordered, de-duplicated list of auth def names in
// effect after applying inheritance/override across scopes.
//
// Semantics:
//   - Start with an empty list.
//   - At each scope (env → suite → case → step), if AuthRef is nil, keep the
//     inherited list unchanged.
//   - If AuthRef is present: when Inherit is true (the default), append this
//     scope's Use to the inherited list (stacking). When Inherit is false,
//     replace the inherited list with this scope's Use (full override). An
//     empty Use with Inherit=false clears auth (explicit "none").
func effectiveNames(s ScopeRefs) []string {
	var current []string
	apply := func(ref *model.AuthRef) {
		if ref == nil {
			return
		}
		inherit := true
		if ref.Inherit != nil {
			inherit = *ref.Inherit
		}
		if inherit {
			current = append(append([]string{}, current...), ref.Use...)
		} else {
			current = append([]string{}, ref.Use...)
		}
	}
	apply(s.Environment)
	apply(s.Suite)
	apply(s.Case)
	apply(s.Step)
	return dedupe(current)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, n := range in {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// build instantiates providers for the named auth defs and orders them by layer.
func (r *Resolver) build(_ context.Context, names []string, vars *secrets.Resolver) (*Chain, error) {
	chain := &Chain{names: append([]string{}, names...)}
	for _, name := range names {
		prov, err := r.provider(name, vars)
		if err != nil {
			return nil, err
		}
		switch prov.Layer() {
		case spi.LayerTransport:
			chain.Transport = append(chain.Transport, prov)
		default:
			chain.Message = append(chain.Message, prov)
		}
	}
	return chain, nil
}

// provider instantiates (or returns a cached) provider for an auth def name.
func (r *Resolver) provider(name string, vars *secrets.Resolver) (spi.AuthProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p, ok := r.cache[name]; ok {
		return p, nil
	}
	def, ok := r.project.AuthByName(name)
	if !ok {
		return nil, verr.Newf(verr.CodeNotFound, "auth definition %q not found", name)
	}
	if session := r.sessions[name]; session != nil && def.Type == "oauth2" && strConfig(def.Config, "grant") == "authorization_code" {
		prov := session.Provider(r.red)
		r.attachTransport(prov)
		r.cache[name] = prov
		return prov, nil
	}
	factory, err := r.reg.AuthFactory(def.Type)
	if err != nil {
		return nil, err
	}
	// Interpolate config values (resolving ${env:...} secrets at run time) and
	// inject the project dir for file-based credentials.
	if r.perCase[name] {
		return r.caseProvider(name, def, factory, vars)
	}
	cfg, err := r.interp.InterpolateMap(def.Config, false)
	r.red.AddAll(r.interp.SecretValues())
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("interpolate auth %q config", name))
	}
	// Run-level variables leave a reference open (for example a token a
	// login step captures): resolve this auth per case instead.
	if _, _, ok := unresolvedRef(def.Type, cfg); ok {
		r.perCase[name] = true
		return r.caseProvider(name, def, factory, vars)
	}
	return r.finish(name, name, def, factory, cfg)
}

// caseProvider resolves def's config with the case's variables and returns
// the provider for that resolved config. Providers are cached by name plus
// a hash of the resolved config, so two cases with different captured
// tokens never share one, while steps that resolve to the same config do
// (an OAuth token cache stays shared). The hash input is never logged.
func (r *Resolver) caseProvider(name string, def model.AuthDef, factory spi.AuthFactory, vars *secrets.Resolver) (spi.AuthProvider, error) {
	interp := vars
	if interp == nil {
		interp = r.interp
	}
	cfg, err := interp.InterpolateMap(def.Config, false)
	r.red.AddAll(interp.SecretValues())
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("interpolate auth %q config", name))
	}
	if key, ref, ok := unresolvedRef(def.Type, cfg); ok {
		return nil, verr.Newf(verr.CodeAuth,
			"auth %q: config %q references ${%s}, which is not defined when this step runs (environment variables, --var, ${env:NAME}, secrets, and values captured by earlier steps of the case are)",
			name, key, ref).WithDetail("auth", name).WithDetail("reference", ref)
	}
	key, err := configKey(name, cfg)
	if err != nil {
		return nil, verr.Wrap(verr.CodeInternal, err, fmt.Sprintf("auth %q config", name))
	}
	if p, ok := r.cache[key]; ok {
		return p, nil
	}
	return r.finish(name, key, def, factory, cfg)
}

// configKey is the cache key for a per-case provider: the auth name and a
// SHA-256 of the resolved config. The config holds secrets; only the hash
// is kept.
func configKey(name string, cfg map[string]any) (string, error) {
	b, err := json.Marshal(cfg) // map keys are sorted, so equal configs hash equally
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return name + "\x00" + hex.EncodeToString(sum[:]), nil
}

// finish builds the provider for an interpolated config and caches it.
func (r *Resolver) finish(name, key string, def model.AuthDef, factory spi.AuthFactory, cfg map[string]any) (spi.AuthProvider, error) {
	cfg["__dir"] = r.dir
	cfg[fsutil.AllowExternalKey] = r.allowExternal
	prov, err := factory(cfg, r.red)
	if err != nil {
		return nil, verr.Context(verr.CodeConfig, err, fmt.Sprintf("build auth %q (%s)", name, def.Type))
	}
	r.attachTransport(prov)
	r.cache[key] = prov
	return prov, nil
}

// hmacTemplateRefs are the placeholders the hmac provider fills in its own
// "message" template after project interpolation.
var hmacTemplateRefs = map[string]bool{
	"method": true, "path": true, "query": true, "body": true, "timestamp": true, "nonce": true,
}

// unresolvedRef finds a ${...} reference left in an interpolated auth config
// and returns the top-level key and the reference text.
func unresolvedRef(typ string, cfg map[string]any) (key, ref string, found bool) {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		allowed := func(string) bool { return false }
		if typ == "hmac" && k == "message" {
			allowed = func(name string) bool { return hmacTemplateRefs[name] }
		}
		if r, ok := findRef(cfg[k], allowed); ok {
			return k, r, true
		}
	}
	return "", "", false
}

func findRef(v any, allowed func(string) bool) (string, bool) {
	switch t := v.(type) {
	case string:
		rest := t
		for {
			i := strings.Index(rest, "${")
			if i < 0 {
				return "", false
			}
			j := strings.IndexByte(rest[i:], '}')
			if j < 0 {
				return "", false
			}
			name := strings.TrimSpace(rest[i+2 : i+j])
			if !allowed(name) {
				return name, true
			}
			rest = rest[i+j+1:]
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if r, ok := findRef(t[k], allowed); ok {
				return r, true
			}
		}
	case []any:
		for _, e := range t {
			if r, ok := findRef(e, allowed); ok {
				return r, true
			}
		}
	}
	return "", false
}
