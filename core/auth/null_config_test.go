package auth

import (
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
)

// A YAML key with no value (token:) decodes to nil and must count as missing,
// never as the literal text "<nil>".
func TestNullAuthConfigIsMissing(t *testing.T) {
	red := secrets.NewRedactor()
	cases := map[string]struct {
		build func() error
		field string
	}{
		"bearer token": {func() error { _, err := newBearer(map[string]any{"token": nil}, red); return err }, "token"},
		"basic username": {func() error {
			_, err := newBasic(map[string]any{"username": nil, "password": "x"}, red)
			return err
		}, "username"},
		"apikey value": {func() error {
			_, err := newAPIKey(map[string]any{"name": "X-Key", "value": nil}, red)
			return err
		}, "value"},
	}
	for name, tc := range cases {
		err := tc.build()
		if err == nil || !strings.Contains(err.Error(), "'"+tc.field+"'") {
			t.Errorf("%s: error = %v, want a missing '%s' error", name, err, tc.field)
		}
	}
	if got := strConfig(map[string]any{"k": nil}, "k"); got != "" {
		t.Fatalf("strConfig(nil) = %q", got)
	}
}
