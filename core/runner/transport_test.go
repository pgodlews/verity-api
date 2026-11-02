package runner

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
)

// TestHTTPProxyEnvHonored verifies buildHTTPClient's transport respects
// HTTP_PROXY. http.ProxyFromEnvironment memoizes the resolved proxy config
// for the lifetime of the process (sync.Once keyed on first use), so any
// earlier test in this package that performs a real HTTP round trip would
// lock in "no proxy" before this test's os.Setenv takes effect. To get a
// reliable result this test re-execs itself in a fresh subprocess with the
// env var set before any HTTP call happens.
func TestHTTPProxyEnvHonored(t *testing.T) {
	if os.Getenv("VERITY_PROXY_TEST_CHILD") == "1" {
		runProxyEnvChild(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHTTPProxyEnvHonored$", "-test.v")
	cmd.Env = append(os.Environ(), "VERITY_PROXY_TEST_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess check failed: %v\n%s", err, out)
	}
}

func runProxyEnvChild(t *testing.T) {
	var hit bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()

	if err := os.Setenv("HTTP_PROXY", proxy.URL); err != nil {
		t.Fatal(err)
	}

	client, err := buildHTTPClient(nil, 5*time.Second, true, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// The target host is never actually dialed: with a proxy configured, the
	// transport dials the proxy and forwards the absolute-URI request line,
	// so this works without network access or DNS resolution.
	resp, err := client.Get("http://example.invalid/")
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	_ = resp.Body.Close()

	if !hit {
		t.Fatal("expected request to be routed through HTTP_PROXY")
	}
}

func TestPrivateTransportCachedPerTLSConfig(t *testing.T) {
	cache := &transportCache{}
	defer cache.close()
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, nil
	}
	cfg := &spi.TLSConfig{InsecureSkipVerify: true}
	a, err := buildHTTPClient(cfg, time.Second, false, nil, dial, cache, "one.example")
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildHTTPClient(cfg, time.Second, false, nil, dial, cache, "one.example")
	if err != nil {
		t.Fatal(err)
	}
	if a.Transport != b.Transport {
		t.Fatal("same TLS config built two transports")
	}
	c, err := buildHTTPClient(cfg, time.Second, false, nil, dial, cache, "two.example")
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport == a.Transport {
		t.Fatal("server name was not part of the cache key")
	}
	insecure := &spi.TLSConfig{InsecureSkipVerify: false}
	d, err := buildHTTPClient(insecure, time.Second, false, nil, dial, cache, "one.example")
	if err != nil {
		t.Fatal(err)
	}
	if d.Transport == a.Transport {
		t.Fatal("insecure flag was not part of the cache key")
	}
	if tlsTransportKey(&spi.TLSConfig{CACertPEM: []byte("ca-a"), ClientCertPEM: []byte("cert")}, "h") ==
		tlsTransportKey(&spi.TLSConfig{CACertPEM: []byte("ca-b"), ClientCertPEM: []byte("cert")}, "h") {
		t.Fatal("CA was not part of the cache key")
	}
	if tlsTransportKey(&spi.TLSConfig{ClientCertPEM: []byte("cert-a"), ClientKeyPEM: []byte("key-a")}, "h") ==
		tlsTransportKey(&spi.TLSConfig{ClientCertPEM: []byte("cert-b"), ClientKeyPEM: []byte("key-b")}, "h") {
		t.Fatal("client cert was not part of the cache key")
	}
	tr, ok := a.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("private transport lost the dial guard")
	}
}
