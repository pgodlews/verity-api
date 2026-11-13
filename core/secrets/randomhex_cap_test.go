package secrets

import (
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestRandomHexIsBounded(t *testing.T) {
	r := NewResolver(nil)
	if out, err := r.Interpolate("${fn:randomHex:4096}", true); err != nil || len(out) != 4096 {
		t.Fatalf("randomHex:4096 = %d chars, %v", len(out), err)
	}
	_, err := r.Interpolate("${fn:randomHex:4097}", true)
	if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
		t.Fatalf("randomHex:4097 error = %v, want %s", err, verr.CodeConfig)
	}
}
