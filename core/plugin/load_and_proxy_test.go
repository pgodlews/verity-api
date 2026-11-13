package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestCoverageHelperProcess(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	if mode != "utility_mock" {
		return
	}
	defer os.Exit(0)
	_ = json.NewEncoder(os.Stdout).Encode(Handshake{Magic: MagicCookie, Version: ProtocolVersion, Kind: "datasource", Type: "utility"})
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		resp := Response{ID: req.ID, Result: map[string]any{"ok": true}}
		switch req.Method {
		case MethodShutdown:
			return
		case MethodRows:
			if bad, _ := req.Params["config"].(map[string]any); bad["badRows"] == true {
				resp.Result["rows"] = []map[string]any{{"id": 1}}
			} else {
				resp.Result["rows"] = []map[string]string{{"id": "1"}}
			}
		case MethodReport:
			if req.Params["out"] == "fail" {
				resp.Result = nil
				resp.Error = &WireError{Code: string(verr.CodeConfig), Message: "report rejected"}
			}
		default:
			resp.Result = nil
			resp.Error = &WireError{Code: string(verr.CodePlugin), Message: "unsupported"}
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func requirePluginCode(t *testing.T, err error, code verr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s", code)
	}
	got, ok := verr.As(err)
	if !ok || got.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestLoadFileLifecycleAndErrors(t *testing.T) {
	ctx := context.Background()
	mgr, err := LoadFile(ctx, registry.New(), filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || mgr == nil || len(mgr.clients) != 0 {
		t.Fatalf("missing plugins file = %#v, %v", mgr, err)
	}
	dir := t.TempDir()
	malformed := filepath.Join(dir, "malformed.json")
	if err := os.WriteFile(malformed, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadFile(ctx, registry.New(), malformed)
	requirePluginCode(t, err, verr.CodePlugin)
	_, err = LoadFile(ctx, registry.New(), dir)
	requirePluginCode(t, err, verr.CodePlugin)
	_, err = Load(ctx, registry.New(), []Manifest{{Path: filepath.Join(dir, "missing-plugin")}})
	requirePluginCode(t, err, verr.CodePlugin)

	bin := filepath.Join(dir, "plugin-bin")
	if err := os.Symlink(os.Args[0], bin); err != nil {
		t.Fatal(err)
	}
	disabled := false
	config := filepath.Join(dir, "plugins.json")
	body, err := json.Marshal(PluginsFile{Plugins: []Manifest{
		{Name: "disabled", Path: "missing-plugin", Enabled: &disabled},
		{Name: "echo", Path: "plugin-bin", Args: []string{"-test.run=TestHelperProcess", "--", "protocol_echo"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, body, 0o644); err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	mgr, err = LoadFile(ctx, reg, config)
	if err != nil {
		t.Fatal(err)
	}
	protocol, err := reg.Protocol("echo")
	if err != nil || protocol.Type() != "echo" || !HidesDial(protocol) {
		t.Fatalf("registered protocol = %#v, %v", protocol, err)
	}
	mgr.Close()
	if len(mgr.clients) != 0 {
		t.Fatal("manager retained clients after Close")
	}
	mgr.Close()
}

func TestRegisterAllKindsAndUnknown(t *testing.T) {
	reg := registry.New()
	for _, kind := range []string{"protocol", "assertion", "datasource", "auth", "reporter"} {
		client := &Client{hs: Handshake{Kind: kind, Type: kind + "-type"}}
		if err := register(reg, client); err != nil {
			t.Fatalf("register %s: %v", kind, err)
		}
	}
	if got, err := reg.Protocol("protocol-type"); err != nil || got.Type() != "protocol-type" {
		t.Fatalf("protocol = %#v, %v", got, err)
	}
	if got, err := reg.Assertion("assertion-type"); err != nil || got.Type() != "assertion-type" {
		t.Fatalf("assertion = %#v, %v", got, err)
	}
	if got, err := reg.DataSource("datasource-type"); err != nil || got.Type() != "datasource-type" {
		t.Fatalf("datasource = %#v, %v", got, err)
	}
	if got, err := reg.Reporter("reporter-type"); err != nil || got.Type() != "reporter-type" {
		t.Fatalf("reporter = %#v, %v", got, err)
	}
	factory, err := reg.AuthFactory("auth-type")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := factory(map[string]any{"key": "value"}, secrets.NewRedactor())
	if err != nil || provider.Type() != "auth-type" {
		t.Fatalf("auth provider = %#v, %v", provider, err)
	}
	err = register(reg, &Client{hs: Handshake{Kind: "unknown"}})
	requirePluginCode(t, err, verr.CodePlugin)
}

func TestDataSourceAndReporterProxies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Launch(ctx, os.Args[0], "-test.run=TestCoverageHelperProcess", "--", "utility_mock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	source := &dataSourceProxy{client: client, typ: "utility"}
	rows, err := source.Rows("project", map[string]any{"key": "value"}, []map[string]string{{"id": "inline"}}, "feed.json")
	if err != nil || len(rows) != 1 || rows[0]["id"] != "1" {
		t.Fatalf("Rows = %#v, %v", rows, err)
	}
	_, err = source.Rows("", map[string]any{"badRows": true}, nil, "")
	requirePluginCode(t, err, verr.CodePlugin)

	reporter := &reporterProxy{client: client, typ: "utility"}
	if err := reporter.Report(&spi.RunResult{Summary: spi.Summary{Status: spi.StatusPassed}}, "report.json"); err != nil {
		t.Fatal(err)
	}
	err = reporter.Report(&spi.RunResult{}, "fail")
	requirePluginCode(t, err, verr.CodeConfig)
	if !strings.Contains(err.Error(), "report rejected") {
		t.Fatalf("report error = %v", err)
	}
	badResult := &spi.RunResult{Suites: []spi.SuiteResult{{Cases: []spi.CaseResult{{Steps: []spi.StepResult{{Error: &spi.ErrorInfo{Details: map[string]any{"bad": func() {}}}}}}}}}}
	err = reporter.Report(badResult, "")
	requirePluginCode(t, err, verr.CodePlugin)
}

func TestWireErrorsAndDecodeEdges(t *testing.T) {
	wire := &WireError{Code: "plugin_error", Message: "failed"}
	if got := wire.Error(); got != "plugin_error: failed" {
		t.Fatalf("WireError.Error = %q", got)
	}
	_, err := DecodeRequest(nil)
	requirePluginCode(t, err, verr.CodePlugin)
	_, err = DecodeResponse(nil)
	requirePluginCode(t, err, verr.CodePlugin)
	req, err := DecodeRequest(map[string]any{
		"headers": map[string]string{"X-Test": "one"},
		"query":   map[string]string{"page": "1"},
		"cookies": map[string]any{"session": 2},
	})
	if err != nil || req.Headers.Get("X-Test") != "one" || req.Query["page"] != "1" || req.Cookies["session"] != "" || len(req.Body) != 0 {
		t.Fatalf("decoded request = %#v, %v", req, err)
	}
	// A body that is not valid base64 is an error, not a silently empty body.
	_, err = DecodeRequest(map[string]any{"body": "not-base64"})
	requirePluginCode(t, err, verr.CodePlugin)
	_, err = DecodeResponse(map[string]any{"body": "not-base64"})
	requirePluginCode(t, err, verr.CodePlugin)
	headers := http.Header{}
	DecodeHeaders(map[string][]string{"X-Multi": {"one", "two"}}, headers)
	if len(headers.Values("X-Multi")) != 2 {
		t.Fatalf("decoded headers = %#v", headers)
	}
	if got := EncodeHeaders(nil); len(got) != 0 {
		t.Fatalf("EncodeHeaders(nil) = %#v", got)
	}
	for _, tt := range []struct {
		value any
		want  float64
	}{{float32(1), 1}, {int(2), 2}, {int64(3), 3}, {json.Number("4"), 4}, {"other", 0}} {
		if got := asFloat(tt.value); got != tt.want {
			t.Errorf("asFloat(%#v) = %v, want %v", tt.value, got, tt.want)
		}
	}
	var target map[string]any
	if err := remarshal(func() {}, &target); err == nil {
		t.Fatal("remarshal accepted a function")
	}
	if err := remarshal(map[string]any{"key": "value"}, &target); err != nil || target["key"] != "value" {
		t.Fatalf("remarshal = %#v, %v", target, err)
	}
	var nilProtocol *protocolProxy
	nilProtocol.DialHidden()
	if HidesDial(&plainProtocol{}) {
		t.Fatal("plain protocol hides its dial")
	}
}

type plainProtocol struct{}

func (plainProtocol) Type() string { return "plain" }
func (plainProtocol) BuildRequest(context.Context, map[string]any) (*spi.Request, error) {
	return nil, errors.New("not implemented")
}
func (plainProtocol) Execute(context.Context, *spi.ProtocolContext, *spi.Request) (*spi.Response, error) {
	return nil, errors.New("not implemented")
}
