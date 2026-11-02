package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestHelperProcess(t *testing.T) {
	var mode string
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	if mode == "" {
		return
	}
	defer os.Exit(0)

	switch mode {
	case "hang_call":
		hs := Handshake{
			Magic:   MagicCookie,
			Version: ProtocolVersion,
			Kind:    "assertion",
			Type:    "hang",
		}
		_ = json.NewEncoder(os.Stdout).Encode(hs)
		var req Request
		_ = json.NewDecoder(os.Stdin).Decode(&req)
		time.Sleep(10 * time.Second)
	case "crash_on_call":
		hs := Handshake{
			Magic:   MagicCookie,
			Version: ProtocolVersion,
			Kind:    "assertion",
			Type:    "crash",
		}
		_ = json.NewEncoder(os.Stdout).Encode(hs)
		var req Request
		_ = json.NewDecoder(os.Stdin).Decode(&req)
		os.Exit(1)
	case "auth_mock":
		hs := Handshake{
			Magic:   MagicCookie,
			Version: ProtocolVersion,
			Kind:    "auth",
			Type:    "mock_auth",
		}
		_ = json.NewEncoder(os.Stdout).Encode(hs)
		dec := json.NewDecoder(os.Stdin)
		enc := json.NewEncoder(os.Stdout)
		for {
			var req Request
			if err := dec.Decode(&req); err != nil {
				return
			}
			if req.Method == MethodShutdown {
				return
			}
			if req.Method == MethodLayer {
				_ = enc.Encode(Response{
					ID:     req.ID,
					Result: map[string]any{"layer": 0},
				})
			} else if req.Method == MethodApply {
				_ = enc.Encode(Response{
					ID: req.ID,
					Result: map[string]any{
						"request": map[string]any{
							"method":  "GET",
							"url":     "http://example.com",
							"headers": map[string]any{"X-Added": []string{"v1", "v2"}},
							"cookies": map[string]any{"session": "abc12345"},
						},
					},
				})
			}
		}
	case "protocol_echo":
		hs := Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "protocol", Type: "echo"}
		_ = json.NewEncoder(os.Stdout).Encode(hs)
		dec := json.NewDecoder(os.Stdin)
		enc := json.NewEncoder(os.Stdout)
		for {
			var req Request
			if err := dec.Decode(&req); err != nil {
				return
			}
			if req.Method == MethodShutdown {
				return
			}
			payload, _ := json.Marshal(map[string]any{
				"dir":                req.Params["dir"],
				"deadline":           req.Params["deadline"],
				"allowExternalFiles": req.Params["allowExternalFiles"],
			})
			_ = enc.Encode(Response{
				ID: req.ID,
				Result: map[string]any{
					"response": map[string]any{
						"statusCode":  0,
						"status":      "OK",
						"headers":     map[string]any{},
						"body":        base64.StdEncoding.EncodeToString(payload),
						"durationMs":  15,
						"contentType": "application/json",
					},
				},
			})
		}
	case "protocol_config_error":
		hs := Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "protocol", Type: "echo"}
		_ = json.NewEncoder(os.Stdout).Encode(hs)
		dec := json.NewDecoder(os.Stdin)
		enc := json.NewEncoder(os.Stdout)
		for {
			var req Request
			if err := dec.Decode(&req); err != nil {
				return
			}
			if req.Method == MethodShutdown {
				return
			}
			_ = enc.Encode(Response{
				ID: req.ID,
				Error: &WireError{
					Code: "config_error", Message: "bad descriptor",
					Details: map[string]any{"file": "echo.pb"},
				},
			})
		}
	case "idle":
		hs := Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "protocol", Type: "idle"}
		_ = json.NewEncoder(os.Stdout).Encode(hs)
		dec := json.NewDecoder(os.Stdin)
		for {
			var req Request
			if err := dec.Decode(&req); err != nil {
				return
			}
			if req.Method == MethodShutdown {
				return
			}
			os.Exit(2)
		}
	}
}

func TestPluginCallTimeoutAndFailFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "hang_call")
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	defer c.Close()

	c.Timeout = 150 * time.Millisecond

	start := time.Now()
	_, err = c.call(MethodEvaluate, map[string]any{"dummy": true})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("call took too long: %v", elapsed)
	}
	vErr, ok := err.(*verr.Error)
	if !ok || vErr.Code != verr.CodePlugin {
		t.Fatalf("expected plugin_error code, got %v", err)
	}
	if !c.dead {
		t.Fatal("expected client to be marked dead")
	}

	// Subsequent calls must fail fast
	startFast := time.Now()
	_, errFast := c.call(MethodEvaluate, map[string]any{"dummy": true})
	if errFast == nil {
		t.Fatal("expected immediate error for dead client, got nil")
	}
	if time.Since(startFast) > 50*time.Millisecond {
		t.Fatalf("expected fail fast, took %v", time.Since(startFast))
	}
}

func TestPluginCallCrashAndFailFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "crash_on_call")
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}
	defer c.Close()

	_, err = c.call(MethodEvaluate, map[string]any{"dummy": true})
	if err == nil {
		t.Fatal("expected error on subprocess crash, got nil")
	}
	if !c.dead {
		t.Fatal("expected client to be marked dead")
	}

	// Subsequent calls must fail fast
	_, errFast := c.call(MethodEvaluate, map[string]any{"dummy": true})
	if errFast == nil {
		t.Fatal("expected immediate error for dead client, got nil")
	}
}

func TestHidesDial(t *testing.T) {
	if !HidesDial(&protocolProxy{typ: "grpc"}) {
		t.Fatal("plugin protocol hides its dial")
	}
	if HidesDial(nil) {
		t.Fatal("nil protocol does not hide a dial")
	}
}

func TestPluginCallHonoursContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	idle, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "idle")
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	start := time.Now()
	_, err = idle.callTimeout(cancelled, MethodExecute, map[string]any{"x": true}, time.Second)
	if err == nil || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("cancelled call err=%v elapsed=%s", err, time.Since(start))
	}
	if idle.dead {
		t.Fatal("a call that never started should leave the plugin usable")
	}

	hang, err := Launch(ctx, os.Args[0], "-test.run=TestHelperProcess", "--", "hang_call")
	if err != nil {
		t.Fatal(err)
	}
	defer hang.Close()
	runCtx, runCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		runCancel()
	}()
	start = time.Now()
	_, err = hang.callTimeout(runCtx, MethodExecute, map[string]any{"x": true}, 10*time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancel")
	}
	if elapsed > time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	if !hang.dead {
		t.Fatal("in-flight cancel should abandon the plugin read")
	}
}
