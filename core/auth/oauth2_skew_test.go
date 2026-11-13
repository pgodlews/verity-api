package auth

import (
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestOAuth2RejectsInvalidRefreshSkew(t *testing.T) {
	base := map[string]any{"tokenUrl": "http://127.0.0.1/token", "clientId": "id", "clientSecret": "client-secret-xyz"}
	for _, skew := range []any{"soon", "-5", "100000", 1.5} {
		cfg := map[string]any{"refreshSkewSeconds": skew}
		for k, v := range base {
			cfg[k] = v
		}
		_, err := newOAuth2(cfg, secrets.NewRedactor())
		if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
			t.Errorf("refreshSkewSeconds %v: error = %v, want %s", skew, err, verr.CodeConfig)
		}
	}
	cfg := map[string]any{"refreshSkewSeconds": 60}
	for k, v := range base {
		cfg[k] = v
	}
	if _, err := newOAuth2(cfg, secrets.NewRedactor()); err != nil {
		t.Fatalf("valid skew: %v", err)
	}
}
