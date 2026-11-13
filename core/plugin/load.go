package plugin

import (
	"context"
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
	// Override lets the plugin replace an extension type that is already
	// registered (a built-in such as rest, bearer, or json). Without it only
	// the core gRPC stub may be replaced.
	Override bool `json:"override,omitempty"`
}

// PluginsFile is the on-disk shape of a plugins config.
type PluginsFile struct {
	Plugins []Manifest `json:"plugins"`
}

// Manager owns the lifecycle of all loaded plugin subprocesses so the host can
// shut them down cleanly at the end of a run.
type Manager struct {
	clients []*Client
	red     *redactorRef
}

// SetRedactor makes the plugins' stderr output pass through red (the run's
// redactor), so secrets the run resolves are masked in plugin logs too.
func (m *Manager) SetRedactor(red *secrets.Redactor) {
	if m == nil || red == nil {
		return
	}
	if m.red == nil {
		m.red = newRedactorRef()
	}
	m.red.set(red)
}

// LoadFile reads a plugins config file and loads every enabled plugin into the
// registry, returning a Manager that must be Closed when the host exits. It
// does not ask for trust: use it only for a file the user named explicitly.
// Auto-discovered project plugins go through LoadForProject.
func LoadFile(ctx context.Context, reg *registry.Registry, path string) (*Manager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manager{}, nil // no plugins configured is not an error
		}
		return nil, verr.Wrap(verr.CodePlugin, err, "read plugins file")
	}
	manifests, err := parsePluginsFile(path, data)
	if err != nil {
		return nil, err
	}
	return Load(ctx, reg, manifests)
}

// Load launches the given plugin manifests and registers them. Disabled
// manifests are skipped. On any failure, already-launched plugins are closed.
func Load(ctx context.Context, reg *registry.Registry, manifests []Manifest) (*Manager, error) {
	m := &Manager{red: newRedactorRef()}
	for _, man := range manifests {
		if man.Enabled != nil && !*man.Enabled {
			continue
		}
		name := man.Name
		if name == "" {
			name = filepath.Base(man.Path)
		}
		client, err := launch(ctx, man.Path, man.Args, newStderrWriter(hostStderr, name, m.red))
		if err != nil {
			m.Close()
			return nil, err
		}
		m.clients = append(m.clients, client)
		if err := checkOverride(reg, client.Handshake(), man); err != nil {
			m.Close()
			return nil, err
		}
		if err := register(reg, client); err != nil {
			m.Close()
			return nil, err
		}
	}
	return m, nil
}

// checkOverride refuses a plugin that would silently replace an extension
// type already in the registry, unless its manifest sets "override": true.
// The core gRPC protocol stub exists to be replaced by plugins/grpc.
func checkOverride(reg *registry.Registry, hs Handshake, man Manifest) error {
	if man.Override || (hs.Kind == "protocol" && hs.Type == "grpc") {
		return nil
	}
	var err error
	switch hs.Kind {
	case "protocol":
		_, err = reg.Protocol(hs.Type)
	case "assertion":
		_, err = reg.Assertion(hs.Type)
	case "datasource":
		_, err = reg.DataSource(hs.Type)
	case "auth":
		_, err = reg.AuthFactory(hs.Type)
	case "reporter":
		_, err = reg.Reporter(hs.Type)
	default:
		return nil // register reports the unknown kind
	}
	if err == nil {
		return verr.Newf(verr.CodePlugin,
			"plugin %q would replace the existing %s type %q; set \"override\": true in its plugins entry to allow it",
			man.Name, hs.Kind, hs.Type)
	}
	return nil
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
