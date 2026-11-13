package plugin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

// After a timeout kills a plugin, later calls say which plugin it was and
// why it stopped, with a plugin_error code and details.
func TestDeadPluginErrorNamesPluginAndReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "hang_call")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.name = "slowpoke"
	c.Timeout = 100 * time.Millisecond
	if _, err := c.call(MethodEvaluate, map[string]any{}); err == nil {
		t.Fatal("expected a timeout")
	}
	_, err = c.call(MethodEvaluate, map[string]any{})
	ve, ok := verr.As(err)
	if !ok || ve.Code != verr.CodePlugin {
		t.Fatalf("error = %v", err)
	}
	if ve.Details["plugin"] != "slowpoke" || !strings.Contains(ve.Message, `"slowpoke"`) ||
		!strings.Contains(ve.Message, "assertion.evaluate call timed out after 100ms") || !strings.Contains(ve.Message, "does not restart") {
		t.Fatalf("message %q details %v", ve.Message, ve.Details)
	}
}
