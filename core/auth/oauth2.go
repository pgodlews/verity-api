package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// tokenResponse is the standard OAuth2 token endpoint response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// cachedToken holds an access token and its computed expiry.
type cachedToken struct {
	accessToken string
	tokenType   string
	expiresAt   time.Time
}

// oauth2Provider implements the OAuth 2.0 client-credentials, password, and
// refresh-token grants, plus headless authorization-code refresh. It caches the access token in memory keyed to its
// expiry and refreshes it before expiry (with a configurable safety skew).
// Tokens, including a refresh token rotated by the authorization server, are
// never written to disk or back into the project config.
type oauth2Provider struct {
	providerType string
	grant        string
	username     string
	password     string
	refreshToken string
	// refreshIssued is set when a token response carries a refresh_token.
	// Later fetches then use the refresh-token grant. A refreshToken that was
	// only present in the config is not used that way unless grant is already
	// refresh_token.
	refreshIssued bool
	tokenURL      string
	clientID      string
	clientSecret  string
	scope         string
	// authStyle controls how client credentials are sent: "header" (Basic) or "body".
	authStyle string
	skew      time.Duration

	red    *secrets.Redactor
	client *http.Client

	mu     sync.Mutex
	cached *cachedToken
}

func newOAuth2ClientCredentials(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	return newOAuth2Provider("oauth2_client_credentials", config, red)
}

func newOAuth2(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	return newOAuth2Provider("oauth2", config, red)
}

func newOAuth2Provider(typ string, config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	p := &oauth2Provider{
		providerType: typ,
		grant:        strConfig(config, "grant"),
		username:     strConfig(config, "username"),
		password:     strConfig(config, "password"),
		refreshToken: strConfig(config, "refreshToken"),
		tokenURL:     strConfig(config, "tokenUrl"),
		clientID:     strConfig(config, "clientId"),
		clientSecret: strConfig(config, "clientSecret"),
		scope:        oauthScope(config),
		authStyle:    strConfig(config, "authStyle"),
		red:          red,
		client:       &http.Client{Timeout: 30 * time.Second},
		skew:         30 * time.Second,
	}
	if p.grant == "" {
		p.grant = "client_credentials"
	}
	// Authorization Code supports public clients. Other grants retain their
	// existing confidential-client requirements.
	required := []string{"tokenUrl", "clientId", "clientSecret"}
	switch p.grant {
	case "client_credentials":
	case "password":
		required = append(required, "username", "password")
	case "refresh_token":
		required = append(required, "refreshToken")
	case "authorization_code":
		required = []string{"authUrl", "tokenUrl", "clientId"}
	default:
		return nil, verr.New(verr.CodeConfig, "oauth2 'grant' must be client_credentials|password|refresh_token|authorization_code")
	}
	for _, key := range required {
		if strConfig(config, key) == "" {
			return nil, verr.Newf(verr.CodeConfig, "oauth2 %s grant requires '%s'", p.grant, key)
		}
	}
	if p.authStyle == "" {
		p.authStyle = "header"
	}
	if p.authStyle != "header" && p.authStyle != "body" {
		return nil, verr.New(verr.CodeConfig, "oauth2 'authStyle' must be header|body")
	}
	if s := strings.TrimSpace(strConfig(config, "refreshSkewSeconds")); s != "" {
		secs, err := strconv.Atoi(s)
		if err != nil || secs < 0 || secs > 86400 {
			return nil, verr.Newf(verr.CodeConfig, "oauth2 'refreshSkewSeconds' must be an integer from 0 to 86400, got %q", s)
		}
		p.skew = time.Duration(secs) * time.Second
	}
	// Register every configured secret, including ones the active grant does
	// not send, so a copied field cannot leak. The Basic client credential is
	// secret material the same way HTTP Basic is.
	p.registerSecrets()
	return p, nil
}

func (p *oauth2Provider) registerSecrets() {
	p.red.AddAll([]string{p.clientSecret, p.password, p.refreshToken})
	p.red.Add(base64.StdEncoding.EncodeToString([]byte(p.clientID + ":" + p.clientSecret)))
}

func (p *oauth2Provider) Type() string       { return p.providerType }
func (*oauth2Provider) Layer() spi.AuthLayer { return spi.LayerMessage }

// useHTTPTransport replaces the client that fetches tokens. The load command
// installs a dialer that refuses non-private addresses.
func (p *oauth2Provider) useHTTPTransport(rt http.RoundTripper) {
	if rt == nil || p.client == nil {
		return
	}
	p.client.Transport = rt
}

// Apply ensures a valid token is cached and sets the Authorization header.
func (p *oauth2Provider) Apply(ctx context.Context, req *spi.Request) error {
	tok, err := p.token(ctx)
	if err != nil {
		return err
	}
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	typ := tok.tokenType
	if typ == "" {
		typ = "Bearer"
	}
	req.Headers.Set("Authorization", typ+" "+tok.accessToken)
	return nil
}

// token returns a valid cached token, fetching/refreshing as needed. The cache
// is keyed to expiry: a token is reused until it is within skew of expiring.
func (p *oauth2Provider) token(ctx context.Context) (*cachedToken, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cached != nil && time.Now().Add(p.skew).Before(p.cached.expiresAt) {
		return p.cached, nil
	}
	tok, err := p.fetch(ctx)
	if err != nil {
		return nil, err
	}
	p.cached = tok
	return tok, nil
}

// fetch performs the token request against the token endpoint.
// Caller must hold p.mu.
func (p *oauth2Provider) fetch(ctx context.Context) (*cachedToken, error) {
	grant := p.grant
	if grant == "authorization_code" {
		if p.refreshToken == "" || strings.Contains(p.refreshToken, "${") {
			return nil, verr.New(verr.CodeAuth, "oauth2 authorization_code requires refreshToken for CLI/CI; sign in through the desktop GUI for an in-memory session")
		}
		grant = "refresh_token"
	}
	if p.refreshIssued && p.grant != "refresh_token" {
		grant = "refresh_token"
	}
	form := url.Values{}
	form.Set("grant_type", grant)
	switch grant {
	case "password":
		form.Set("username", p.username)
		form.Set("password", p.password)
	case "refresh_token":
		form.Set("refresh_token", p.refreshToken)
	}
	if p.scope != "" {
		form.Set("scope", p.scope)
	}

	return p.requestToken(ctx, form)
}

// requestToken also serves the GUI code exchange. Caller holds p.mu or owns p.
func (p *oauth2Provider) requestToken(ctx context.Context, form url.Values) (*cachedToken, error) {
	if p.authStyle == "body" || p.clientSecret == "" {
		form.Set("client_id", p.clientID)
		if p.clientSecret != "" {
			form.Set("client_secret", p.clientSecret)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, verr.New(verr.CodeAuth, "build token request: "+p.red.Redact(err.Error()))
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	if p.authStyle != "body" && p.clientSecret != "" {
		httpReq.SetBasicAuth(p.clientID, p.clientSecret)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		// The message is redacted and the raw error is not wrapped: reporters
		// append an unwrapped cause, which would otherwise re-expose it.
		return nil, verr.New(verr.CodeAuth, "token endpoint request failed: "+p.red.Redact(err.Error()))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, verr.Newf(verr.CodeAuth, "token endpoint returned %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, verr.New(verr.CodeAuth, "token endpoint returned invalid JSON")
	}
	p.red.AddAll([]string{tr.AccessToken, tr.RefreshToken})
	if tr.AccessToken == "" {
		return nil, verr.New(verr.CodeAuth, "token endpoint returned no access_token")
	}
	// Keep rotation in provider memory; never mutate the project config.
	if tr.RefreshToken != "" {
		p.refreshToken = tr.RefreshToken
		p.refreshIssued = true
	}

	expires := time.Now().Add(time.Hour) // default if expires_in absent
	if tr.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return &cachedToken{
		accessToken: tr.AccessToken,
		tokenType:   tr.TokenType,
		expiresAt:   expires,
	}, nil
}

// oauthScope accepts the original space-delimited scope and PKCE scopes arrays.
func oauthScope(config map[string]any) string {
	if s := strConfig(config, "scope"); s != "" {
		return s
	}
	switch scopes := config["scopes"].(type) {
	case string:
		return scopes
	case []string:
		return strings.Join(scopes, " ")
	case []any:
		var out []string
		for _, scope := range scopes {
			out = append(out, fmt.Sprint(scope))
		}
		return strings.Join(out, " ")
	}
	return ""
}
