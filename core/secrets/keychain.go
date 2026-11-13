package secrets

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

// KeychainService is shared by the CLI, desktop app, and resolver. A project
// scope is an explicit account-name prefix (project/NAME), not a file path.
const KeychainService = "verity"

// KeychainBackend permits tests to use an in-memory store without accessing
// the OS credential store. go-keyring provides the production implementation.
type KeychainBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// ErrKeychainNotFound is what a backend returns for a missing item.
var ErrKeychainNotFound = errors.New("keychain item not found")

// errNoKeychain is returned when no OS backend was linked into the binary.
var errNoKeychain = errors.New("no OS keychain backend in this build")

// defaultKeychain is the OS credential store. It lives in the oskeychain
// package, which the CLI and the desktop app import, so plugin binaries that
// use core/secrets do not link the D-Bus and Windows credential libraries.
var defaultKeychain KeychainBackend

// SetDefaultKeychainBackend installs the OS backend (called by oskeychain).
// Install during initialization, before any concurrent keychain operations.
func SetDefaultKeychainBackend(b KeychainBackend) {
	defaultKeychain = b
	defaultKeychainCache.mu.Lock()
	defaultKeychainCache.entries = nil
	defaultKeychainCache.mu.Unlock()
}

type noKeychain struct{}

func (noKeychain) Get(string, string) (string, error) { return "", errNoKeychain }
func (noKeychain) Set(string, string, string) error   { return errNoKeychain }
func (noKeychain) Delete(string, string) error        { return errNoKeychain }

// Keychain is an ExternalProvider and the shared credential-management API.
// The zero value uses the OS store when one is linked in (see oskeychain);
// constructing one does not open the store. Do not copy after first use.
type Keychain struct {
	Backend KeychainBackend
	// Now supplies the cache clock; nil uses time.Now. Set before first use.
	Now   func() time.Time
	cache keychainCache
}

const keychainTTL = 60 * time.Second

type keychainFlight struct {
	done  chan struct{}
	value string
	err   error
}

type keychainEntry struct {
	mu      sync.Mutex
	op      sync.Mutex // serializes OS reads and mutations for this account
	value   string
	expires time.Time
	flight  *keychainFlight
}

type keychainCache struct {
	mu      sync.Mutex
	entries map[string]*keychainEntry
}

// All zero-value stores share the production provider's cache, so writes from
// the desktop and CLI invalidate values resolved by the external provider.
var defaultKeychainCache keychainCache

func (k *Keychain) entry(name string) *keychainEntry {
	c := &k.cache
	if k.Backend == nil {
		c = &defaultKeychainCache
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*keychainEntry)
	}
	if c.entries[name] == nil {
		c.entries[name] = &keychainEntry{}
	}
	return c.entries[name]
}

func (k *Keychain) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *Keychain) backend() KeychainBackend {
	if k.Backend != nil {
		return k.Backend
	}
	if defaultKeychain != nil {
		return defaultKeychain
	}
	return noKeychain{}
}

var keychainName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

// ValidateKeychainName rejects names that cannot safely appear in a reference.
func ValidateKeychainName(name string) error {
	if len(name) > 200 || !keychainName.MatchString(name) {
		return verr.New(verr.CodeConfig, "keychain name must be 1–200 ASCII letters, digits, underscores, dots, or hyphens, with optional slash-separated scopes; each segment must start with a letter, digit, or underscore")
	}
	return nil
}

// keychainError deliberately discards backend diagnostics: OS tools and D-Bus
// errors may contain credential material. Only a validated account name is safe.
func keychainError(name string, err error) error {
	if errors.Is(err, ErrKeychainNotFound) {
		return verr.Newf(verr.CodeConfig, "keychain item %q not found (service %q)", name, KeychainService)
	}
	return verr.Newf(verr.CodeConfig, "keychain item %q: credential store unavailable or locked; unlock the OS store or use an environment secret in CI", name)
}

func (k *Keychain) Resolve(name string) (string, bool, error) {
	if err := ValidateKeychainName(name); err != nil {
		return "", false, err
	}
	entry := k.entry(name)
	entry.mu.Lock()
	if k.now().Before(entry.expires) {
		value := entry.value
		entry.mu.Unlock()
		return value, true, nil
	}
	if flight := entry.flight; flight != nil {
		entry.mu.Unlock()
		<-flight.done
		return flight.value, flight.err == nil, flight.err
	}
	flight := &keychainFlight{done: make(chan struct{})}
	entry.value, entry.expires, entry.flight = "", time.Time{}, flight
	entry.mu.Unlock()

	// Share concurrent misses, including a failed OS read, without caching
	// errors. The per-account operation lock orders reads against Set/Delete.
	entry.op.Lock()
	defer entry.op.Unlock()
	value, err := k.backend().Get(KeychainService, name)
	if err != nil {
		err = keychainError(name, err)
		value = ""
	}
	entry.mu.Lock()
	if entry.flight == flight {
		entry.flight = nil
		if err == nil {
			entry.value, entry.expires = value, k.now().Add(keychainTTL)
		}
	}
	flight.value, flight.err = value, err
	close(flight.done)
	entry.mu.Unlock()
	return value, err == nil, err
}

// invalidate is called with the account's operation lock held. Detaching a
// pending read prevents it from repopulating the cache after this mutation.
func (entry *keychainEntry) invalidate() {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.value, entry.expires, entry.flight = "", time.Time{}, nil
}

func (k *Keychain) Set(name, value string) error {
	if err := ValidateKeychainName(name); err != nil {
		return err
	}
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r"))
	if value == "" {
		return verr.New(verr.CodeConfig, "keychain value must not be empty after trimming whitespace")
	}
	entry := k.entry(name)
	entry.op.Lock()
	defer entry.op.Unlock()
	entry.invalidate()
	if err := k.backend().Set(KeychainService, name, value); err != nil {
		return keychainError(name, err)
	}
	return nil
}

func (k *Keychain) Delete(name string) error {
	if err := ValidateKeychainName(name); err != nil {
		return err
	}
	entry := k.entry(name)
	entry.op.Lock()
	defer entry.op.Unlock()
	entry.invalidate()
	if err := k.backend().Delete(KeychainService, name); err != nil {
		return keychainError(name, err)
	}
	return nil
}

// Exists checks the OS store directly, without returning or caching its value.
// A missing item is false; unavailable/locked stores remain configuration errors.
func (k *Keychain) Exists(name string) (bool, error) {
	if err := ValidateKeychainName(name); err != nil {
		return false, err
	}
	entry := k.entry(name)
	entry.op.Lock()
	defer entry.op.Unlock()
	_, err := k.backend().Get(KeychainService, name)
	if errors.Is(err, ErrKeychainNotFound) {
		return false, nil
	}
	if err != nil {
		return false, keychainError(name, err)
	}
	return true, nil
}
