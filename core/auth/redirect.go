package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/pgodlews/verity-api/core/spi"
)

// requestSigner marks a message provider whose output covers the exact
// method, URL and body (aws_sigv4, hmac). A request it signed must be signed
// again whenever any of those change, and must not follow a redirect.
type requestSigner interface{ signsRequest() }

func (*sigV4Provider) signsRequest() {}
func (*hmacProvider) signsRequest()  {}

// Signs reports whether any provider in the chain signs the request bytes.
func (c *Chain) Signs() bool {
	if c == nil {
		return false
	}
	for _, p := range c.Message {
		if _, ok := p.(requestSigner); ok {
			return true
		}
	}
	return false
}

// Sign runs only the signing providers against req. Use it for a request
// whose method, URL or body differs from the one Apply saw (GraphQL
// introspection), after the other providers' headers have been copied over.
func (c *Chain) Sign(ctx context.Context, req *spi.Request) error {
	if c == nil {
		return nil
	}
	for _, p := range c.Message {
		if _, ok := p.(requestSigner); !ok {
			continue
		}
		if err := p.Apply(ctx, req); err != nil {
			return fmt.Errorf("message auth %q: %w", p.Type(), err)
		}
	}
	return nil
}

// AddedHeaders returns the header names whose values differ between before
// and after: the headers an auth chain set. Names are canonical.
func AddedHeaders(before, after http.Header) []string {
	var out []string
	for name, vals := range after {
		if strings.Join(before.Values(name), "\x00") != strings.Join(vals, "\x00") {
			out = append(out, http.CanonicalHeaderKey(name))
		}
	}
	return out
}

// GuardRedirects tightens client's redirect policy for one request. A signed
// request does not follow redirects: the signature covers the original URL,
// so the 3xx response is the result. Otherwise the existing policy (or Go's
// default limit of 10) applies, and authHeaders are removed as soon as a
// redirect leaves the original origin (scheme, host and port), so a token,
// API key or session token is never sent to another server. Go itself only
// drops Authorization and Cookie, and keeps them for subdomains.
func GuardRedirects(client *http.Client, signed bool, authHeaders []string) {
	if client == nil {
		return
	}
	prev := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if signed {
			return http.ErrUseLastResponse
		}
		if prev != nil {
			if err := prev(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
			for _, name := range authHeaders {
				req.Header.Del(name)
			}
		}
		return nil
	}
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(hostPort(a), hostPort(b))
}

func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}
