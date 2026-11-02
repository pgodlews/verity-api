// Package oskeychain links the operating system's credential store into
// ${secret:keychain:NAME}: macOS Keychain, the Linux Secret Service over
// D-Bus, or Windows Credential Manager, through go-keyring. Import it for its
// side effect from binaries that should read the OS store (the CLI and the
// desktop app); plugins built on core/secrets do not need it.
package oskeychain

import (
	"errors"

	"github.com/zalando/go-keyring"

	"github.com/pgodlews/verity-api/core/secrets"
)

func init() { secrets.SetDefaultKeychainBackend(backend{}) }

type backend struct{}

func (backend) Get(service, user string) (string, error) {
	v, err := keyring.Get(service, user)
	return v, notFound(err)
}

func (backend) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (backend) Delete(service, user string) error {
	return notFound(keyring.Delete(service, user))
}

func notFound(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return secrets.ErrKeychainNotFound
	}
	return err
}
