package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/secrets"
)

// testCert is a generated certificate + key in PEM form.
type testCert struct {
	certPEM []byte
	keyPEM  []byte
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
}

// genCert creates a cert signed by parent (or self-signed when parent is nil).
func genCert(t *testing.T, cn string, isCA bool, parent *testCert, dnsNames []string, ips []net.IP) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	if isCA {
		tmpl.IsCA = true
		tmpl.BasicConstraintsValid = true
	}
	signer := tmpl
	signerKey := key
	if parent != nil {
		signer = parent.cert
		signerKey = parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return &testCert{certPEM: certPEM, keyPEM: keyPEM, cert: cert, key: key}
}

// TestMTLSHandshake stands up a TLS server that requires (and verifies) a
// client certificate signed by a test CA, then drives the mTLS provider's
// TLSConfig through a real handshake.
func TestMTLSHandshake(t *testing.T) {
	ca := genCert(t, "Test CA", true, nil, nil, nil)
	server := genCert(t, "localhost", false, ca, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	client := genCert(t, "test-client", false, ca, nil, nil)

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(ca.certPEM)

	serverTLSCert, err := tls.X509KeyPair(server.certPEM, server.keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Confirm the client cert reached the server.
		if len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte("mtls-ok"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverTLSCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}
	srv.StartTLS()
	defer srv.Close()

	// Build the mTLS provider with the client cert/key and the CA bundle.
	red := secrets.NewRedactor()
	prov, err := newMTLS(map[string]any{
		"certPEM": string(client.certPEM),
		"keyPEM":  string(client.keyPEM),
		"caPEM":   string(ca.certPEM),
	}, red)
	if err != nil {
		t.Fatal(err)
	}
	req := newReq()
	if err := prov.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.TLS == nil || len(req.TLS.ClientCertPEM) == 0 {
		t.Fatal("mTLS provider did not set transport TLS config")
	}

	// Construct a client that performs the handshake using the provider's TLS.
	clientCert, err := tls.X509KeyPair(req.TLS.ClientCertPEM, req.TLS.ClientKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(req.TLS.CACertPEM)
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{clientCert},
			RootCAs:      pool,
		},
	}}
	resp, err := httpClient.Get(srv.URL)
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 from mTLS server, got %d", resp.StatusCode)
	}

	// The client private key must be redacted.
	if red.Redact(string(client.keyPEM)) == string(client.keyPEM) {
		t.Fatal("client private key not redacted")
	}
}

// TestMTLSRejectsWithoutClientCert confirms the test server actually enforces
// client auth (so the positive test is meaningful).
func TestMTLSRejectsWithoutClientCert(t *testing.T) {
	ca := genCert(t, "Test CA", true, nil, nil, nil)
	server := genCert(t, "localhost", false, ca, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(ca.certPEM)
	serverTLSCert, _ := tls.X509KeyPair(server.certPEM, server.keyPEM)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverTLSCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
	}
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.certPEM)
	httpClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool}, // no client cert
	}}
	if _, err := httpClient.Get(srv.URL); err == nil {
		t.Fatal("expected handshake failure without client cert")
	}
}
