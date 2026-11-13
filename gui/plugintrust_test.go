package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
)

func TestPluginTrustAsksOnceAndRemembers(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "trusted-plugins.json")
	asked := 0
	answer := true
	pt := &pluginTrust{
		store:   &plugin.TrustStore{Path: storePath},
		confirm: func(plugin.Fingerprint) (bool, error) { asked++; return answer, nil },
	}
	fp := plugin.Fingerprint{Path: "/p/.verity/plugins.json", SHA256: "aa", Binaries: []string{"/p/bin"}}

	for i := 0; i < 2; i++ {
		ok, err := pt.trust(fp)
		if err != nil || !ok {
			t.Fatalf("trust #%d = %v, %v", i, ok, err)
		}
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want 1", asked)
	}

	// A new app process reads the remembered decision without asking.
	again := &pluginTrust{store: &plugin.TrustStore{Path: storePath}, confirm: func(plugin.Fingerprint) (bool, error) {
		t.Fatal("remembered fingerprint asked again")
		return false, nil
	}}
	if ok, err := again.trust(fp); err != nil || !ok {
		t.Fatalf("remembered trust = %v, %v", ok, err)
	}

	// A changed file is a new question, and a refusal is not remembered.
	changed := fp
	changed.SHA256 = "bb"
	answer = false
	for i := 0; i < 2; i++ {
		if ok, err := pt.trust(changed); err != nil || ok {
			t.Fatalf("refused trust = %v, %v", ok, err)
		}
	}
	if asked != 3 {
		t.Fatalf("asked %d times, want 3", asked)
	}

	failing := &pluginTrust{store: &plugin.TrustStore{}, confirm: func(plugin.Fingerprint) (bool, error) { return false, errors.New("dialog failed") }}
	if ok, err := failing.trust(changed); err == nil || ok {
		t.Fatalf("dialog error = %v, %v", ok, err)
	}
}

func TestAppWithoutWindowRefusesProjectPlugins(t *testing.T) {
	app := &App{}
	if ok, err := app.trustPlugins(plugin.Fingerprint{Path: "x"}); err != nil || ok {
		t.Fatalf("zero App trust = %v, %v", ok, err)
	}
	if ok, err := app.confirmPlugins(plugin.Fingerprint{Path: "x"}); err != nil || ok {
		t.Fatalf("confirm without window = %v, %v", ok, err)
	}
}
