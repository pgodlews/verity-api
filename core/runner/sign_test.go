package runner

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/bootstrap"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

func TestHMACSignsUploadAndResignsOnRetry(t *testing.T) {
	const secret = "hmac-secret-value-0123456789"
	dir := t.TempDir()
	payload := []byte("file-body-to-sign")
	if err := os.WriteFile(filepath.Join(dir, "body.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var nonces, sigs []string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Nonce")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(nonce + "."))
		mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		if r.Header.Get("X-Signature") != want || string(body) != string(payload) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		calls++
		nonces = append(nonces, nonce)
		sigs = append(sigs, want)
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	yaml := []byte(`
schemaVersion: "1.0"
name: signed
auth:
  - name: signer
    type: hmac
    config:
      secret: "${env:HMAC_SECRET}"
      nonceHeader: X-Nonce
      message: "${nonce}.${body}"
suites:
  - name: s
    auth: { use: [signer] }
    cases:
      - name: c
        steps:
          - name: upload
            protocol: rest
            request:
              method: POST
              url: "${BASE_URL}/sign"
              bodyFile: body.bin
            retry: { count: 1, until: status }
            assertions:
              - { type: status, value: 204 }
`)
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HMAC_SECRET", secret)
	project, err := model.Load(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{ExtraVars: map[string]string{"BASE_URL": srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	step := res.Suites[0].Cases[0].Steps[0]
	if step.Status != spi.StatusPassed || step.Attempts != 2 || calls != 2 {
		t.Fatalf("status %s attempts %d calls %d error %+v", step.Status, step.Attempts, calls, step.Error)
	}
	if nonces[0] == nonces[1] || sigs[0] == sigs[1] {
		t.Fatalf("retry reused nonce %q or signature", nonces[0])
	}
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), secret) || strings.Contains(string(blob), sigs[0]) || strings.Contains(string(blob), sigs[1]) {
		t.Fatal("report contains the HMAC secret or a signature")
	}
}

func TestSigV4EnvFallbackSignsBody(t *testing.T) {
	const (
		access  = "AKIATESTEXAMPLE"
		secret  = "test-secret-access-key-value"
		session = "test-session-token-value-012345"
	)
	var gotHash, gotSession, gotAuth, gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		gotSession = r.Header.Get("X-Amz-Security-Token")
		gotAuth = r.Header.Get("Authorization")
		if i := strings.LastIndex(gotAuth, "Signature="); i >= 0 {
			gotSig = gotAuth[i+len("Signature="):]
		}
		if gotHash != hex.EncodeToString(sum[:]) || gotSession != session || !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") || r.Host == "" || r.Header.Get("X-Amz-Date") == "" {
			http.Error(w, "bad sigv4", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	yaml := []byte(`
schemaVersion: "1.0"
name: aws
auth:
  - name: aws
    type: aws_sigv4
    config:
      region: us-east-1
      service: service
suites:
  - name: s
    auth: { use: [aws] }
    cases:
      - name: c
        steps:
          - name: put
            protocol: rest
            request:
              method: PUT
              url: "${BASE_URL}/item"
              body: "payload"
            assertions:
              - { type: status, value: 204 }
`)
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_ACCESS_KEY_ID", access)
	t.Setenv("AWS_SECRET_ACCESS_KEY", secret)
	t.Setenv("AWS_SESSION_TOKEN", session)
	project, err := model.Load(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(bootstrap.Default()).Run(context.Background(), project, Options{ExtraVars: map[string]string{"BASE_URL": srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	step := res.Suites[0].Cases[0].Steps[0]
	if step.Status != spi.StatusPassed {
		t.Fatalf("status %s error %+v auth %s", step.Status, step.Error, gotAuth)
	}
	if !strings.Contains(gotAuth, "Credential="+access+"/") {
		t.Fatal(gotAuth)
	}
	blob, _ := json.Marshal(res)
	text := string(blob)
	if strings.Contains(text, secret) || strings.Contains(text, session) || (gotSig != "" && strings.Contains(text, gotSig)) {
		t.Fatal("report contains the secret, session token, or signature")
	}
}
