package load

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/verr"
)

// A loopback proxy must not carry a guarded load run to a public address:
// the server below is both the private target (which redirects to a public
// IP literal) and an HTTP proxy that would answer for that public address.
func TestProxyCannotBypassPrivateAddressGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://203.0.113.10/x", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK) // "proxied" public request
	}))
	defer srv.Close()
	proxyURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer tr.CloseIdleConnections()
	_, err = Run(context.Background(), bootstrap.Default(), oneStep(srv.URL+"/start"), Options{
		VUs: 1, Iterations: 1, FollowRedirects: true, Transport: tr,
	})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.10") || !strings.Contains(err.Error(), "allow-remote") {
		t.Fatalf("error = %v, want a refusal of 203.0.113.10", err)
	}
}

func TestGuardedTransportsIgnoreProxy(t *testing.T) {
	dial := guardedDial(nil)
	if tr := newSharedTransport(2, dial); tr.Proxy != nil {
		t.Fatal("guarded shared transport keeps a proxy")
	}
	if tr := newSharedTransport(2, nil); tr.Proxy == nil {
		t.Fatal("unguarded transport lost the environment proxy")
	}
}

func TestGuardRefusesTransportWithOwnDialer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	var d net.Dialer
	tr := &http.Transport{DialContext: d.DialContext}
	_, err := Run(context.Background(), bootstrap.Default(), oneStep(srv.URL), Options{VUs: 1, Iterations: 1, Transport: tr})
	if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
		t.Fatalf("error = %v, want %s", err, verr.CodeConfig)
	}
}
