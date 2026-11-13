package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/pgodlews/verity-api/core/fsutil"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// mtlsProvider is a transport-level provider contributing a client certificate,
// key, and optional custom CA bundle plus TLS version controls. The runner
// merges the resulting spi.TLSConfig into the HTTP transport before any
// message-level provider runs.
//
// Cert/key/CA may be supplied inline as PEM (certPEM/keyPEM/caPEM) or by file
// path (certFile/keyFile/caFile). File paths resolve relative to the project
// directory passed in config["__dir"] by the runner.
type mtlsProvider struct {
	certPEM  []byte
	keyPEM   []byte
	caPEM    []byte
	minVer   uint16
	maxVer   uint16
	insecure bool
}

func newMTLS(config map[string]any, red *secrets.Redactor) (spi.AuthProvider, error) {
	dir := strConfig(config, "__dir")
	p := &mtlsProvider{}

	var err error
	if p.certPEM, err = pemFromConfig(config, "certPEM", "certFile", dir); err != nil {
		return nil, fmt.Errorf("mtls cert: %w", err)
	}
	if p.keyPEM, err = pemFromConfig(config, "keyPEM", "keyFile", dir); err != nil {
		return nil, fmt.Errorf("mtls key: %w", err)
	}
	if p.caPEM, err = pemFromConfig(config, "caPEM", "caFile", dir); err != nil {
		return nil, fmt.Errorf("mtls ca: %w", err)
	}
	if len(p.certPEM) == 0 || len(p.keyPEM) == 0 {
		return nil, fmt.Errorf("mtls requires a client cert and key (certPEM/certFile + keyPEM/keyFile)")
	}
	// Validate the keypair early so misconfig fails fast with a clear error.
	if _, err := tls.X509KeyPair(p.certPEM, p.keyPEM); err != nil {
		return nil, fmt.Errorf("mtls invalid cert/key pair: %w", err)
	}
	// The private key is highly sensitive; ensure it is redacted everywhere.
	red.Add(string(p.keyPEM))

	if p.minVer, err = parseTLSVersion(config, "minVersion"); err != nil {
		return nil, err
	}
	if p.maxVer, err = parseTLSVersion(config, "maxVersion"); err != nil {
		return nil, err
	}
	if p.minVer != 0 && p.maxVer != 0 && p.minVer > p.maxVer {
		return nil, verr.New(verr.CodeConfig, "mtls minVersion is higher than maxVersion")
	}
	if v, ok := config["insecureSkipVerify"].(bool); ok {
		p.insecure = v
	}
	return p, nil
}

func (mtlsProvider) Type() string         { return "mtls" }
func (mtlsProvider) Layer() spi.AuthLayer { return spi.LayerTransport }
func (p *mtlsProvider) Apply(_ context.Context, req *spi.Request) error {
	req.TLS = &spi.TLSConfig{
		ClientCertPEM:      p.certPEM,
		ClientKeyPEM:       p.keyPEM,
		CACertPEM:          p.caPEM,
		MinVersion:         p.minVer,
		MaxVersion:         p.maxVer,
		InsecureSkipVerify: p.insecure,
	}
	return nil
}

// pemFromConfig reads PEM data either inline (inlineKey) or from a file
// (fileKey), resolving relative paths against dir.
func pemFromConfig(config map[string]any, inlineKey, fileKey, dir string) ([]byte, error) {
	if inline := strConfig(config, inlineKey); inline != "" {
		return []byte(inline), nil
	}
	if path := strConfig(config, fileKey); path != "" {
		// Confined to the project directory unless --allow-external-files.
		return fsutil.ReadInside(dir, path, fsutil.AllowExternal(config), "mtls "+fileKey)
	}
	return nil, nil
}

// tlsVersion maps a friendly version string to a tls.Version constant.
// parseTLSVersion reads an optional TLS version field. Empty means the Go
// default; an unknown value is a config_error instead of silently becoming
// the default.
func parseTLSVersion(config map[string]any, key string) (uint16, error) {
	s := strings.TrimSpace(strConfig(config, key))
	if s == "" {
		return 0, nil
	}
	if v := tlsVersion(s); v != 0 {
		return v, nil
	}
	return 0, verr.Newf(verr.CodeConfig, "mtls %s %q is not one of 1.0, 1.1, 1.2, 1.3", key, s)
}

func tlsVersion(s string) uint16 {
	switch s {
	case "1.0", "tls1.0", "TLS1.0":
		return tls.VersionTLS10
	case "1.1", "tls1.1", "TLS1.1":
		return tls.VersionTLS11
	case "1.2", "tls1.2", "TLS1.2":
		return tls.VersionTLS12
	case "1.3", "tls1.3", "TLS1.3":
		return tls.VersionTLS13
	default:
		return 0
	}
}
