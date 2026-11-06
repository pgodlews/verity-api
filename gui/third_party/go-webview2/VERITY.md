# Verity WebView2 compatibility patch

This is the source needed by Wails v2.12.0 from
`github.com/wailsapp/go-webview2 v1.0.22` (Go module checksum
`h1:YT61F5lj+GGaat5OB96Aa3b4QA+mybD0Ggq6NZijQ58=`).
The unused `pkg/webview2` bindings are omitted. Upstream licenses, including the
loader license, are preserved. Loader DLLs are copied unchanged from that module.

Go sources are normalised with `gofmt`; the only behaviour change is in
`pkg/edge/chromium.go`: `Focus` ignores requests before controller
initialisation and during shutdown, and logs a numeric HRESULT when `MoveFocus`
fails instead of calling the fatal error handler. Other failures keep their
upstream handling. `pkg/edge/focus_test.go` adds regression coverage using a fake
COM controller, without requiring an installed browser or changing system focus.

The fatal focus path in Wails v2 matches the supplied Windows crash stacks.
The related upstream v3 work is documented in
[Wails #5580](https://github.com/wailsapp/wails/issues/5580); it does not patch the
v2 dependency. Remove this local replacement when a compatible upstream release
includes equivalent handling. Keep this copy scoped to the GUI module.

Run on Windows:

```sh
go test ./pkg/edge -run '^TestFocus'
```

Other upstream tests include browser integration and require an installed
WebView2 runtime. The focused regression tests above use only fake COM calls.

The native desktop still needs a Windows retest of Git polling, Alt+Tab,
minimise/restore, and native save/open dialogs, including unsaved edits.
