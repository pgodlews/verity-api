package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/verr"
)

type desktopKeychain struct {
	service, name, value string
	err                  error
}

func (f *desktopKeychain) Get(string, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.name == "" {
		return "", secrets.ErrKeychainNotFound
	}
	return f.value, nil
}

func (f *desktopKeychain) Delete(string, string) error {
	return f.err
}

func (f *desktopKeychain) Set(service, name, value string) error {
	f.service, f.name, f.value = service, name, value
	return f.err
}

func TestDesktopKeychainBinding(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		fake := &desktopKeychain{}
		if unavailable {
			fake.err = errors.New("locked: test-sensitive-value")
		}
		app := &App{keychain: secrets.Keychain{Backend: fake}}
		ref, err := app.StoreKeychainSecret("project/TOKEN", "test-sensitive-value")
		if unavailable {
			ve, ok := verr.As(err)
			if !ok || ve.Code != verr.CodeConfig || ref != "" || strings.Contains(err.Error(), "test-sensitive-value") {
				t.Fatalf("unsafe store failure: %v", err)
			}
			continue
		}
		if err != nil || ref != "${secret:keychain:project/TOKEN}" {
			t.Fatalf("binding result: %q, %v", ref, err)
		}
		if fake.service != "verity" || fake.name != "project/TOKEN" || fake.value != "test-sensitive-value" {
			t.Fatal("binding stored the wrong item")
		}
	}
	app := &App{keychain: secrets.Keychain{Backend: &desktopKeychain{}}}
	if _, err := app.StoreKeychainSecret("bad:name", "test-sensitive-value"); err == nil {
		t.Fatal("binding accepted an invalid account name")
	}
}

func TestDesktopKeychainExistsAndTrim(t *testing.T) {
	fake := &desktopKeychain{}
	app := &App{keychain: secrets.Keychain{Backend: fake}}
	if exists, err := app.KeychainSecretExists("project/TOKEN"); exists || err != nil {
		t.Fatalf("missing: %v", err)
	}
	if _, err := app.StoreKeychainSecret("project/TOKEN", " \t test-sensitive-value \r\n"); err != nil {
		t.Fatal(err)
	}
	if fake.value != "test-sensitive-value" {
		t.Fatal("desktop failed to trim")
	}
	if exists, err := app.KeychainSecretExists("project/TOKEN"); !exists || err != nil {
		t.Fatalf("existing: %v", err)
	}
	if _, err := app.StoreKeychainSecret("project/TOKEN", " \t\r\n"); err == nil {
		t.Fatal("desktop accepted empty credential")
	}
	fake.err = errors.New("locked: test-sensitive-value")
	if exists, err := app.KeychainSecretExists("project/TOKEN"); exists || err == nil || strings.Contains(err.Error(), "test-sensitive-value") {
		t.Fatalf("unsafe unavailable lookup: %v", err)
	}
}
