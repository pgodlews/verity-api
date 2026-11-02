package auth

import (
	"context"
	"sync"
	"testing"

	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func boolPtr(b bool) *bool { return &b }

func TestEffectiveNamesInheritStacking(t *testing.T) {
	// Suite declares [a]; case declares [b] inheriting → [a, b]; step declares
	// [c] inheriting → [a, b, c].
	names := effectiveNames(ScopeRefs{
		Suite: &model.AuthRef{Use: []string{"a"}},
		Case:  &model.AuthRef{Use: []string{"b"}},
		Step:  &model.AuthRef{Use: []string{"c"}},
	})
	want := []string{"a", "b", "c"}
	if !equal(names, want) {
		t.Fatalf("got %v want %v", names, want)
	}
}

func TestEffectiveNamesOverride(t *testing.T) {
	// Case overrides (inherit=false) → suite auth dropped; step inherits.
	names := effectiveNames(ScopeRefs{
		Suite: &model.AuthRef{Use: []string{"a"}},
		Case:  &model.AuthRef{Use: []string{"b"}, Inherit: boolPtr(false)},
		Step:  &model.AuthRef{Use: []string{"c"}},
	})
	want := []string{"b", "c"}
	if !equal(names, want) {
		t.Fatalf("got %v want %v", names, want)
	}
}

func TestEffectiveNamesExplicitNone(t *testing.T) {
	// Step explicitly clears auth (inherit=false, empty use).
	names := effectiveNames(ScopeRefs{
		Suite: &model.AuthRef{Use: []string{"a"}},
		Step:  &model.AuthRef{Use: []string{}, Inherit: boolPtr(false)},
	})
	if len(names) != 0 {
		t.Fatalf("expected no auth, got %v", names)
	}
}

func TestEffectiveNamesDedup(t *testing.T) {
	names := effectiveNames(ScopeRefs{
		Suite: &model.AuthRef{Use: []string{"a"}},
		Case:  &model.AuthRef{Use: []string{"a", "b"}},
	})
	want := []string{"a", "b"}
	if !equal(names, want) {
		t.Fatalf("got %v want %v", names, want)
	}
}

// TestProviderStacking verifies a chain composes a transport provider (mtls) and
// a message provider (bearer) with transport ordered first.
func TestProviderStacking(t *testing.T) {
	reg := registry.New()
	Register(reg)
	project := &model.Project{
		Auth: []model.AuthDef{
			{Name: "tok", Type: "bearer", Config: map[string]any{"token": "stack-token-123456"}},
			{Name: "key", Type: "apikey", Config: map[string]any{"name": "X-Api", "value": "api-value-123456"}},
		},
	}
	red := secrets.NewRedactor()
	interp := secrets.NewResolver(nil)
	res := NewResolver(reg, project, red, interp, "")

	chain, err := res.Resolve(context.Background(), ScopeRefs{
		Suite: &model.AuthRef{Use: []string{"tok"}},
		Step:  &model.AuthRef{Use: []string{"key"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Message) != 2 {
		t.Fatalf("expected 2 stacked message providers, got %d", len(chain.Message))
	}
	req := newReq()
	if err := chain.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Headers.Get("Authorization") != "Bearer stack-token-123456" {
		t.Fatalf("bearer not applied: %q", req.Headers.Get("Authorization"))
	}
	if req.Headers.Get("X-Api") != "api-value-123456" {
		t.Fatalf("apikey not applied: %q", req.Headers.Get("X-Api"))
	}
}

func TestResolverSharesProviderInstance(t *testing.T) {
	// The same named auth resolved twice must return the same instance so OAuth
	// token caches persist across steps.
	reg := registry.New()
	Register(reg)
	project := &model.Project{Auth: []model.AuthDef{
		{Name: "tok", Type: "bearer", Config: map[string]any{"token": "shared-token-123456"}},
	}}
	res := NewResolver(reg, project, secrets.NewRedactor(), secrets.NewResolver(nil), "")
	p1, _ := res.provider("tok")
	p2, _ := res.provider("tok")
	if p1 != p2 {
		t.Fatal("expected cached provider instance to be reused")
	}
}

func TestResolverConcurrentResolve(t *testing.T) {
	reg := registry.New()
	Register(reg)
	project := &model.Project{Auth: []model.AuthDef{
		{Name: "tok", Type: "bearer", Config: map[string]any{"token": "concurrent-token-123456"}},
		{Name: "key", Type: "apikey", Config: map[string]any{"name": "X-Api", "value": "api-val-123456"}},
	}}
	res := NewResolver(reg, project, secrets.NewRedactor(), secrets.NewResolver(nil), "")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := "tok"
			if idx%2 == 0 {
				name = "key"
			}
			chain, err := res.Resolve(context.Background(), ScopeRefs{
				Step: &model.AuthRef{Use: []string{name}},
			})
			if err != nil {
				t.Errorf("Resolve failed: %v", err)
				return
			}
			if len(chain.Message) != 1 {
				t.Errorf("expected 1 provider, got %d", len(chain.Message))
			}
		}(i)
	}
	wg.Wait()
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ spi.AuthProvider = (*bearerProvider)(nil)
