// Package auth provides Verity's built-in reference AuthProvider implementations.
//
// Two composable layers (see spi.AuthLayer):
//   - Message-level providers mutate the outgoing request (Basic, Digest,
//     Bearer, API key, OAuth2 bearer, AWS SigV4, HMAC).
//   - Transport-level providers contribute TLS config (mTLS).
//
// Every provider is registered through the same registry.RegisterAuth path as
// third-party providers; the host gives built-ins no special privileges.
// Providers register their secret material with the Redactor so credentials are
// masked everywhere, and OAuth providers cache tokens in memory only.
package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/pgodlews/verity-api/core/internal/scalar"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// Register installs all built-in auth providers into the registry.
func Register(r *registry.Registry) {
	r.RegisterAuth("none", newNone)
	r.RegisterAuth("basic", newBasic)
	r.RegisterAuth("digest", newDigest)
	r.RegisterAuth("bearer", newBearer)
	r.RegisterAuth("apikey", newAPIKey)
	r.RegisterAuth("mtls", newMTLS)
	r.RegisterAuth("oauth2", newOAuth2)
	r.RegisterAuth("oauth2_client_credentials", newOAuth2ClientCredentials)
	r.RegisterAuth("aws_sigv4", newSigV4)
	r.RegisterAuth("hmac", newHMAC)
}

// strConfig fetches a string config value, returning "" when absent or null.
func strConfig(config map[string]any, key string) string {
	return scalar.String(config[key])
}

// noneProvider is the explicit no-auth provider. Useful to override an
// inherited provider with "no auth" at a narrower scope.
type noneProvider struct{}

func newNone(_ map[string]any, _ *secrets.Redactor) (spi.AuthProvider, error) {
	return noneProvider{}, nil
}
func (noneProvider) Type() string                              { return "none" }
func (noneProvider) Layer() spi.AuthLayer                      { return spi.LayerMessage }
func (noneProvider) Apply(context.Context, *spi.Request) error { return nil }

// basicProvider implements HTTP Basic authentication.
type basicProvider struct {
	username string
	password string
}

func newBasic(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	p := &basicProvider{
		username: strConfig(config, "username"),
		password: strConfig(config, "password"),
	}
	if p.username == "" {
		return nil, fmt.Errorf("basic auth requires 'username'")
	}
	red.Add(p.password)
	// The base64 credential is also secret material.
	red.Add(base64.StdEncoding.EncodeToString([]byte(p.username + ":" + p.password)))
	return p, nil
}
func (basicProvider) Type() string         { return "basic" }
func (basicProvider) Layer() spi.AuthLayer { return spi.LayerMessage }
func (p *basicProvider) Apply(_ context.Context, req *spi.Request) error {
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	cred := base64.StdEncoding.EncodeToString([]byte(p.username + ":" + p.password))
	req.Headers.Set("Authorization", "Basic "+cred)
	return nil
}

// bearerProvider sets a static Bearer token.
type bearerProvider struct {
	token string
}

func newBearer(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	tok := strConfig(config, "token")
	if tok == "" {
		return nil, fmt.Errorf("bearer auth requires 'token'")
	}
	red.Add(tok)
	return &bearerProvider{token: tok}, nil
}
func (bearerProvider) Type() string         { return "bearer" }
func (bearerProvider) Layer() spi.AuthLayer { return spi.LayerMessage }
func (p *bearerProvider) Apply(_ context.Context, req *spi.Request) error {
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	req.Headers.Set("Authorization", "Bearer "+p.token)
	return nil
}

// apiKeyProvider places an API key in a header, query parameter, or cookie.
type apiKeyProvider struct {
	name      string
	value     string
	placement string // "header" (default), "query", "cookie"
}

func newAPIKey(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	p := &apiKeyProvider{
		name:      strConfig(config, "name"),
		value:     strConfig(config, "value"),
		placement: strConfig(config, "in"),
	}
	if p.name == "" {
		return nil, fmt.Errorf("apikey auth requires 'name'")
	}
	if p.value == "" {
		return nil, fmt.Errorf("apikey auth requires 'value'")
	}
	if p.placement == "" {
		p.placement = "header"
	}
	switch p.placement {
	case "header", "query", "cookie":
	default:
		return nil, fmt.Errorf("apikey 'in' must be header|query|cookie, got %q", p.placement)
	}
	red.Add(p.value)
	return p, nil
}
func (apiKeyProvider) Type() string         { return "apikey" }
func (apiKeyProvider) Layer() spi.AuthLayer { return spi.LayerMessage }
func (p *apiKeyProvider) Apply(_ context.Context, req *spi.Request) error {
	switch p.placement {
	case "header":
		if req.Headers == nil {
			req.Headers = http.Header{}
		}
		req.Headers.Set(p.name, p.value)
	case "query":
		if req.Query == nil {
			req.Query = map[string]string{}
		}
		req.Query[p.name] = p.value
	case "cookie":
		if req.Cookies == nil {
			req.Cookies = map[string]string{}
		}
		req.Cookies[p.name] = p.value
	}
	return nil
}
