package secrets

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// ExternalProvider resolves an external secret by key. Implementations might
// read a file, a vault, or an environment variable. They must never persist
// resolved values to disk.
type ExternalProvider interface {
	// Resolve returns the secret value for key and whether it was found.
	Resolve(key string) (string, bool, error)
}

var (
	extMu        sync.RWMutex
	extProviders = map[string]ExternalProvider{
		"env":      envProvider{},
		"file":     fileProvider{},
		"keychain": &Keychain{},
	}
)

// RegisterExternalProvider registers a named external secret provider. It is
// safe for concurrent use and intended to be called during init by plugins.
func RegisterExternalProvider(name string, p ExternalProvider) {
	extMu.Lock()
	defer extMu.Unlock()
	extProviders[name] = p
}

// resolveExternalSecret parses "provider:key" and dispatches to the provider.
// When no provider prefix is present, the "env" provider is used.
func resolveExternalSecret(spec string) (string, bool, error) {
	provider, key := "env", spec
	if i := strings.Index(spec, ":"); i >= 0 {
		provider, key = spec[:i], spec[i+1:]
	}
	extMu.RLock()
	p, ok := extProviders[provider]
	extMu.RUnlock()
	if !ok {
		return "", false, fmt.Errorf("unknown secret provider %q", provider)
	}
	return p.Resolve(key)
}

type envProvider struct{}

func (envProvider) Resolve(key string) (string, bool, error) {
	v, ok := os.LookupEnv(key)
	return v, ok, nil
}

type fileProvider struct{}

// Resolve reads the entire file at key and returns its trimmed contents. Useful
// for Docker/Kubernetes secret mounts (e.g. /run/secrets/token).
func (fileProvider) Resolve(key string) (string, bool, error) {
	b, err := os.ReadFile(key)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read secret file %q: %w", key, err)
	}
	return strings.TrimSpace(string(b)), true, nil
}
