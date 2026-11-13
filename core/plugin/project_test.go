package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/verr"
)

// TestProjectPluginHelper is a plugin that records that it was started by
// writing the marker file named after "--", then serves a protocol handshake.
func TestProjectPluginHelper(t *testing.T) {
	marker := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			marker = os.Args[i+1]
			break
		}
	}
	if marker == "" {
		return
	}
	defer os.Exit(0)
	_ = os.WriteFile(marker, []byte("started"), 0o644)
	_ = json.NewEncoder(os.Stdout).Encode(Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "protocol", Type: "marker"})
	dec := json.NewDecoder(os.Stdin)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil || req.Method == MethodShutdown {
			return
		}
	}
}

// writeMarkerProject creates <dir>/.verity/plugins.json listing the marker
// plugin and returns the project dir and marker path.
func writeMarkerProject(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	marker = filepath.Join(dir, "plugin-started")
	if err := os.MkdirAll(filepath.Join(dir, ".verity"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(PluginsFile{Plugins: []Manifest{{
		Name: "marker",
		Path: os.Args[0],
		Args: []string{"-test.run=^TestProjectPluginHelper$", "--", marker},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectFile(dir), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, marker
}

func TestLoadForProjectRefusesUntrustedPlugins(t *testing.T) {
	t.Setenv(TrustEnv, "")
	dir, marker := writeMarkerProject(t)
	for name, trust := range map[string]TrustFunc{
		"nil":     nil,
		"refused": func(Fingerprint) (bool, error) { return false, nil },
		"env":     TrustFromEnv,
	} {
		t.Run(name, func(t *testing.T) {
			reg := registry.New()
			mgr, err := LoadForProject(context.Background(), reg, dir, "", trust)
			if mgr != nil {
				mgr.Close()
			}
			got, ok := verr.As(err)
			if !ok || got.Code != verr.CodeConfig {
				t.Fatalf("error = %v, want %s", err, verr.CodeConfig)
			}
			want, _ := filepath.Abs(ProjectFile(dir))
			if got.Details["file"] != want {
				t.Fatalf("details = %#v, want file %s", got.Details, want)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("untrusted plugin was started (marker stat: %v)", statErr)
			}
			if _, perr := reg.Protocol("marker"); perr == nil {
				t.Fatal("untrusted plugin was registered")
			}
		})
	}
}

func TestLoadForProjectTrustedAndExplicit(t *testing.T) {
	t.Setenv(TrustEnv, "")
	cases := map[string]struct {
		trust    TrustFunc
		explicit bool
		env      string
	}{
		"trust func": {trust: TrustAlways},
		"env":        {trust: TrustFromEnv, env: "1"},
		"explicit":   {explicit: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(TrustEnv, tc.env)
			dir, marker := writeMarkerProject(t)
			explicit := ""
			if tc.explicit {
				explicit = ProjectFile(dir)
			}
			var seen Fingerprint
			trust := tc.trust
			if trust != nil {
				inner := trust
				trust = func(fp Fingerprint) (bool, error) { seen = fp; return inner(fp) }
			}
			reg := registry.New()
			mgr, err := LoadForProject(context.Background(), reg, dir, explicit, trust)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			if _, err := reg.Protocol("marker"); err != nil {
				t.Fatalf("trusted plugin not registered: %v", err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("trusted plugin did not start: %v", err)
			}
			if !tc.explicit {
				want, _ := filepath.Abs(ProjectFile(dir))
				if seen.Path != want || len(seen.SHA256) != 64 || len(seen.Binaries) != 1 || seen.Binaries[0] != os.Args[0] {
					t.Fatalf("fingerprint = %#v", seen)
				}
			}
		})
	}
}

func TestLoadForProjectWithoutPluginsFile(t *testing.T) {
	mgr, err := LoadForProject(context.Background(), registry.New(), t.TempDir(), "", nil)
	if err != nil || mgr == nil {
		t.Fatalf("no plugins file = %v, %v", mgr, err)
	}
}

func TestTrustStorePersistsAndInvalidatesOnChange(t *testing.T) {
	dir, _ := writeMarkerProject(t)
	fp, err := FingerprintFile(ProjectFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "state", "trusted-plugins.json")
	store := &TrustStore{Path: storePath}
	if ok, err := store.Trusted(fp); err != nil || ok {
		t.Fatalf("fresh store trusted = %v, %v", ok, err)
	}
	if err := store.Remember(fp); err != nil {
		t.Fatal(err)
	}
	reloaded := &TrustStore{Path: storePath}
	if ok, err := reloaded.Trusted(fp); err != nil || !ok {
		t.Fatalf("reloaded store trusted = %v, %v", ok, err)
	}
	// Any change to the file content asks again.
	if err := os.WriteFile(ProjectFile(dir), []byte(`{"plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := FingerprintFile(ProjectFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Key() == fp.Key() {
		t.Fatal("fingerprint did not change with content")
	}
	if ok, err := reloaded.Trusted(changed); err != nil || ok {
		t.Fatalf("changed file trusted = %v, %v", ok, err)
	}
	// The same content at another path is a different decision.
	moved := fp
	moved.Path += ".moved"
	if moved.Key() == fp.Key() {
		t.Fatal("fingerprint ignores path")
	}
}
