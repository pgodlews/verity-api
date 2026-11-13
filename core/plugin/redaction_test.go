package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

// Values an auth plugin adds must be registered with the run redactor.
func TestAuthPluginValuesAreRedacted(t *testing.T) {
	c, err := Launch(context.Background(), os.Args[0], "-test.run=TestHelperProcess", "--", "auth_mock")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	red := secrets.NewRedactor()
	a := &authProxy{client: c, typ: "mock_auth", config: map[string]any{}, red: red}
	req := &spi.Request{Method: "GET", URL: "http://127.0.0.1/", Headers: http.Header{"X-Keep": []string{"plain"}}}
	if err := a.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Cookies["session"] != "abc12345" || req.Headers.Get("X-Added") != "v1" {
		t.Fatalf("request not mutated: %+v", req)
	}
	out := red.Redact("cookie abc12345 header v1 and v2")
	for _, secret := range []string{"abc12345", "v1", "v2"} {
		if strings.Contains(out, secret) {
			t.Fatalf("plugin credential %q not redacted: %q", secret, out)
		}
	}
}

// syncBuffer is a concurrency-safe bytes.Buffer for captured stderr.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStderrPluginHelper is a plugin that writes to stderr when asked to
// shut down: one full line containing a secret and a trailing partial line.
func TestStderrPluginHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "stderr_plugin" {
		return
	}
	defer os.Exit(0)
	_ = json.NewEncoder(os.Stdout).Encode(Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "protocol", Type: "noisy"})
	dec := json.NewDecoder(os.Stdin)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.Method == MethodShutdown {
			_, _ = os.Stderr.WriteString("token=plugin-stderr-secret-777 failed\npanic: partial")
			return
		}
	}
}

func TestPluginStderrIsPrefixedAndRedacted(t *testing.T) {
	var buf syncBuffer
	old := hostStderr
	hostStderr = &buf
	t.Cleanup(func() { hostStderr = old })

	mgr, err := Load(context.Background(), registry.New(), []Manifest{{
		Name: "noisy", Path: os.Args[0], Args: []string{"-test.run=^TestStderrPluginHelper$", "--", "stderr_plugin"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetRedactor(secrets.NewRedactor("plugin-stderr-secret-777"))
	mgr.Close()

	out := buf.String()
	if strings.Contains(out, "plugin-stderr-secret-777") {
		t.Fatalf("secret printed: %q", out)
	}
	if !strings.Contains(out, "[plugin noisy] token=") || !strings.Contains(out, "[plugin noisy] panic: partial") {
		t.Fatalf("plugin stderr not forwarded with prefix: %q", out)
	}
}
