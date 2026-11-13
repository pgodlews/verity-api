package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

func TestSDKHeaderRoundTrip(t *testing.T) {
	req := &spi.Request{
		Method:  "POST",
		URL:     "http://example.com/test",
		Headers: http.Header{},
		Cookies: map[string]string{"auth": "token123"},
		Body:    []byte(`payload`),
	}
	req.Headers.Set("Single", "val")
	req.Headers.Add("Multi", "v1")
	req.Headers.Add("Multi", "v2")

	encReq := plugin.EncodeRequest(req)
	decReq, err := decodeRequest(encReq)
	if err != nil {
		t.Fatalf("decodeRequest failed: %v", err)
	}
	if decReq.Headers.Get("Single") != "val" {
		t.Fatalf("expected Single header val, got %s", decReq.Headers.Get("Single"))
	}
	if len(decReq.Headers.Values("Multi")) != 2 {
		t.Fatalf("expected 2 Multi headers, got %v", decReq.Headers.Values("Multi"))
	}
	if decReq.Cookies["auth"] != "token123" {
		t.Fatalf("expected cookie auth=token123, got %v", decReq.Cookies)
	}

	resp := &spi.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Headers:    http.Header{},
		Body:       []byte(`done`),
		Duration:   10 * time.Millisecond,
	}
	resp.Headers.Add("Set-Cookie", "c1=1")
	resp.Headers.Add("Set-Cookie", "c2=2")

	encResp := plugin.EncodeResponse(resp)
	decResp, err := decodeResponse(encResp)
	if err != nil {
		t.Fatalf("decodeResponse failed: %v", err)
	}
	if len(decResp.Headers.Values("Set-Cookie")) != 2 {
		t.Fatalf("expected 2 Set-Cookie headers, got %v", decResp.Headers.Values("Set-Cookie"))
	}
	if decResp.Duration != 10*time.Millisecond {
		t.Fatalf("duration = %s", decResp.Duration)
	}
}

func TestDecodePluginRequestUsesNumbersAndReturnsStructuredErrors(t *testing.T) {
	data := []byte(`{"id":7,"method":"protocol.build","params":{"n":1e400}}`)
	req, err := decodePluginRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	number, ok := req.Params["n"].(json.Number)
	if !ok || number.String() != "1e400" {
		t.Fatalf("number = %#v, want json.Number(1e400)", req.Params["n"])
	}
	if _, err := decodePluginRequest(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
	bad := []byte(`{"id":7,"method":"protocol.build","params":"invalid"}`)
	_, decodeErr := decodePluginRequest(bad)
	if decodeErr == nil {
		t.Fatal("invalid params were accepted")
	}
	resp := pluginDecodeError(bad, decodeErr)
	if resp.ID != 7 || resp.Error == nil || resp.Error.Code != "plugin_error" || resp.Error.Message == "" {
		t.Fatalf("decode response = %+v", resp)
	}
}

type scriptedProtocol struct {
	buildErr    error
	execErr     error
	sawDir      string
	sawExternal bool
	sawDeadline bool
}

func (scriptedProtocol) Type() string { return "scripted" }

func (s *scriptedProtocol) BuildRequest(context.Context, map[string]any) (*spi.Request, error) {
	if s.buildErr != nil {
		return nil, s.buildErr
	}
	return &spi.Request{Method: "m", URL: "u", Headers: http.Header{}}, nil
}

func (s *scriptedProtocol) Execute(ctx context.Context, pctx *spi.ProtocolContext, _ *spi.Request) (*spi.Response, error) {
	s.sawDir = pctx.Dir
	s.sawExternal = pctx.AllowExternalFiles
	_, s.sawDeadline = ctx.Deadline()
	if s.execErr != nil {
		return nil, s.execErr
	}
	return &spi.Response{StatusCode: 7, Headers: http.Header{}, Body: []byte("{}")}, nil
}

func TestProtocolExecuteContextAndVerr(t *testing.T) {
	p := &scriptedProtocol{}
	deadline := time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	resp := handleProtocol(p, plugin.Request{
		Method: plugin.MethodExecute,
		Params: map[string]any{
			"request":            map[string]any{},
			"dir":                "/proj",
			"allowExternalFiles": true,
			"deadline":           deadline,
		},
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if !p.sawDeadline || p.sawDir != "/proj" || !p.sawExternal {
		t.Fatalf("context: dir %q external %v deadline %v", p.sawDir, p.sawExternal, p.sawDeadline)
	}

	p.buildErr = verr.New(verr.CodeConfig, "bad request").WithDetail("field", "target")
	built := handleProtocol(p, plugin.Request{Method: plugin.MethodBuildRequest, Params: map[string]any{"request": map[string]any{}}})
	if built.Error == nil || built.Error.Code != "config_error" || built.Error.Message != "bad request" {
		t.Fatalf("build: %+v", built.Error)
	}
	if built.Error.Details["field"] != "target" {
		t.Fatalf("details: %+v", built.Error.Details)
	}

	p2 := &scriptedProtocol{execErr: errors.New("boom")}
	plain := handleProtocol(p2, plugin.Request{Method: plugin.MethodExecute, Params: map[string]any{}})
	if plain.Error == nil || plain.Error.Code != "transport_error" || plain.Error.Message != "boom" {
		t.Fatalf("plain: %+v", plain.Error)
	}
}
