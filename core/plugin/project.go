package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/verr"
)

// TrustEnv is the environment variable that trusts a project's
// auto-discovered plugins file, for CI. "1" or "true" trusts it.
const TrustEnv = "VERITY_TRUST_PLUGINS"

// ProjectFile returns the auto-discovered plugins file path for a project
// directory: <dir>/.verity/plugins.json.
func ProjectFile(projectDir string) string {
	return filepath.Join(projectDir, ".verity", "plugins.json")
}

// Fingerprint identifies one auto-discovered plugins file so a trust decision
// can be remembered and is invalidated when the file or its location changes.
type Fingerprint struct {
	// Path is the absolute plugins file path.
	Path string `json:"path"`
	// SHA256 is the hex SHA-256 of the file content.
	SHA256 string `json:"sha256"`
	// Binaries are the resolved executable paths of the enabled plugins.
	Binaries []string `json:"binaries"`
}

// Key returns a stable identifier combining path, content hash, and binaries.
func (f Fingerprint) Key() string {
	h := sha256.New()
	h.Write([]byte(f.Path))
	h.Write([]byte{0})
	h.Write([]byte(f.SHA256))
	for _, b := range f.Binaries {
		h.Write([]byte{0})
		h.Write([]byte(b))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TrustFunc decides whether an auto-discovered plugins file may be executed.
// It is not consulted for a plugins file the caller named explicitly.
type TrustFunc func(Fingerprint) (bool, error)

// TrustAlways trusts every plugins file (the CLI's --trust-plugins).
func TrustAlways(Fingerprint) (bool, error) { return true, nil }

// TrustFromEnv trusts a plugins file when VERITY_TRUST_PLUGINS is "1" or "true".
func TrustFromEnv(Fingerprint) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(TrustEnv))) {
	case "1", "true":
		return true, nil
	}
	return false, nil
}

// LoadForProject loads plugins for a project. A non-empty explicitPath is the
// caller's explicit choice and is loaded as is. Otherwise an existing
// <projectDir>/.verity/plugins.json is loaded only when trust approves its
// fingerprint; a nil trust, or a refusal, returns a config_error naming the
// file without starting any plugin. No plugins file returns an empty Manager.
func LoadForProject(ctx context.Context, reg *registry.Registry, projectDir, explicitPath string, trust TrustFunc) (*Manager, error) {
	if explicitPath != "" {
		return LoadFile(ctx, reg, explicitPath)
	}
	auto := ProjectFile(projectDir)
	if abs, err := filepath.Abs(auto); err == nil {
		auto = abs
	}
	data, err := os.ReadFile(auto)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Manager{}, nil
		}
		return nil, verr.Wrap(verr.CodePlugin, err, "read plugins file")
	}
	manifests, err := parsePluginsFile(auto, data)
	if err != nil {
		return nil, err
	}
	fp := fingerprintOf(auto, data, manifests)
	ok := false
	if trust != nil {
		ok, err = trust(fp)
		if err != nil {
			return nil, err
		}
	}
	if !ok {
		e := verr.Newf(verr.CodeConfig,
			"project plugins file %s is not trusted: its plugins run as local programs; "+
				"pass --trust-plugins or --plugins %s, or set %s=1",
			auto, auto, TrustEnv)
		e.Details = map[string]any{"file": auto}
		return nil, e
	}
	return Load(ctx, reg, manifests)
}

// FingerprintFile reads a plugins file and returns its fingerprint.
func FingerprintFile(path string) (Fingerprint, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Fingerprint{}, verr.Wrap(verr.CodePlugin, err, "resolve plugins file")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Fingerprint{}, verr.Wrap(verr.CodePlugin, err, "read plugins file")
	}
	manifests, err := parsePluginsFile(abs, data)
	if err != nil {
		return Fingerprint{}, err
	}
	return fingerprintOf(abs, data, manifests), nil
}

func fingerprintOf(path string, data []byte, manifests []Manifest) Fingerprint {
	sum := sha256.Sum256(data)
	fp := Fingerprint{Path: path, SHA256: hex.EncodeToString(sum[:]), Binaries: []string{}}
	for _, m := range manifests {
		if m.Enabled != nil && !*m.Enabled {
			continue
		}
		fp.Binaries = append(fp.Binaries, m.Path)
	}
	return fp
}

// parsePluginsFile decodes a plugins file and resolves relative plugin paths
// against the file's directory.
func parsePluginsFile(path string, data []byte) ([]Manifest, error) {
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
	return pf.Plugins, nil
}

// TrustStore remembers approved plugins-file fingerprints in an app-local
// JSON file (never in the project). The zero value with an empty Path keeps
// decisions in memory only. It is safe for concurrent use.
type TrustStore struct {
	Path string

	mu     sync.Mutex
	loaded bool
	mem    map[string]trustRecord
}

type trustRecord struct {
	Fingerprint
	TrustedAt time.Time `json:"trustedAt"`
}

type trustFile struct {
	Trusted map[string]trustRecord `json:"trusted"`
}

// Trusted reports whether fp was approved before.
func (s *TrustStore) Trusted(fp Fingerprint) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return false, err
	}
	_, ok := s.mem[fp.Key()]
	return ok, nil
}

// Remember records fp as approved and persists the store.
func (s *TrustStore) Remember(fp Fingerprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	s.mem[fp.Key()] = trustRecord{Fingerprint: fp, TrustedAt: time.Now().UTC()}
	if s.Path == "" {
		return nil
	}
	data, err := json.MarshalIndent(trustFile{Trusted: s.mem}, "", "  ")
	if err != nil {
		return verr.Wrap(verr.CodeInternal, err, "encode plugin trust store")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return verr.Wrap(verr.CodeInternal, err, "create plugin trust store directory")
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return verr.Wrap(verr.CodeInternal, err, "write plugin trust store")
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		_ = os.Remove(tmp)
		return verr.Wrap(verr.CodeInternal, err, "write plugin trust store")
	}
	return nil
}

func (s *TrustStore) load() error {
	if s.loaded {
		return nil
	}
	s.mem = map[string]trustRecord{}
	if s.Path != "" {
		data, err := os.ReadFile(s.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return verr.Wrap(verr.CodeInternal, err, "read plugin trust store")
		default:
			var f trustFile
			if err := json.Unmarshal(data, &f); err != nil {
				return verr.Wrap(verr.CodeInternal, err, "parse plugin trust store")
			}
			for k, v := range f.Trusted {
				s.mem[k] = v
			}
		}
	}
	s.loaded = true
	return nil
}
