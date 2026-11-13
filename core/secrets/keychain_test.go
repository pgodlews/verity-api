package secrets

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

type fakeKeychain struct {
	items map[string]string
	err   error
}

func (f *fakeKeychain) Get(service, user string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if value, ok := f.items[service+":"+user]; ok {
		return value, nil
	}
	return "", ErrKeychainNotFound
}

func (f *fakeKeychain) Set(service, user, password string) error {
	if f.err != nil {
		return f.err
	}
	f.items[service+":"+user] = password
	return nil
}

func (f *fakeKeychain) Delete(service, user string) error {
	if _, err := f.Get(service, user); err != nil {
		return err
	}
	delete(f.items, service+":"+user)
	return nil
}

func withKeychainProvider(t *testing.T, provider *Keychain) {
	t.Helper()
	extMu.RLock()
	old := extProviders["keychain"]
	extMu.RUnlock()
	RegisterExternalProvider("keychain", provider)
	t.Cleanup(func() { RegisterExternalProvider("keychain", old) })
}

func TestKeychainResolutionAndRedaction(t *testing.T) {
	fake := &fakeKeychain{items: map[string]string{}}
	store := &Keychain{Backend: fake}
	withKeychainProvider(t, store)
	for _, name := range []string{"TOKEN", "project/TOKEN"} {
		t.Run(name, func(t *testing.T) {
			value := "test-credential-" + name
			if err := store.Set(name, value); err != nil {
				t.Fatal(err)
			}
			resolver := NewResolver(map[string]string{"indirect": "${secret:keychain:" + name + "}"})
			got, err := resolver.Interpolate("Bearer ${indirect}", true)
			if err != nil || got != "Bearer "+value {
				t.Fatalf("resolution failed: %v", err)
			}
			if redacted := NewRedactor(resolver.SecretValues()...).Redact("echo " + value); redacted != "echo "+Mask {
				t.Fatal("resolved keychain value was not redacted")
			}
		})
	}
	if err := store.Delete("TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve("TOKEN"); err == nil {
		t.Fatal("deleted item still resolves")
	}
	if got, ok, err := store.Resolve("project/TOKEN"); err != nil || !ok || got == "" {
		t.Fatal("deleting an unscoped item affected the scoped item")
	}
}

func TestKeychainErrorsAreSafeConfigErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"missing", fmt.Errorf("wrapped: %w", ErrKeychainNotFound), "not found"},
		{"locked", errors.New("locked: test-sensitive-value"), "unavailable or locked"},
		{"headless", errors.New("no store: test-sensitive-value"), "unavailable or locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &Keychain{Backend: &fakeKeychain{err: tc.err}}
			withKeychainProvider(t, store)
			for _, strict := range []bool{false, true} {
				_, err := NewResolver(nil).Interpolate("${secret:keychain:project/TOKEN}", strict)
				ve, ok := verr.As(err)
				if !ok || ve.Code != verr.CodeConfig || !strings.Contains(ve.Message, "project/TOKEN") || !strings.Contains(ve.Message, tc.want) {
					t.Fatalf("unexpected error: %v", err)
				}
				if strings.Contains(err.Error(), "test-sensitive-value") || errors.Unwrap(err) != nil {
					t.Fatal("backend diagnostic escaped")
				}
			}
			for _, err := range []error{store.Set("TOKEN", "test-sensitive-value"), store.Delete("TOKEN")} {
				if ve, ok := verr.As(err); !ok || ve.Code != verr.CodeConfig || strings.Contains(err.Error(), "test-sensitive-value") {
					t.Fatalf("unsafe management error: %v", err)
				}
			}
		})
	}
}

func TestKeychainValuesAreOpaque(t *testing.T) {
	value := "literal-${fn:private-fragment}-${missing}"
	withKeychainProvider(t, &Keychain{Backend: &fakeKeychain{items: map[string]string{"verity:TOKEN": value}}})
	resolver := NewResolver(nil)
	got, err := resolver.Interpolate("${secret:keychain:TOKEN}", true)
	if err != nil || got != value {
		t.Fatal("keychain credential contents were expanded")
	}
	if NewRedactor(resolver.SecretValues()...).Redact(value) != Mask {
		t.Fatal("opaque value was not registered")
	}
}

func TestKeychainNames(t *testing.T) {
	for _, name := range []string{"TOKEN", "project/TOKEN", "project/sub/TOKEN", "a-b.c_1"} {
		if err := ValidateKeychainName(name); err != nil {
			t.Errorf("valid name %q: %v", name, err)
		}
	}
	store := &Keychain{Backend: &fakeKeychain{items: map[string]string{}}}
	for _, name := range []string{"", "/TOKEN", "project/", "project//TOKEN", "../TOKEN", "TOKEN\n", "x:y", "x}", strings.Repeat("x", 201)} {
		if _, _, err := store.Resolve(name); err == nil {
			t.Errorf("accepted invalid name %q", name)
		}
		if err := store.Set(name, "value"); err == nil {
			t.Errorf("Set accepted invalid name %q", name)
		}
		if err := store.Delete(name); err == nil {
			t.Errorf("Delete accepted invalid name %q", name)
		}
	}
}

// Without an OS backend linked in (plugin binaries), resolution fails as an
// unavailable store, not a panic.
func TestKeychainWithoutBackendIsUnavailable(t *testing.T) {
	prev := defaultKeychain
	defaultKeychain = nil
	defer func() { defaultKeychain = prev }()
	if _, _, err := (&Keychain{}).Resolve("ANY_NAME"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v", err)
	}
}

// The fake counts OS calls and supports concurrent use without a real store.
type countingKeychain struct {
	mu sync.Mutex
	fakeKeychain
	gets int
}

func (f *countingKeychain) Get(service, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	return f.fakeKeychain.Get(service, name)
}

func (f *countingKeychain) Set(service, name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fakeKeychain.Set(service, name, value)
}

func (f *countingKeychain) Delete(service, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fakeKeychain.Delete(service, name)
}

func TestKeychainCacheConcurrentExpiryAndInvalidation(t *testing.T) {
	now := time.Unix(1000, 0)
	fake := &countingKeychain{fakeKeychain: fakeKeychain{items: map[string]string{"verity:TOKEN": "first-value"}}}
	store := &Keychain{Backend: fake, Now: func() time.Time { return now }}
	resolve := func(want string) {
		t.Helper()
		got, ok, err := store.Resolve("TOKEN")
		if err != nil || !ok || got != want {
			t.Fatalf("resolve failed: %v", err)
		}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, ok, err := store.Resolve("TOKEN")
			if err != nil || !ok || got != "first-value" {
				t.Error("concurrent resolution failed")
			}
		}()
	}
	close(start)
	wg.Wait()
	if fake.gets != 1 {
		t.Fatalf("concurrent miss made %d OS reads", fake.gets)
	}
	fake.items["verity:TOKEN"] = "external-value"
	now = now.Add(59 * time.Second)
	resolve("first-value")
	now = now.Add(time.Second)
	resolve("external-value")
	if fake.gets != 2 {
		t.Fatalf("expiry made %d OS reads", fake.gets)
	}
	if err := store.Set("TOKEN", "new-value"); err != nil {
		t.Fatal(err)
	}
	resolve("new-value")
	if fake.gets != 3 {
		t.Fatal("set did not invalidate cache")
	}
	if err := store.Delete("TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve("TOKEN"); err == nil {
		t.Fatal("delete left a cached value")
	}
	before := fake.gets
	if _, _, err := store.Resolve("TOKEN"); err == nil || fake.gets != before+1 {
		t.Fatal("not-found error was cached")
	}
	fake.err = errors.New("locked")
	before = fake.gets
	for i := 0; i < 2; i++ {
		if _, _, err := store.Resolve("TOKEN"); err == nil {
			t.Fatal("expected locked error")
		}
	}
	if fake.gets != before+2 {
		t.Fatal("unavailable error was cached")
	}
	fake.err = nil
	fake.items["verity:TOKEN"] = "unlocked-value"
	resolve("unlocked-value")
	if _, _, err := store.Resolve("OTHER"); err == nil {
		t.Fatal("accounts shared a cache entry")
	}
}

func TestKeychainDefaultCacheInvalidatedAcrossCallers(t *testing.T) {
	old := defaultKeychain
	defer func() { SetDefaultKeychainBackend(old) }()
	fake := &countingKeychain{fakeKeychain: fakeKeychain{items: map[string]string{"verity:SHARED": "first"}}}
	SetDefaultKeychainBackend(fake)
	provider, manager := &Keychain{}, &Keychain{}
	if _, _, err := provider.Resolve("SHARED"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set("SHARED", "second"); err != nil {
		t.Fatal(err)
	}
	if got, _, err := provider.Resolve("SHARED"); err != nil || got != "second" {
		t.Fatal("management write did not invalidate provider")
	}
	if err := manager.Delete("SHARED"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.Resolve("SHARED"); err == nil {
		t.Fatal("management delete did not invalidate provider")
	}
}

func TestKeychainExistsAndTrim(t *testing.T) {
	fake := &fakeKeychain{items: map[string]string{}}
	store := &Keychain{Backend: fake}
	if exists, err := store.Exists("TOKEN"); exists || err != nil {
		t.Fatalf("missing: %v, %v", exists, err)
	}
	for _, value := range []string{" value ", "\t value\r\n", "\u0085value\u2003"} {
		if err := store.Set("TOKEN", value); err != nil {
			t.Fatal(err)
		}
		if fake.items["verity:TOKEN"] != "value" {
			t.Fatal("store did not trim whitespace")
		}
	}
	if exists, err := store.Exists("TOKEN"); !exists || err != nil {
		t.Fatal("existing item not found")
	}
	for _, value := range []string{"", " \t\r\n", "\u0085\u2003"} {
		if err := store.Set("TOKEN", value); err == nil {
			t.Fatal("accepted empty credential")
		}
	}
	if fake.items["verity:TOKEN"] != "value" {
		t.Fatal("empty value overwrote credential")
	}
	if exists, err := store.Exists("bad:name"); exists || err == nil {
		t.Fatal("accepted invalid existence lookup")
	}
	if _, _, err := store.Resolve("TOKEN"); err != nil {
		t.Fatal(err)
	}
	fake.err = errors.New("locked: test-sensitive-value")
	if exists, err := store.Exists("TOKEN"); exists || err == nil || !strings.Contains(err.Error(), "unavailable") || strings.Contains(err.Error(), "test-sensitive-value") {
		t.Fatalf("unsafe existence error: %v", err)
	}
}

// Failed mutations must not leave a previously resolved credential usable
// from cache while the OS store reports it unavailable.
func TestKeychainFailedMutationsInvalidate(t *testing.T) {
	for _, action := range []string{"set", "delete"} {
		t.Run(action, func(t *testing.T) {
			fake := &fakeKeychain{items: map[string]string{"verity:TOKEN": "old-value"}}
			store := &Keychain{Backend: fake}
			if _, _, err := store.Resolve("TOKEN"); err != nil {
				t.Fatal(err)
			}
			fake.err = errors.New("locked")
			var err error
			if action == "set" {
				err = store.Set("TOKEN", "new-value")
			} else {
				err = store.Delete("TOKEN")
			}
			if err == nil {
				t.Fatal("expected mutation failure")
			}
			if _, _, err := store.Resolve("TOKEN"); err == nil {
				t.Fatal("failed mutation left a stale cache entry")
			}
		})
	}
}
