package auth

import (
	"strings"
	"testing"
)

func TestConfigKeyHashesTheResolvedConfig(t *testing.T) {
	a1, _ := configKey("api", map[string]any{"token": "secret-token-aaaa", "header": "X"})
	a2, _ := configKey("api", map[string]any{"header": "X", "token": "secret-token-aaaa"})
	b, _ := configKey("api", map[string]any{"token": "secret-token-bbbb", "header": "X"})
	other, _ := configKey("other", map[string]any{"token": "secret-token-aaaa", "header": "X"})
	if a1 != a2 || a1 == b || a1 == other {
		t.Fatalf("keys %q %q %q %q", a1, a2, b, other)
	}
	if strings.Contains(a1, "secret-token") {
		t.Fatal("cache key holds the config value")
	}
}
