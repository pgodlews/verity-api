package plugin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

// TestRobustPluginHelper misbehaves according to the last argument.
func TestRobustPluginHelper(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "bad_magic", "wrong_id":
	default:
		return
	}
	defer os.Exit(0)
	if mode == "bad_magic" {
		_, _ = os.Stderr.WriteString("last words")
		_ = json.NewEncoder(os.Stdout).Encode(Handshake{Magic: "nope", Version: ProtocolVersion, Kind: "assertion", Type: "x"})
		time.Sleep(30 * time.Second) // must be killed and reaped by the host
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "assertion", Type: "x"})
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil || req.Method == MethodShutdown {
			return
		}
		_ = enc.Encode(Response{ID: req.ID + 100, Result: map[string]any{}})
	}
}

func TestHandshakeFailureReapsPlugin(t *testing.T) {
	var buf syncBuffer
	old := hostStderr
	hostStderr = &buf
	t.Cleanup(func() { hostStderr = old })
	start := time.Now()
	_, err := Launch(context.Background(), os.Args[0], "-test.run=^TestRobustPluginHelper$", "--", "bad_magic")
	requirePluginCode(t, err, verr.CodePlugin)
	if time.Since(start) > 10*time.Second {
		t.Fatal("Launch waited for the misbehaving plugin")
	}
	// The trailing stderr line is flushed only after the process was waited for.
	if !strings.Contains(buf.String(), "last words") {
		t.Fatalf("plugin not reaped (stderr %q)", buf.String())
	}
}

func TestMismatchedResponseIDIsRejected(t *testing.T) {
	c, err := Launch(context.Background(), os.Args[0], "-test.run=^TestRobustPluginHelper$", "--", "wrong_id")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.call(MethodLayer, nil)
	requirePluginCode(t, err, verr.CodePlugin)
	if !strings.Contains(err.Error(), "response id") {
		t.Fatalf("error = %v", err)
	}
	if _, err := c.call(MethodLayer, nil); err == nil {
		t.Fatal("desynchronised plugin still used")
	}
}
