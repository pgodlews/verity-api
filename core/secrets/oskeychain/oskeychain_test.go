package oskeychain

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/pgodlews/verity-api/core/secrets"
)

// Exercise the production adapter with go-keyring's mock, never the OS store.
func TestOSAdapterWithMock(t *testing.T) {
	keyring.MockInit()
	store := secrets.Keychain{}
	if err := store.Set("ADAPTER_TOKEN", "adapter-test-value"); err != nil {
		t.Fatal(err)
	}
	if value, ok, err := store.Resolve("ADAPTER_TOKEN"); err != nil || !ok || value != "adapter-test-value" {
		t.Fatal("adapter did not retrieve the mock value")
	}
	if err := store.Delete("ADAPTER_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve("ADAPTER_TOKEN"); err == nil {
		t.Fatal("adapter did not delete the mock item")
	}
}
