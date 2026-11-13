package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"sync"

	"github.com/pgodlews/verity-api/core/auth"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// OAuthRequest identifies a session without carrying credentials.
type OAuthRequest struct {
	Path        string `json:"path"`
	Environment string `json:"environment"`
	AuthName    string `json:"authName"`
}

type oauthKey struct {
	path, environment, name string
}

type oauthEntry struct {
	session     *auth.OAuthSession
	fingerprint [32]byte
	generation  uint64
}

// OAuthSessions belongs to one workspace/desktop process, never a package
// global. Its zero value is usable. No token is serialized by its methods.
type OAuthSessions struct {
	mu      sync.Mutex
	entries map[oauthKey]oauthEntry
}

func oauthConfig(p *model.Project, environment, name string, extra map[string]string) (map[string]any, string, [32]byte, error) {
	env, err := p.SelectEnvironment(environment)
	if err != nil {
		return nil, "", [32]byte{}, err
	}
	def, ok := p.AuthByName(name)
	if !ok || def.Type != "oauth2" || def.Config["grant"] != "authorization_code" {
		return nil, "", [32]byte{}, verr.New(verr.CodeConfig, "OAuth login requires an oauth2 authorization_code auth definition")
	}
	vars := map[string]string{}
	for k, v := range env.Variables {
		vars[k] = v
	}
	for k, v := range extra {
		vars[k] = v
	}
	config := map[string]any{}
	for k, v := range def.Config {
		if k != "refreshToken" {
			config[k] = v
		}
	}
	// A CI refresh-token reference may deliberately be unset on the desktop.
	cfg, err := secrets.NewResolver(vars).InterpolateMap(config, true)
	if err != nil {
		return nil, "", [32]byte{}, verr.New(verr.CodeConfig, "cannot resolve OAuth configuration for this environment")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", [32]byte{}, verr.New(verr.CodeConfig, "invalid OAuth configuration")
	}
	return cfg, env.Name, sha256.Sum256(encoded), nil
}

// OAuthLogin runs a PKCE flow and retains the resulting session in memory.
func (s *OAuthSessions) OAuthLogin(ctx context.Context, req OAuthRequest, open func(string) error) error {
	path, err := filepath.Abs(req.Path)
	if err != nil {
		return err
	}
	p, err := model.Load(path)
	if err != nil {
		return err
	}
	cfg, env, fingerprint, err := oauthConfig(p, req.Environment, req.AuthName, nil)
	if err != nil {
		return err
	}
	key := oauthKey{path, env, req.AuthName}
	s.mu.Lock()
	if s.entries == nil {
		s.entries = map[oauthKey]oauthEntry{}
	}
	entry := s.entries[key]
	entry.generation++
	generation := entry.generation
	s.entries[key] = entry
	s.mu.Unlock()

	session, err := auth.Login(ctx, cfg, secrets.NewRedactor(), open)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[key].generation != generation {
		return verr.New(verr.CodeAuth, "OAuth login was superseded or signed out")
	}
	s.entries[key] = oauthEntry{session: session, fingerprint: fingerprint, generation: generation}
	return nil
}

// OAuthLogout removes the session and invalidates any pending login for it.
func (s *OAuthSessions) OAuthLogout(req OAuthRequest) error {
	path, err := filepath.Abs(req.Path)
	if err != nil {
		return err
	}
	p, err := model.Load(path)
	if err != nil {
		return err
	}
	env, err := p.SelectEnvironment(req.Environment)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[oauthKey]oauthEntry{}
	}
	key := oauthKey{path, env.Name, req.AuthName}
	entry := s.entries[key]
	s.entries[key] = oauthEntry{generation: entry.generation + 1}
	return nil
}

// OAuthStatus reports whether the current configuration has a stored session.
func (s *OAuthSessions) OAuthStatus(req OAuthRequest) (bool, error) {
	path, err := filepath.Abs(req.Path)
	if err != nil {
		return false, err
	}
	p, err := model.Load(path)
	if err != nil {
		return false, err
	}
	_, env, fingerprint, err := oauthConfig(p, req.Environment, req.AuthName, nil)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[oauthKey{path, env, req.AuthName}]
	return entry.session != nil && entry.fingerprint == fingerprint, nil
}

// Run executes a workspace run with sessions belonging to this desktop process.
func (s *OAuthSessions) Run(ctx context.Context, opts RunOptions) (*spi.RunResult, error) {
	return run(ctx, opts, s, nil, nil)
}

func (s *OAuthSessions) forRun(p *model.Project, opts RunOptions) (map[string]*auth.OAuthSession, error) {
	if s == nil {
		return nil, nil
	}
	path, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, err
	}
	out := map[string]*auth.OAuthSession{}
	for _, def := range p.Auth {
		if def.Type != "oauth2" || def.Config["grant"] != "authorization_code" {
			continue
		}
		_, env, fingerprint, err := oauthConfig(p, opts.Environment, def.Name, opts.Vars)
		if err != nil {
			continue // The ordinary auth resolver reports used config errors.
		}
		s.mu.Lock()
		entry := s.entries[oauthKey{path, env, def.Name}]
		s.mu.Unlock()
		if entry.session != nil && entry.fingerprint == fingerprint {
			out[def.Name] = entry.session
		}
	}
	return out, nil
}
