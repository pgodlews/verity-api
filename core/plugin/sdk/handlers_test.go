package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/spi"
)

type testAssertion struct {
	err error
}

func (a testAssertion) Type() string { return "test" }
func (a testAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	if a.err != nil {
		return nil, a.err
	}
	return &spi.AssertionResult{
		Passed:      string(actx.Response.Body) == "body" && spec.Expr == "expr" && spec.Op == "eq" && spec.Value == "value",
		Type:        a.Type(),
		Description: spec.Description,
	}, nil
}

type testDataSource struct {
	err    error
	dir    string
	path   string
	config map[string]any
	inline []map[string]string
}

func (d *testDataSource) Type() string { return "test" }
func (d *testDataSource) Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error) {
	d.dir, d.path, d.config, d.inline = dir, path, config, inline
	if d.err != nil {
		return nil, d.err
	}
	return []map[string]string{{"id": "1"}}, nil
}

type testReporter struct {
	err    error
	result *spi.RunResult
	out    string
}

func (r *testReporter) Type() string { return "test" }
func (r *testReporter) Report(result *spi.RunResult, out string) error {
	r.result, r.out = result, out
	return r.err
}

type testAuth struct {
	err error
}

func (a testAuth) Type() string         { return "test" }
func (a testAuth) Layer() spi.AuthLayer { return spi.LayerMessage }
func (a testAuth) Apply(_ context.Context, req *spi.Request) error {
	if a.err != nil {
		return a.err
	}
	req.Headers.Set("X-Test", "applied")
	return nil
}

func TestHandleAssertion(t *testing.T) {
	req := plugin.Request{Params: map[string]any{
		"response": map[string]any{"statusCode": 200, "body": "Ym9keQ=="},
		"spec": map[string]any{
			"description": "description", "expr": "expr", "op": "eq", "value": "value",
			"config": map[string]any{"key": "config"},
		},
	}}
	resp := handleAssertion(testAssertion{}, req)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	result := resp.Result["result"].(map[string]any)
	if result["passed"] != true || result["description"] != "description" {
		t.Fatalf("assertion result = %#v", result)
	}
	resp = handleAssertion(testAssertion{err: errors.New("evaluate failed")}, req)
	if resp.Error == nil || resp.Error.Code != "plugin_error" || resp.Error.Message != "evaluate failed" {
		t.Fatalf("assertion error = %#v", resp.Error)
	}
}

func TestHandleDataSource(t *testing.T) {
	d := &testDataSource{}
	req := plugin.Request{Params: map[string]any{
		"dir": "project", "path": "feed.csv", "config": map[string]any{"key": "value"},
		"inline": []any{map[string]any{"id": "inline"}},
	}}
	resp := handleDataSource(d, req)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if d.dir != "project" || d.path != "feed.csv" || d.config["key"] != "value" || !reflect.DeepEqual(d.inline, []map[string]string{{"id": "inline"}}) {
		t.Fatalf("datasource arguments: dir=%q path=%q config=%#v inline=%#v", d.dir, d.path, d.config, d.inline)
	}
	rows := resp.Result["rows"].([]map[string]string)
	if len(rows) != 1 || rows[0]["id"] != "1" {
		t.Fatalf("rows = %#v", rows)
	}
	resp = handleDataSource(&testDataSource{err: errors.New("read failed")}, req)
	if resp.Error == nil || resp.Error.Code != "plugin_error" || resp.Error.Message != "read failed" {
		t.Fatalf("datasource error = %#v", resp.Error)
	}
}

func TestHandleReporter(t *testing.T) {
	r := &testReporter{}
	req := plugin.Request{Params: map[string]any{
		"result": map[string]any{"summary": map[string]any{"status": "passed"}},
		"out":    "report.json",
	}}
	resp := handleReporter(r, req)
	if resp.Error != nil || resp.Result["ok"] != true {
		t.Fatalf("report response = %#v", resp)
	}
	if r.out != "report.json" || r.result == nil || r.result.Summary.Status != spi.StatusPassed {
		t.Fatalf("report arguments: out=%q result=%#v", r.out, r.result)
	}
	resp = handleReporter(&testReporter{err: errors.New("write failed")}, req)
	if resp.Error == nil || resp.Error.Code != "plugin_error" || resp.Error.Message != "write failed" {
		t.Fatalf("reporter error = %#v", resp.Error)
	}
	resp = handleReporter(&testReporter{}, plugin.Request{Params: map[string]any{"result": func() {}}})
	if resp.Error == nil || resp.Error.Code != "plugin_error" {
		t.Fatalf("malformed result error = %#v", resp.Error)
	}
}

func TestHandleAuth(t *testing.T) {
	factory := func(config map[string]any) (spi.AuthProvider, error) {
		if config["key"] != "value" {
			t.Fatalf("auth config = %#v", config)
		}
		return testAuth{}, nil
	}
	req := plugin.Request{Method: plugin.MethodLayer, Params: map[string]any{"config": map[string]any{"key": "value"}}}
	resp := handleAuth(factory, req)
	if resp.Error != nil || resp.Result["layer"] != int(spi.LayerMessage) {
		t.Fatalf("layer response = %#v", resp)
	}
	req.Method = plugin.MethodApply
	req.Params["request"] = map[string]any{"method": "GET", "url": "/", "headers": map[string]any{}}
	resp = handleAuth(factory, req)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	headers := resp.Result["request"].(map[string]any)["headers"].(map[string]any)
	if headers["X-Test"] != "applied" {
		t.Fatalf("applied headers = %#v", headers)
	}
	req.Method = "other"
	resp = handleAuth(factory, req)
	if resp.Error == nil || resp.Error.Code != "plugin_error" {
		t.Fatalf("unsupported method error = %#v", resp.Error)
	}
	resp = handleAuth(func(map[string]any) (spi.AuthProvider, error) {
		return nil, errors.New("factory failed")
	}, plugin.Request{Params: map[string]any{}})
	if resp.Error == nil || resp.Error.Code != "auth_error" || resp.Error.Message != "factory failed" {
		t.Fatalf("factory error = %#v", resp.Error)
	}
	resp = handleAuth(func(map[string]any) (spi.AuthProvider, error) {
		return testAuth{err: errors.New("apply failed")}, nil
	}, plugin.Request{Method: plugin.MethodApply, Params: map[string]any{"request": map[string]any{}}})
	if resp.Error == nil || resp.Error.Code != "auth_error" || resp.Error.Message != "apply failed" {
		t.Fatalf("apply error = %#v", resp.Error)
	}
}

func TestProtocolBuildSuccessAndUnsupportedMethod(t *testing.T) {
	p := &scriptedProtocol{}
	resp := handleProtocol(p, plugin.Request{Method: plugin.MethodBuildRequest, Params: map[string]any{"request": map[string]any{"url": "/"}}})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	built := resp.Result["request"].(map[string]any)
	if built["method"] != "m" || built["url"] != "u" {
		t.Fatalf("built request = %#v", built)
	}
	resp = handleProtocol(p, plugin.Request{Method: "other", Params: map[string]any{}})
	if resp.Error == nil || resp.Error.Code != "plugin_error" || resp.Error.Message != "unsupported method other" {
		t.Fatalf("unsupported method error = %#v", resp.Error)
	}
}

func TestWireConversionEdgeCases(t *testing.T) {
	req, err := decodeRequest(map[string]any{
		"headers": map[string]any{"Multi": []any{"one", 2}, "Empty": 3},
		"query":   map[string]any{"page": 2},
		"cookies": map[string]any{"session": 3},
		"body":    "not-base64",
		"config":  map[string]any{"key": "value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req.Headers.Values("Multi"), []string{"one", ""}) || req.Headers.Get("Empty") != "" {
		t.Fatalf("decoded headers = %#v", req.Headers)
	}
	if req.Query["page"] != "" || req.Cookies["session"] != "" || len(req.Body) != 0 || req.Config["key"] != "value" {
		t.Fatalf("decoded request = %#v", req)
	}
	for _, tt := range []struct {
		value any
		want  float64
	}{{float64(1), 1}, {float32(2), 2}, {int(3), 3}, {int64(4), 4}, {json.Number("5"), 5}, {"other", 0}} {
		if got := asFloat(tt.value); got != tt.want {
			t.Errorf("asFloat(%#v) = %v, want %v", tt.value, got, tt.want)
		}
	}
	if err := remarshal(map[string]any{"key": "value"}, new(map[string]any)); err != nil {
		t.Fatal(err)
	}
	if err := remarshal(func() {}, new(any)); err == nil {
		t.Fatal("remarshal accepted an unsupported value")
	}
}

func TestServeEntrypointsHandshakeAndShutdown(t *testing.T) {
	tests := []struct {
		kind  string
		typ   string
		serve func()
	}{
		{kind: "protocol", typ: "scripted", serve: func() { ServeProtocol(&scriptedProtocol{}) }},
		{kind: "assertion", typ: "test", serve: func() { ServeAssertion(testAssertion{}) }},
		{kind: "datasource", typ: "test", serve: func() { ServeDataSource(&testDataSource{}) }},
		{kind: "reporter", typ: "test", serve: func() { ServeReporter(&testReporter{}) }},
		{kind: "auth", typ: "test", serve: func() { ServeAuth("test", func(map[string]any) (spi.AuthProvider, error) { return testAuth{}, nil }) }},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			inR, inW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			oldIn, oldOut := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = inR, outW
			_, _ = io.WriteString(inW, "not-json\n"+`{"id":1,"method":"host.shutdown"}`+"\n")
			_ = inW.Close()
			tt.serve()
			os.Stdin, os.Stdout = oldIn, oldOut
			_ = outW.Close()
			var hs plugin.Handshake
			if err := json.NewDecoder(bufio.NewReader(outR)).Decode(&hs); err != nil {
				t.Fatal(err)
			}
			if hs.Magic != plugin.MagicCookie || hs.Version != plugin.ProtocolVersion || hs.Kind != tt.kind || hs.Type != tt.typ {
				t.Fatalf("handshake = %#v", hs)
			}
			_ = inR.Close()
			_ = outR.Close()
		})
	}
}

func TestEncodeHeadersNil(t *testing.T) {
	if got := encodeHeaders(nil); len(got) != 0 {
		t.Fatalf("encodeHeaders(nil) = %#v", got)
	}
	req, err := decodeRequest(nil)
	if err != nil || req.Headers == nil || req.Query == nil || req.Cookies == nil {
		t.Fatalf("decodeRequest(nil) = %#v, %v", req, err)
	}
	resp, err := decodeResponse(nil)
	if err != nil || resp.Headers == nil || resp.Meta == nil {
		t.Fatalf("decodeResponse(nil) = %#v, %v", resp, err)
	}
	if got := encodeHeaders(http.Header{"Empty": nil}); len(got) != 0 {
		t.Fatalf("empty header encoded as %#v", got)
	}
}
