// Package registry is Verity's central extension registry. Protocols,
// assertions, reporters, auth providers, and data sources are registered here
// by stable type identifier and resolved by the runner at execution time.
//
// The registry holds no behavior of its own and no protocol/auth/test logic; it
// is a typed lookup table. First-party extensions register through the same API
// as third-party plugins, so there is one code path for all extensions.
//
// A Registry instance is explicit (no package-level singleton) so that the CLI,
// GUI, and tests can each build an isolated registry with exactly the
// extensions they need — supporting the "lean base, opt-in plugins" goal and
// avoiding hidden global state.
package registry

import (
	"sort"
	"sync"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Registry stores extensions keyed by type. It is safe for concurrent use.
type Registry struct {
	mu          sync.RWMutex
	protocols   map[string]spi.Protocol
	assertions  map[string]spi.Assertion
	reporters   map[string]spi.Reporter
	authFactory map[string]spi.AuthFactory
	dataSources map[string]spi.DataSource
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{
		protocols:   map[string]spi.Protocol{},
		assertions:  map[string]spi.Assertion{},
		reporters:   map[string]spi.Reporter{},
		authFactory: map[string]spi.AuthFactory{},
		dataSources: map[string]spi.DataSource{},
	}
}

// RegisterProtocol adds a protocol plugin, keyed by its Type().
func (r *Registry) RegisterProtocol(p spi.Protocol) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.protocols[p.Type()] = p
}

// RegisterAssertion adds an assertion plugin, keyed by its Type().
func (r *Registry) RegisterAssertion(a spi.Assertion) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.assertions[a.Type()] = a
}

// RegisterReporter adds a reporter plugin, keyed by its Type().
func (r *Registry) RegisterReporter(rep spi.Reporter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reporters[rep.Type()] = rep
}

// RegisterAuth adds an auth provider factory, keyed by type.
func (r *Registry) RegisterAuth(typ string, f spi.AuthFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authFactory[typ] = f
}

// RegisterDataSource adds a data source plugin, keyed by its Type().
func (r *Registry) RegisterDataSource(d spi.DataSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dataSources[d.Type()] = d
}

// Protocol returns the protocol for typ, or a not_found error.
func (r *Registry) Protocol(typ string) (spi.Protocol, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.protocols[typ]
	if !ok {
		return nil, verr.Newf(verr.CodeNotFound, "no protocol registered for type %q", typ)
	}
	return p, nil
}

// Assertion returns the assertion for typ, or a not_found error.
func (r *Registry) Assertion(typ string) (spi.Assertion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.assertions[typ]
	if !ok {
		return nil, verr.Newf(verr.CodeNotFound, "no assertion registered for type %q", typ)
	}
	return a, nil
}

// Reporter returns the reporter for typ, or a not_found error.
func (r *Registry) Reporter(typ string) (spi.Reporter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rep, ok := r.reporters[typ]
	if !ok {
		return nil, verr.Newf(verr.CodeNotFound, "no reporter registered for type %q", typ)
	}
	return rep, nil
}

// AuthFactory returns the auth factory for typ, or a not_found error.
func (r *Registry) AuthFactory(typ string) (spi.AuthFactory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.authFactory[typ]
	if !ok {
		return nil, verr.Newf(verr.CodeNotFound, "no auth provider registered for type %q", typ)
	}
	return f, nil
}

// DataSource returns the data source for typ, or a not_found error.
func (r *Registry) DataSource(typ string) (spi.DataSource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.dataSources[typ]
	if !ok {
		return nil, verr.Newf(verr.CodeNotFound, "no data source registered for type %q", typ)
	}
	return d, nil
}

// Catalog is a snapshot of registered extension type identifiers, used by the
// CLI/GUI to introspect capabilities and by AI agents to discover what's
// available.
type Catalog struct {
	Protocols   []string `json:"protocols"`
	Assertions  []string `json:"assertions"`
	Reporters   []string `json:"reporters"`
	Auth        []string `json:"auth"`
	DataSources []string `json:"dataSources"`
}

// Catalog returns a sorted snapshot of all registered type identifiers.
func (r *Registry) Catalog() Catalog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Catalog{
		Protocols:   keys(r.protocols),
		Assertions:  keys(r.assertions),
		Reporters:   keys(r.reporters),
		Auth:        keysAuth(r.authFactory),
		DataSources: keys(r.dataSources),
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysAuth(m map[string]spi.AuthFactory) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
