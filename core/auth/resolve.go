package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
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
			return fmt.Errorf("transport auth %q: %w", p.Type(), err)
		}
	}
	for _, p := range c.Message {
		if err := p.Apply(ctx, req); err != nil {
			return fmt.Errorf("message auth %q: %w", p.Type(), err)
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

	mu       sync.Mutex
	cache    map[string]spi.AuthProvider
	sessions map[string]*OAuthSession
	// httpTransport, when set, is installed on providers that dial their own
	// HTTP connections (OAuth2 token requests).
	httpTransport http.RoundTripper
}

// httpTransportSetter is implemented by providers that open their own HTTP
// connections. The method is unexported; only this package installs it.
type httpTransportSetter interface {
	useHTTPTransport(rt http.RoundTripper)
}

// SetHTTPTransport installs the transport used by providers that dial on
// their own. Call it before Resolve. A nil transport leaves each provider's
// default client in place.
func (r *Resolver) SetHTTPTransport(rt http.RoundTripper) {
	r.mu.Lock()
	r.httpTransport = rt
	r.mu.Unlock()
}

func (r *Resolver) attachTransport(prov spi.AuthProvider) {
	if r.httpTransport == nil || prov == nil {
		return
	}
	if s, ok := prov.(httpTransportSetter); ok {
		s.useHTTPTransport(r.httpTransport)
	}
}

// ResolverOptions supplies optional process-only workspace sessions.
type ResolverOptions struct {
	Sessions map[string]*OAuthSession
}

// NewResolver creates an auth resolver. interp interpolates ${...} in config
// values (so secrets resolve from env at run time); dir is the project source
// directory used to resolve file-based credentials (mTLS certs).
func NewResolver(reg *registry.Registry, project *model.Project, red *secrets.Redactor, interp *secrets.Resolver, dir string, opts ...ResolverOptions) *Resolver {
	var sessions map[string]*OAuthSession
	if len(opts) > 0 {
		sessions = opts[0].Sessions
	}
	return &Resolver{
		reg: reg, project: project, red: red, interp: interp, dir: dir,
		cache: map[string]spi.AuthProvider{}, sessions: sessions,
	}
}

// ScopeRefs holds the AuthRef declared at each scope, outermost first. Nil
// entries mean "no auth declared at this scope".
type ScopeRefs struct {
	Environment *model.AuthRef
	Suite       *model.AuthRef
	Case        *model.AuthRef
	Step        *model.AuthRef
}

// Resolve computes the effective ordered list of auth names for the given
// scopes, applying inheritance and override rules, then builds the provider
// chain.
//
// Inheritance rules (per scope, from outer to inner):
//   - If a scope declares no AuthRef, it inherits the parent's effective list.
//   - If a scope declares an AuthRef with Inherit=false (default when Use is
//     non-empty and Inherit unset is true), it replaces... see effectiveNames.
func (r *Resolver) Resolve(ctx context.Context, scopes ScopeRefs) (*Chain, error) {
	names := effectiveNames(scopes)
	return r.build(ctx, names)
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
func (r *Resolver) build(_ context.Context, names []string) (*Chain, error) {
	chain := &Chain{names: append([]string{}, names...)}
	for _, name := range names {
		prov, err := r.provider(name)
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
func (r *Resolver) provider(name string) (spi.AuthProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p, ok := r.cache[name]; ok {
		return p, nil
	}
	def, ok := r.project.AuthByName(name)
	if !ok {
		return nil, fmt.Errorf("auth definition %q not found", name)
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
	cfg, err := r.interp.InterpolateMap(def.Config, false)
	r.red.AddAll(r.interp.SecretValues())
	if err != nil {
		return nil, fmt.Errorf("interpolate auth %q config: %w", name, err)
	}
	cfg["__dir"] = r.dir
	prov, err := factory(cfg, r.red)
	if err != nil {
		return nil, fmt.Errorf("build auth %q (%s): %w", name, def.Type, err)
	}
	r.attachTransport(prov)
	r.cache[name] = prov
	return prov, nil
}
