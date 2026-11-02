package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Manifest describes an external plugin to load. A host reads a list of these
// (e.g. from .verity/plugins.json) and launches each one. The manifest format
// is published as a JSON Schema so AI agents can author it (see ManifestSchema).
type Manifest struct {
	// Name is an informational identifier for logs.
	Name string `json:"name"`
	// Path is the plugin executable path.
	Path string `json:"path"`
	// Args are optional arguments passed to the plugin process.
	Args []string `json:"args,omitempty"`
	// Enabled allows disabling a plugin without removing its entry.
	Enabled *bool `json:"enabled,omitempty"`
}

// PluginsFile is the on-disk shape of a plugins config.
type PluginsFile struct {
	Plugins []Manifest `json:"plugins"`
}

// Manager owns the lifecycle of all loaded plugin subprocesses so the host can
// shut them down cleanly at the end of a run.
type Manager struct {
	clients []*Client
}

// LoadFile reads a plugins config file and loads every enabled plugin into the
// registry, returning a Manager that must be Closed when the host exits.
func LoadFile(ctx context.Context, reg *registry.Registry, path string) (*Manager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manager{}, nil // no plugins configured is not an error
		}
		return nil, verr.Wrap(verr.CodePlugin, err, "read plugins file")
	}
	var pf PluginsFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "parse plugins file")
	}
	// Resolve relative plugin paths against the plugins file's directory so a
	// project's .verity/plugins.json can reference binaries portably.
	baseDir := filepath.Dir(path)
	for i := range pf.Plugins {
		if p := pf.Plugins[i].Path; p != "" && !filepath.IsAbs(p) {
			pf.Plugins[i].Path = filepath.Join(baseDir, p)
		}
	}
	return Load(ctx, reg, pf.Plugins)
}

// Load launches the given plugin manifests and registers them. Disabled
// manifests are skipped. On any failure, already-launched plugins are closed.
func Load(ctx context.Context, reg *registry.Registry, manifests []Manifest) (*Manager, error) {
	m := &Manager{}
	for _, man := range manifests {
		if man.Enabled != nil && !*man.Enabled {
			continue
		}
		client, err := Launch(ctx, man.Path, man.Args...)
		if err != nil {
			m.Close()
			return nil, err
		}
		m.clients = append(m.clients, client)
		if err := register(reg, client); err != nil {
			m.Close()
			return nil, err
		}
	}
	return m, nil
}

// register installs a launched plugin into the registry based on its handshake
// Kind. Each kind maps to the matching proxy type.
func register(reg *registry.Registry, c *Client) error {
	hs := c.Handshake()
	switch hs.Kind {
	case "protocol":
		reg.RegisterProtocol(&protocolProxy{client: c, typ: hs.Type})
	case "assertion":
		reg.RegisterAssertion(&assertionProxy{client: c, typ: hs.Type})
	case "datasource":
		reg.RegisterDataSource(&dataSourceProxy{client: c, typ: hs.Type})
	case "auth":
		// Auth plugins register a factory that produces an auth proxy bound to
		// the resolved config; the proxy forwards Apply over the wire.
		reg.RegisterAuth(hs.Type, func(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
			return &authProxy{client: c, typ: hs.Type, config: config, red: red}, nil
		})
	case "reporter":
		reg.RegisterReporter(&reporterProxy{client: c, typ: hs.Type})
	default:
		return verr.Newf(verr.CodePlugin, "unknown plugin kind %q", hs.Kind)
	}
	return nil
}

// Close shuts down all loaded plugins.
func (m *Manager) Close() {
	for _, c := range m.clients {
		_ = c.Close()
	}
	m.clients = nil
}
