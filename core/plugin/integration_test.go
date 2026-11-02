package plugin_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
)

// TestExampleAssertionPlugin builds the bundled example-assertion plugin and
// drives it through the real VPP/1 subprocess protocol, asserting the host-side
// proxy behaves like an in-process assertion. This is the end-to-end proof that
// the out-of-process plugin mechanism works without recompiling the host.
func TestExampleAssertionPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("skips subprocess build in -short mode")
	}
	// Locate the plugin module relative to this test file.
	pluginDir, err := filepath.Abs("../../plugins/example-assertion")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Skipf("example plugin not present: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "example-assertion")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = pluginDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}

	reg := registry.New()
	mgr, err := plugin.Load(context.Background(), reg, []plugin.Manifest{
		{Name: "example-assertion", Path: bin},
	})
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	defer mgr.Close()

	a, err := reg.Assertion("json_array_len")
	if err != nil {
		t.Fatalf("plugin assertion not registered: %v", err)
	}

	actx := &spi.AssertionContext{Response: &spi.Response{Body: []byte(`{"tags":["x","y","z"]}`)}}
	res, err := a.Evaluate(context.Background(), actx, spi.AssertionSpec{
		Expr: "tags", Op: "eq", Value: float64(3),
	})
	if err != nil {
		t.Fatalf("evaluate via plugin: %v", err)
	}
	if !res.Passed {
		t.Fatalf("expected pass from plugin, got %+v", res)
	}

	// And a failing case to confirm round-trip of negative results.
	res2, _ := a.Evaluate(context.Background(), actx, spi.AssertionSpec{
		Expr: "tags", Op: "eq", Value: float64(99),
	})
	if res2.Passed {
		t.Fatal("expected fail from plugin for wrong length")
	}
}
