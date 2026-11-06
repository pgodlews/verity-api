//go:build windows

package edge

import (
	"bytes"
	"log"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFocusLifecycle(t *testing.T) {
	for _, state := range []string{"no controller", "initialising", "shutting down"} {
		t.Run(state, func(t *testing.T) {
			calls := 0
			controller := &ICoreWebView2Controller{vtbl: &_ICoreWebView2ControllerVtbl{
				MoveFocus: NewComProc(func(_, _ uintptr) uintptr { calls++; return 0 }),
			}}
			e := &Chromium{controller: controller}
			switch state {
			case "no controller":
				atomic.StoreUintptr(&e.inited, 1)
				e.controller = nil
			case "shutting down":
				atomic.StoreUintptr(&e.inited, 1)
				e.ShuttingDown()
			}
			e.Focus()
			if calls != 0 {
				t.Fatalf("MoveFocus called while %s", state)
			}
		})
	}
}

func TestFocusFailureIsRecoverable(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	calls := 0
	controller := &ICoreWebView2Controller{vtbl: &_ICoreWebView2ControllerVtbl{
		MoveFocus: NewComProc(func(_, reason uintptr) uintptr {
			calls++
			if reason != uintptr(COREWEBVIEW2_MOVE_FOCUS_REASON_PROGRAMMATIC) {
				t.Errorf("focus reason: %d", reason)
			}
			if calls == 1 {
				return 0x80070057 // E_INVALIDARG: transient native focus failure.
			}
			return 0
		}),
	}}
	e := &Chromium{controller: controller, globalErrorCallback: func(error) {
		t.Error("focus failure reached the fatal error callback")
	}}
	atomic.StoreUintptr(&e.inited, 1)
	e.Focus()
	e.Focus() // A later focus event must still be delivered after the failure.
	if calls != 2 {
		t.Fatalf("MoveFocus calls: %d", calls)
	}
	if got := output.String(); !strings.Contains(got, "HRESULT 0x80070057") || strings.Contains(got, "Stack trace") {
		t.Fatalf("expected safe numeric diagnostic: %q", got)
	}
}
