package secrets

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/verr"
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
		return "", false, verr.Newf(verr.CodeConfig, "unknown secret provider %q", provider)
	}
	return p.Resolve(key)
}

type envProvider struct{}

func (envProvider) Resolve(key string) (string, bool, error) {
	v, ok := os.LookupEnv(key)
	return v, ok, nil
}

// fileProvider is registered so "file" is a known provider name. A project's
// ${secret:file:PATH} never reaches it: the Resolver reads the file itself
// under its project-directory policy (see readSecretFile). Direct callers of
// the provider registry get the same confinement with no project directory,
// which refuses every path.
type fileProvider struct{}

func (fileProvider) Resolve(key string) (string, bool, error) {
	return readSecretFile("", key, false)
}

// readSecretFile reads a secret file and returns its trimmed contents. Useful
// for Docker/Kubernetes secret mounts (e.g. /run/secrets/token), which are
// outside a project and therefore need --allow-external-files. A relative
// path resolves against the project directory dir.
func readSecretFile(dir, path string, allowExternal bool) (string, bool, error) {
	resolved, err := fsutil.ResolveInside(dir, path, allowExternal, "secret file")
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			// A missing file is an error, not an unset value: a lenient
			// caller would otherwise send the literal ${secret:file:…} text.
			return "", false, verr.Newf(verr.CodeConfig, "secret file %q does not exist", path)
		}
		return "", false, verr.Context(verr.CodeConfig, err, fmt.Sprintf("read secret file %q", path))
	}
	return strings.TrimSpace(string(b)), true, nil
}
