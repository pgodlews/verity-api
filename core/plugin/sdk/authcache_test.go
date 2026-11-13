package sdk

import (
	"context"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/spi"
)

type countingAuth struct{}

func (countingAuth) Type() string                              { return "counting" }
func (countingAuth) Layer() spi.AuthLayer                      { return spi.LayerMessage }
func (countingAuth) Apply(context.Context, *spi.Request) error { return nil }

func TestAuthProvidersAreCachedByConfig(t *testing.T) {
	built := 0
	cache := newAuthCache(func(map[string]any) (spi.AuthProvider, error) { built++; return countingAuth{}, nil })
	call := func(cfg map[string]any) {
		resp := handleAuth(cache.provider, plugin.Request{Method: plugin.MethodLayer, Params: map[string]any{"config": cfg}})
		if resp.Error != nil {
			t.Fatal(resp.Error)
		}
	}
	call(map[string]any{"a": "1", "b": "2"})
	call(map[string]any{"b": "2", "a": "1"})
	if built != 1 {
		t.Fatalf("built %d providers for one config, want 1", built)
	}
	call(map[string]any{"a": "other"})
	if built != 2 {
		t.Fatalf("built %d providers for two configs, want 2", built)
	}
}
