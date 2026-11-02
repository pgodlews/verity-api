package secrets

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestInterpolatePlainVar(t *testing.T) {
	r := NewResolver(map[string]string{"BASE_URL": "http://example.com"})
	out, err := r.Interpolate("${BASE_URL}/api", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "http://example.com/api" {
		t.Fatalf("got %q", out)
	}
}

func TestInterpolateEnvSecretRegistered(t *testing.T) {
	t.Setenv("API_TOKEN", "secret-token-xyz-123456")
	r := NewResolver(nil)
	out, err := r.Interpolate("Bearer ${env:API_TOKEN}", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "Bearer secret-token-xyz-123456" {
		t.Fatalf("got %q", out)
	}
	// The resolved env value must be registered as a secret for redaction.
	found := false
	for _, s := range r.SecretValues() {
		if s == "secret-token-xyz-123456" {
			found = true
		}
	}
	if !found {
		t.Fatal("env-resolved secret was not registered for redaction")
	}
}

func TestInterpolateUnknownStrict(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.Interpolate("${MISSING_VAR}", true)
	if err == nil {
		t.Fatal("expected strict error for missing var")
	}
}

func TestInterpolateUnknownLenient(t *testing.T) {
	r := NewResolver(nil)
	out, err := r.Interpolate("${MISSING_VAR}", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "${MISSING_VAR}" {
		t.Fatalf("lenient interpolation should leave unknown refs intact, got %q", out)
	}
}

func TestInterpolateNestedVarChain(t *testing.T) {
	r := NewResolver(map[string]string{"A": "${B}", "B": "final"})
	out, err := r.Interpolate("${A}", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" {
		t.Fatalf("got %q, want %q", out, "final")
	}
}

func TestInterpolateNestedEnvRef(t *testing.T) {
	t.Setenv("TOKEN", "supersecret")
	r := NewResolver(map[string]string{"AUTH": "${env:TOKEN}"})
	out, err := r.Interpolate("value=${AUTH}", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "value=supersecret" {
		t.Fatalf("got %q", out)
	}
	found := false
	for _, s := range r.SecretValues() {
		if s == "supersecret" {
			found = true
		}
	}
	if !found {
		t.Fatal("secret resolved through a nested reference was not registered for redaction")
	}
}

func TestInterpolateCycleDetected(t *testing.T) {
	r := NewResolver(map[string]string{"A": "${B}", "B": "${A}"})
	out, err := r.Interpolate("${A}", false)
	if err == nil {
		t.Fatalf("expected cycle error, got output %q", out)
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error should mention the cycle: %v", err)
	}
}

func TestInterpolateDepthCapExceeded(t *testing.T) {
	vars := map[string]string{}
	for i := 0; i < 12; i++ {
		vars[fmt.Sprintf("V%d", i)] = fmt.Sprintf("${V%d}", i+1)
	}
	vars["V12"] = "leaf"
	r := NewResolver(vars)
	out, err := r.Interpolate("${V0}", false)
	if err == nil {
		t.Fatalf("expected depth-cap error, got output %q", out)
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("error should mention depth: %v", err)
	}
}

func TestInterpolateShortChainWithinDepthCap(t *testing.T) {
	r := NewResolver(map[string]string{"A": "${B}", "B": "${C}", "C": "leaf"})
	out, err := r.Interpolate("${A}", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "leaf" {
		t.Fatalf("got %q", out)
	}
}

func TestFileSecretProvider(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("file-secret-value-9999\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	r := NewResolver(nil)
	out, err := r.Interpolate("${secret:file:"+f.Name()+"}", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "file-secret-value-9999" {
		t.Fatalf("got %q", out)
	}
}
