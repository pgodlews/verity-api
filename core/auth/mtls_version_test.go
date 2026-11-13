package auth

import (
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestMTLSRejectsUnknownTLSVersion(t *testing.T) {
	c := genCert(t, "client", false, nil, nil, nil)
	base := func() map[string]any {
		return map[string]any{"certPEM": string(c.certPEM), "keyPEM": string(c.keyPEM)}
	}
	for _, tc := range []map[string]any{{"minVersion": "1.4"}, {"maxVersion": "TLSv1.2"}, {"minVersion": "1.3", "maxVersion": "1.2"}} {
		cfg := base()
		for k, v := range tc {
			cfg[k] = v
		}
		_, err := newMTLS(cfg, secrets.NewRedactor())
		if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
			t.Errorf("%v: error = %v, want %s", tc, err, verr.CodeConfig)
		}
	}
	cfg := base()
	cfg["minVersion"] = "1.2"
	if _, err := newMTLS(cfg, secrets.NewRedactor()); err != nil {
		t.Fatalf("valid minVersion: %v", err)
	}
}
