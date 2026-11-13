package runner

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
)

// NewHTTPClient builds the HTTP client used for a step, including transport
// TLS from auth. timeout is the whole client budget. followRedirects matches
// the run option.
func NewHTTPClient(tlsCfg *spi.TLSConfig, timeout time.Duration, followRedirects bool) (*http.Client, error) {
	return buildHTTPClient(tlsCfg, timeout, followRedirects, nil, nil, nil, "")
}

// buildHTTPClient constructs an *http.Client whose transport reflects the
// transport-level TLS configuration produced by the auth chain. When tlsCfg is
// nil, a client with sane defaults is returned. Each step gets its own client
// so per-step mTLS settings do not leak across steps.
//
// shared, when non-nil and tlsCfg is nil, is used as the client's transport
// so many steps can reuse connections. The caller owns its lifetime. A
// non-nil tlsCfg always builds a private transport. dial, when set, is
// installed on that private transport (the load command's address guard).
// cache, when set, reuses one private transport per TLS config for the run.
func buildHTTPClient(tlsCfg *spi.TLSConfig, timeout time.Duration, followRedirects bool, shared http.RoundTripper, dial func(context.Context, string, string) (net.Conn, error), cache *transportCache, serverName string) (*http.Client, error) {
	var transport http.RoundTripper
	if shared != nil && tlsCfg == nil {
		transport = shared
	} else if cache != nil && tlsCfg != nil {
		built, err := cache.transport(tlsTransportKey(tlsCfg, serverName), func() (*http.Transport, error) {
			return defaultTransport(tlsCfg, dial)
		})
		if err != nil {
			return nil, err
		}
		transport = built
	} else {
		built, err := defaultTransport(tlsCfg, dial)
		if err != nil {
			return nil, err
		}
		transport = built
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
	if !followRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client, nil
}

// defaultTransport builds the per-step transport, including mTLS material
// when tlsCfg is set. dial replaces the default dialer when set.
func defaultTransport(tlsCfg *spi.TLSConfig, dial func(context.Context, string, string) (net.Conn, error)) (*http.Transport, error) {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if dial != nil {
		// A custom dialer (the load-test private-address guard) must see the
		// real destination, so HTTP(S)_PROXY is not used.
		transport.DialContext = dial
		transport.Proxy = nil
	}

	if tlsCfg != nil {
		t := &tls.Config{
			MinVersion:         tlsCfg.MinVersion,
			MaxVersion:         tlsCfg.MaxVersion,
			InsecureSkipVerify: tlsCfg.InsecureSkipVerify, //nolint:gosec // opt-in, test-only
		}
		if len(tlsCfg.CipherSuites) > 0 {
			t.CipherSuites = tlsCfg.CipherSuites
		}
		if len(tlsCfg.ClientCertPEM) > 0 && len(tlsCfg.ClientKeyPEM) > 0 {
			cert, err := tls.X509KeyPair(tlsCfg.ClientCertPEM, tlsCfg.ClientKeyPEM)
			if err != nil {
				return nil, fmt.Errorf("load client cert/key: %w", err)
			}
			t.Certificates = []tls.Certificate{cert}
		}
		if len(tlsCfg.CACertPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(tlsCfg.CACertPEM) {
				return nil, fmt.Errorf("failed to parse custom CA bundle")
			}
			t.RootCAs = pool
		}
		transport.TLSClientConfig = t
	}
	return transport, nil
}

// requestServerName is the TLS server name for a request URL. It is part of
// the private-transport cache key so two hosts do not share a client config.
func requestServerName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// transportCache keeps one private HTTP transport per TLS config for a run.
// Building one per attempt leaks idle connections until IdleConnTimeout.
type transportCache struct {
	mu    sync.Mutex
	items map[string]*http.Transport
}

func (c *transportCache) transport(key string, build func() (*http.Transport, error)) (*http.Transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*http.Transport{}
	}
	if t, ok := c.items[key]; ok {
		return t, nil
	}
	t, err := build()
	if err != nil {
		return nil, err
	}
	c.items[key] = t
	return t, nil
}

func (c *transportCache) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.items {
		t.CloseIdleConnections()
	}
}

// tlsTransportKey identifies a TLS config. The key covers the client cert,
// its key, the CA bundle, the insecure flag, version and cipher limits, and
// the server name. Equal configs share one transport.
func tlsTransportKey(cfg *spi.TLSConfig, serverName string) string {
	h := sha256.New()
	write := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		_, _ = h.Write(n[:])
		_, _ = h.Write(b)
	}
	write(cfg.ClientCertPEM)
	write(cfg.ClientKeyPEM)
	write(cfg.CACertPEM)
	var flags [8]byte
	if cfg.InsecureSkipVerify {
		flags[0] = 1
	}
	binary.BigEndian.PutUint16(flags[2:], cfg.MinVersion)
	binary.BigEndian.PutUint16(flags[4:], cfg.MaxVersion)
	_, _ = h.Write(flags[:])
	for _, suite := range cfg.CipherSuites {
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], suite)
		_, _ = h.Write(b[:])
	}
	_, _ = h.Write([]byte(serverName))
	return hex.EncodeToString(h.Sum(nil))
}
