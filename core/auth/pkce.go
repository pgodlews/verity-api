package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// PKCE holds a fresh S256 proof and an independent CSRF state value.
// It is transient secret material and must never be returned to the frontend.
type PKCE struct {
	Verifier  string `json:"-"`
	Challenge string `json:"-"`
	State     string `json:"-"`
}

// NewPKCE generates an S256 verifier/challenge and a separate random state.
func NewPKCE() (*PKCE, error) {
	random := func() (string, error) {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return base64.RawURLEncoding.EncodeToString(b), err
	}
	verifier, err := random()
	if err != nil {
		return nil, verr.New(verr.CodeAuth, "PKCE random generation failed")
	}
	state, err := random()
	if err != nil {
		return nil, verr.New(verr.CodeAuth, "OAuth state generation failed")
	}
	sum := sha256.Sum256([]byte(verifier))
	return &PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), State: state}, nil
}

// AuthorizeURL builds the browser URL. Existing provider-specific query options
// (for example offline access consent) are preserved.
func AuthorizeURL(config map[string]any, redirect string, proof *PKCE) (string, error) {
	u, err := url.Parse(strConfig(config, "authUrl"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", verr.New(verr.CodeConfig, "oauth2 requires a valid HTTP(S) authUrl")
	}
	if proof == nil || proof.State == "" || proof.Challenge == "" || strConfig(config, "clientId") == "" {
		return "", verr.New(verr.CodeConfig, "oauth2 requires clientId and a PKCE proof")
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", strConfig(config, "clientId"))
	q.Set("redirect_uri", redirect)
	q.Set("state", proof.State)
	q.Set("code_challenge", proof.Challenge)
	q.Set("code_challenge_method", "S256")
	if scope := oauthScope(config); scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type callbackResult struct {
	code string
	err  error
}

// Loopback owns a loopback-only callback server. Close it when the flow ends.
type Loopback struct {
	RedirectURL string
	server      *http.Server
	result      chan callbackResult
}

// NewLoopback starts a listener on the configured port (0 chooses a free port).
func NewLoopback(port int, state string) (*Loopback, error) {
	if port < 0 || port > 65535 || state == "" {
		return nil, verr.New(verr.CodeConfig, "oauth2 redirectPort must be 0–65535 and state must be set")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, verr.New(verr.CodeAuth, "cannot listen for OAuth callback")
	}
	flow := &Loopback{
		RedirectURL: "http://" + listener.Addr().String() + "/callback",
		result:      make(chan callbackResult, 1),
	}
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		result := callbackResult{code: q.Get("code")}
		if q.Get("error") != "" {
			result.err = verr.New(verr.CodeAuth, "OAuth authorization was denied")
		} else if len(q["code"]) != 1 || result.code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			return
		}
		accepted := false
		once.Do(func() { accepted = true })
		if !accepted {
			http.Error(w, "OAuth callback already received", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>Verity</title><p>Authorization received. You can close this tab and return to Verity.</p>")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		flow.result <- result
	})
	flow.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = flow.server.Serve(listener) }()
	return flow, nil
}

// Close releases the listener and any remaining callback connections.
func (f *Loopback) Close() error { return f.server.Close() }

// Wait receives one callback, or returns an auth error on cancellation/timeout.
func (f *Loopback) Wait(ctx context.Context) (string, error) {
	select {
	case result := <-f.result:
		return result.code, result.err
	case <-ctx.Done():
		return "", verr.New(verr.CodeAuth, "OAuth login cancelled or timed out")
	}
}

// OAuthTokens are for in-process callers only; workspace bindings never expose
// this type. Tokens and expiry remain in memory for the process lifetime.
type OAuthTokens struct {
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	TokenType    string    `json:"-"`
	ExpiresAt    time.Time `json:"-"`
}

// ExchangeCode redeems an authorization code with its original PKCE verifier.
func ExchangeCode(ctx context.Context, config map[string]any, code, redirect, verifier string, red *secrets.Redactor) (*OAuthTokens, error) {
	p, err := authorizationProvider(config, red)
	if err != nil {
		return nil, err
	}
	if code == "" || verifier == "" || redirect == "" {
		return nil, verr.New(verr.CodeAuth, "code exchange requires code, redirect URI and PKCE verifier")
	}
	red.AddAll([]string{code, verifier})
	tok, err := p.requestToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}, nil)
	if err != nil {
		return nil, err
	}
	return &OAuthTokens{
		AccessToken: tok.accessToken, RefreshToken: p.refreshToken,
		TokenType: tok.tokenType, ExpiresAt: tok.expiresAt,
	}, nil
}

func authorizationProvider(config map[string]any, red *secrets.Redactor) (*oauth2Provider, error) {
	cfg := make(map[string]any, len(config)+1)
	for k, v := range config {
		cfg[k] = v
	}
	cfg["grant"] = "authorization_code"
	provider, err := newOAuth2(cfg, red)
	if err != nil {
		return nil, err
	}
	return provider.(*oauth2Provider), nil
}

// OAuthSession shares token rotation across workspace runs. Its own lock keeps
// each run's redactor attached throughout acquisition and header application.
type OAuthSession struct {
	mu       sync.Mutex
	provider *oauth2Provider
}

// NewOAuthSession seeds a process-only session from a successful code exchange.
func NewOAuthSession(config map[string]any, tokens *OAuthTokens, red *secrets.Redactor) (*OAuthSession, error) {
	if tokens == nil || tokens.AccessToken == "" {
		return nil, verr.New(verr.CodeAuth, "OAuth session requires an access token")
	}
	p, err := authorizationProvider(config, red)
	if err != nil {
		return nil, err
	}
	p.cached = &cachedToken{accessToken: tokens.AccessToken, tokenType: tokens.TokenType, expiresAt: tokens.ExpiresAt}
	p.refreshToken = tokens.RefreshToken
	p.refreshIssued = tokens.RefreshToken != ""
	red.AddAll([]string{tokens.AccessToken, tokens.RefreshToken})
	return &OAuthSession{provider: p}, nil
}

// Provider attaches this session to a run's redactor without copying its tokens.
func (s *OAuthSession) Provider(red *secrets.Redactor) spi.AuthProvider {
	return &sessionProvider{session: s, red: red}
}

type sessionProvider struct {
	session *OAuthSession
	red     *secrets.Redactor
}

func (*sessionProvider) Type() string         { return "oauth2" }
func (*sessionProvider) Layer() spi.AuthLayer { return spi.LayerMessage }

func (s *sessionProvider) useTokenClient(tc TokenClient) {
	s.session.mu.Lock()
	defer s.session.mu.Unlock()
	s.session.provider.useTokenClient(tc)
}

func (s *sessionProvider) Apply(ctx context.Context, req *spi.Request) error {
	s.session.mu.Lock()
	defer s.session.mu.Unlock()
	p := s.session.provider
	p.red = s.red
	p.registerSecrets()
	if p.cached != nil {
		s.red.Add(p.cached.accessToken)
	}
	return p.Apply(ctx, req)
}

// Login opens a browser through the caller's desktop adapter. The core never
// launches a browser itself; CLI runs use refreshToken and cannot call this.
func Login(ctx context.Context, config map[string]any, red *secrets.Redactor, open func(string) error) (*OAuthSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if ctx.Err() != nil {
		return nil, verr.New(verr.CodeAuth, "OAuth login cancelled or timed out")
	}
	if _, err := authorizationProvider(config, red); err != nil {
		return nil, err
	}
	proof, err := NewPKCE()
	if err != nil {
		return nil, err
	}
	red.AddAll([]string{proof.Verifier, proof.State})
	port := 0
	if raw := strConfig(config, "redirectPort"); raw != "" {
		port, err = strconv.Atoi(raw)
		if err != nil {
			return nil, verr.New(verr.CodeConfig, "oauth2 redirectPort must be an integer")
		}
	}
	flow, err := NewLoopback(port, proof.State)
	if err != nil {
		return nil, err
	}
	defer flow.Close()
	target, err := AuthorizeURL(config, flow.RedirectURL, proof)
	if err != nil {
		return nil, err
	}
	if open == nil {
		return nil, verr.New(verr.CodeAuth, "interactive OAuth login requires the desktop GUI")
	}
	if err := open(target); err != nil {
		return nil, verr.New(verr.CodeAuth, "cannot open OAuth authorization in the browser")
	}
	code, err := flow.Wait(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := ExchangeCode(ctx, config, code, flow.RedirectURL, proof.Verifier, red)
	if err != nil {
		return nil, err
	}
	return NewOAuthSession(config, tokens, red)
}
