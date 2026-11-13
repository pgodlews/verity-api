// Package plugin implements Verity's out-of-process plugin mechanism: the
// "Verity Plugin Protocol v1" (VPP/1).
//
// Rationale for the chosen mechanism (documented precisely, as required):
//
//   - Plugins run as separate OS processes that the host launches and speaks to
//     over stdin/stdout. This deliberately avoids Go's native -buildmode=plugin,
//     which is brittle across toolchain/version boundaries and platforms.
//   - The wire format is newline-delimited JSON request/response envelopes
//     (JSON-RPC in spirit). This keeps the host and SDK free of a protobuf/gRPC
//     toolchain, so the base binary stays lean and cross-compiles cleanly to all
//     GOOS/GOARCH targets — a core product goal. gRPC and WASM are documented as
//     alternative transports a future protocol version may add; the message
//     contracts here are transport-agnostic.
//   - A plugin ships as its own self-contained binary and is added to a host via
//     the plugins config without recompiling the host.
//
// The same five SPI extension points are supported in-process and over VPP/1:
// Protocol, Assertion, Reporter, AuthProvider, and DataSource. Host-side proxy
// types (see proxy.go and proxy_more.go) implement the spi interfaces by forwarding to the
// subprocess, so the runner cannot tell a plugin from a built-in.
package plugin

// ProtocolVersion is the VPP wire-protocol version. The host refuses to load a
// plugin that advertises an incompatible version during the handshake.
const ProtocolVersion = 1

// Handshake is exchanged once at startup. The plugin prints a Handshake as its
// first line of stdout; the host validates Magic and Version before any calls.
type Handshake struct {
	// Magic must equal MagicCookie to confirm this is a Verity plugin and not
	// some unrelated process writing to stdout.
	Magic string `json:"magic"`
	// Version is the VPP protocol version the plugin speaks.
	Version int `json:"version"`
	// Kind is the extension point this plugin implements: "protocol",
	// "assertion", "reporter", "auth", or "datasource".
	Kind string `json:"kind"`
	// Type is the stable type identifier the extension registers under
	// (e.g. "grpc", "awssigv4").
	Type string `json:"type"`
	// Name and Version of the plugin itself (informational).
	PluginName    string `json:"pluginName,omitempty"`
	PluginVersion string `json:"pluginVersion,omitempty"`
}

// MagicCookie guards against accidentally executing a non-plugin binary.
const MagicCookie = "VERITY_PLUGIN_V1"

// Method identifies the SPI method being invoked over the wire.
type Method string

const (
	MethodBuildRequest Method = "protocol.buildRequest"
	MethodExecute      Method = "protocol.execute"
	MethodEvaluate     Method = "assertion.evaluate"
	MethodApply        Method = "auth.apply"
	MethodLayer        Method = "auth.layer"
	MethodReport       Method = "reporter.report"
	MethodRows         Method = "datasource.rows"
	MethodShutdown     Method = "host.shutdown"
)

// Request is a single RPC envelope from host to plugin.
type Request struct {
	ID     int    `json:"id"`
	Method Method `json:"method"`
	// Params holds the method-specific payload (documented per method in
	// docs/extending-plugins.md).
	Params map[string]any `json:"params,omitempty"`
}

// Response is a single RPC envelope from plugin to host.
type Response struct {
	ID int `json:"id"`
	// Result holds the method-specific payload (documented per method in
	// docs/extending-plugins.md).
	Result map[string]any `json:"result,omitempty"`
	// Error, when non-empty, indicates the call failed. It mirrors verr.Error.
	Error *WireError `json:"error,omitempty"`
}

// WireError is the serialized form of a structured error across the boundary.
type WireError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *WireError) Error() string { return e.Code + ": " + e.Message }
