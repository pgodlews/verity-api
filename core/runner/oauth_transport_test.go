package runner

import (
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
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

type pemPair struct {
	cert, key []byte
	parsed    *x509.Certificate
	priv      *ecdsa.PrivateKey
}

func makeCert(t *testing.T, cn string, parent *pemPair, ca bool) *pemPair {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:        ca, BasicConstraintsValid: ca,
	}
	signer, signerKey := tmpl, priv
	if parent != nil {
		signer, signerKey = parent.parsed, parent.priv
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &priv.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(priv)
	return &pemPair{
		cert:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		key:    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		parsed: parsed, priv: priv,
	}
}

// The OAuth token request goes through the run's transport for the step's
// mTLS config: the private CA and client certificate reach the token
// endpoint as they reach the API.
func TestOAuthTokenRequestUsesTheStepsMTLS(t *testing.T) {
	ca := makeCert(t, "ca", nil, true)
	server := makeCert(t, "server", ca, false)
	client := makeCert(t, "client", ca, false)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.cert)
	serverPair, err := tls.X509KeyPair(server.cert, server.key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mtls-token-value-0001","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer mtls-token-value-0001" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.StartTLS()
	defer srv.Close()

	p := &model.Project{
		SchemaVersion: "1.0", Name: "oauth-mtls",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Auth: []model.AuthDef{
			{Name: "tls", Type: "mtls", Config: map[string]any{"certPEM": string(client.cert), "keyPEM": string(client.key), "caPEM": string(ca.cert)}},
			{Name: "oauth", Type: "oauth2", Config: map[string]any{"tokenUrl": srv.URL + "/token", "clientId": "id", "clientSecret": "client-secret-value-01"}},
		},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
			Name: "api", Protocol: "rest", Auth: &model.AuthRef{Use: []string{"tls", "oauth"}},
			Request:    map[string]any{"url": srv.URL + "/api"},
			Assertions: []model.Assertion{{Type: "status", Value: 200}},
		}}}}}},
	}
	st := retryRun(t, nil, p, Options{}).Suites[0].Cases[0].Steps[0]
	if st.Status != spi.StatusPassed {
		t.Fatalf("step %s: %+v %+v", st.Status, st.Error, st.Assertions)
	}
}

// Token requests honour the run's timeout instead of a fixed 30 seconds.
func TestOAuthTokenRequestUsesTheRunTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	p := &model.Project{
		SchemaVersion: "1.0", Name: "oauth-timeout",
		Environments: []model.Environment{{Name: "default", Default: true}},
		Auth: []model.AuthDef{{Name: "oauth", Type: "oauth2", Config: map[string]any{
			"tokenUrl": srv.URL + "/token", "clientId": "id", "clientSecret": "client-secret-value-02"}}},
		Suites: []model.Suite{{Name: "s", Cases: []model.TestCase{{Name: "c", Steps: []model.Step{{
			Name: "api", Protocol: "rest", Auth: &model.AuthRef{Use: []string{"oauth"}},
			Request: map[string]any{"url": srv.URL + "/api"},
		}}}}}},
	}
	start := time.Now()
	st := retryRun(t, nil, p, Options{Timeout: 200 * time.Millisecond}).Suites[0].Cases[0].Steps[0]
	if time.Since(start) > 5*time.Second {
		t.Fatalf("token request ignored the run timeout (%v)", time.Since(start))
	}
	if st.Error == nil || st.Error.Code != string(verr.CodeAuth) || strings.Contains(st.Error.Message, "client-secret-value-02") {
		t.Fatalf("step error %+v", st.Error)
	}
}
