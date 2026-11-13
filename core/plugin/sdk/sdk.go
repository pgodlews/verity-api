// Package sdk is the plugin author's toolkit: it implements the plugin (server)
// side of the Verity Plugin Protocol v1 so a plugin binary can be written in a
// few lines. Plugin authors implement one of the spi interfaces and call the
// matching Serve* function from main; the SDK handles the handshake and the
// stdin/stdout RPC loop.
//
// This package has no dependencies beyond the core spi types, so plugin binaries
// stay small and cross-compile cleanly.
package sdk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pgodlews/verity-api/core/plugin"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// ServeProtocol runs a Protocol plugin: handshake + RPC loop over stdio.
func ServeProtocol(p spi.Protocol) {
	serve(plugin.Handshake{
		Magic: plugin.MagicCookie, Version: plugin.ProtocolVersion,
		Kind: "protocol", Type: p.Type(),
	}, func(req plugin.Request) plugin.Response {
		return handleProtocol(p, req)
	})
}

// ServeAssertion runs an Assertion plugin.
func ServeAssertion(a spi.Assertion) {
	serve(plugin.Handshake{
		Magic: plugin.MagicCookie, Version: plugin.ProtocolVersion,
		Kind: "assertion", Type: a.Type(),
	}, func(req plugin.Request) plugin.Response {
		return handleAssertion(a, req)
	})
}

// ServeDataSource runs a DataSource plugin.
func ServeDataSource(d spi.DataSource) {
	serve(plugin.Handshake{
		Magic: plugin.MagicCookie, Version: plugin.ProtocolVersion,
		Kind: "datasource", Type: d.Type(),
	}, func(req plugin.Request) plugin.Response {
		return handleDataSource(d, req)
	})
}

// ServeReporter runs a Reporter plugin.
func ServeReporter(r spi.Reporter) {
	serve(plugin.Handshake{
		Magic: plugin.MagicCookie, Version: plugin.ProtocolVersion,
		Kind: "reporter", Type: r.Type(),
	}, func(req plugin.Request) plugin.Response {
		return handleReporter(r, req)
	})
}

// AuthFactoryFunc builds an AuthProvider from resolved config. Auth plugins
// supply one of these to ServeAuth.
type AuthFactoryFunc func(config map[string]any) (spi.AuthProvider, error)

// ServeAuth runs an AuthProvider plugin. typ is the registered type id.
func ServeAuth(typ string, factory AuthFactoryFunc) {
	cache := newAuthCache(factory)
	serve(plugin.Handshake{
		Magic: plugin.MagicCookie, Version: plugin.ProtocolVersion,
		Kind: "auth", Type: typ,
	}, func(req plugin.Request) plugin.Response {
		return handleAuth(cache.provider, req)
	})
}

// authCache keeps one provider per distinct config for the plugin process,
// so providers that fetch and cache tokens (OAuth-like flows) are not
// rebuilt, and do not fetch again, on every request.
type authCache struct {
	factory AuthFactoryFunc
	mu      sync.Mutex
	items   map[[32]byte]spi.AuthProvider
}

func newAuthCache(factory AuthFactoryFunc) *authCache {
	return &authCache{factory: factory, items: map[[32]byte]spi.AuthProvider{}}
}

func (c *authCache) provider(cfg map[string]any) (spi.AuthProvider, error) {
	data, err := json.Marshal(cfg) // map keys are sorted, so equal configs hash equally
	if err != nil {
		return c.factory(cfg)
	}
	key := sha256.Sum256(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.items[key]; ok {
		return p, nil
	}
	p, err := c.factory(cfg)
	if err != nil {
		return nil, err
	}
	c.items[key] = p
	return p, nil
}

// serve writes the handshake then runs the request/response loop until stdin
// closes or a shutdown is requested.
func serve(hs plugin.Handshake, handler func(plugin.Request) plugin.Response) {
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(hs); err != nil {
		fmt.Fprintln(os.Stderr, "handshake failed:", err)
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		req, err := decodePluginRequest(scanner.Bytes())
		if err != nil {
			resp := pluginDecodeError(scanner.Bytes(), err)
			_ = enc.Encode(resp)
			continue
		}
		if req.Method == plugin.MethodShutdown {
			return
		}
		resp := handler(req)
		resp.ID = req.ID
		_ = enc.Encode(resp)
	}
}

func decodePluginRequest(data []byte) (plugin.Request, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var req plugin.Request
	if err := dec.Decode(&req); err != nil {
		return plugin.Request{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return plugin.Request{}, fmt.Errorf("multiple JSON values")
		}
		return plugin.Request{}, err
	}
	return req, nil
}

func pluginDecodeError(data []byte, err error) plugin.Response {
	var envelope struct {
		ID int `json:"id"`
	}
	_ = json.Unmarshal(data, &envelope)
	resp := errResp("plugin_error", "decode plugin request: "+err.Error())
	resp.ID = envelope.ID
	return resp
}

func errResp(code, msg string) plugin.Response {
	return plugin.Response{Error: &plugin.WireError{Code: code, Message: msg}}
}

// wireError preserves verr codes across VPP/1. Plain errors keep fallback
// (plugin_error while building a request, transport_error while executing).
func wireError(err error, fallback string) plugin.Response {
	if ve, ok := verr.As(err); ok {
		msg := ve.Message
		if cause := ve.Unwrap(); cause != nil {
			msg += ": " + cause.Error()
		}
		return plugin.Response{Error: &plugin.WireError{
			Code: string(ve.Code), Message: msg, Details: ve.Details,
		}}
	}
	return errResp(fallback, err.Error())
}

// protocolCallContext rebuilds the execute context from optional VPP/1 fields.
// dir is the project directory. deadline is the attempt deadline (timeoutMs).
func protocolCallContext(params map[string]any) (context.Context, context.CancelFunc, *spi.ProtocolContext) {
	if params == nil {
		params = map[string]any{}
	}
	ctx := context.Background()
	cancel := func() {}
	if s := asString(params["deadline"]); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			ctx, cancel = context.WithDeadline(ctx, t)
		}
	}
	return ctx, cancel, &spi.ProtocolContext{
		Client: http.DefaultClient,
		// Built-in code calls Redactor methods unconditionally; give plugin
		// code a working (plugin-local) redactor rather than nil.
		Redactor:           secrets.NewRedactor(),
		Dir:                asString(params["dir"]),
		AllowExternalFiles: asBool(params["allowExternalFiles"]),
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func okResp(result map[string]any) plugin.Response {
	return plugin.Response{Result: result}
}

// --- per-kind handlers ---

func handleProtocol(p spi.Protocol, req plugin.Request) plugin.Response {
	switch req.Method {
	case plugin.MethodBuildRequest:
		raw, _ := req.Params["request"].(map[string]any)
		built, err := p.BuildRequest(context.Background(), raw)
		if err != nil {
			return wireError(err, "plugin_error")
		}
		return okResp(map[string]any{"request": encodeRequest(built)})
	case plugin.MethodExecute:
		r, err := decodeRequest(req.Params["request"])
		if err != nil {
			return wireError(err, "plugin_error")
		}
		ctx, cancel, pctx := protocolCallContext(req.Params)
		defer cancel()
		resp, err := p.Execute(ctx, pctx, r)
		if err != nil {
			return wireError(err, "transport_error")
		}
		return okResp(map[string]any{"response": encodeResponse(resp)})
	default:
		return errResp("plugin_error", "unsupported method "+string(req.Method))
	}
}

func handleAssertion(a spi.Assertion, req plugin.Request) plugin.Response {
	respMap, _ := req.Params["response"].(map[string]any)
	specMap, _ := req.Params["spec"].(map[string]any)
	resp, err := decodeResponse(respMap)
	if err != nil {
		return wireError(err, "plugin_error")
	}
	spec := spi.AssertionSpec{
		Description: asString(specMap["description"]),
		Expr:        asString(specMap["expr"]),
		Op:          asString(specMap["op"]),
		Value:       specMap["value"],
	}
	if cfg, ok := specMap["config"].(map[string]any); ok {
		spec.Config = cfg
	}
	res, err := a.Evaluate(context.Background(), &spi.AssertionContext{Response: resp, Redactor: secrets.NewRedactor()}, spec)
	if err != nil {
		return errResp("plugin_error", err.Error())
	}
	var m map[string]any
	remarshal(res, &m)
	return okResp(map[string]any{"result": m})
}

func handleDataSource(d spi.DataSource, req plugin.Request) plugin.Response {
	dir := asString(req.Params["dir"])
	path := asString(req.Params["path"])
	cfg, _ := req.Params["config"].(map[string]any)
	var inline []map[string]string
	remarshal(req.Params["inline"], &inline)
	rows, err := d.Rows(dir, cfg, inline, path)
	if err != nil {
		return errResp("plugin_error", err.Error())
	}
	return okResp(map[string]any{"rows": rows})
}

func handleReporter(r spi.Reporter, req plugin.Request) plugin.Response {
	var result spi.RunResult
	if err := remarshal(req.Params["result"], &result); err != nil {
		return errResp("plugin_error", err.Error())
	}
	out := asString(req.Params["out"])
	if err := r.Report(&result, out); err != nil {
		return errResp("plugin_error", err.Error())
	}
	return okResp(map[string]any{"ok": true})
}

func handleAuth(factory AuthFactoryFunc, req plugin.Request) plugin.Response {
	cfg, _ := req.Params["config"].(map[string]any)
	prov, err := factory(cfg)
	if err != nil {
		return errResp("auth_error", err.Error())
	}
	switch req.Method {
	case plugin.MethodLayer:
		return okResp(map[string]any{"layer": int(prov.Layer())})
	case plugin.MethodApply:
		r, derr := decodeRequest(req.Params["request"])
		if derr != nil {
			return errResp("plugin_error", derr.Error())
		}
		if err := prov.Apply(context.Background(), r); err != nil {
			return errResp("auth_error", err.Error())
		}
		return okResp(map[string]any{"request": encodeRequest(r)})
	default:
		return errResp("plugin_error", "unsupported method "+string(req.Method))
	}
}

// --- wire helpers (mirror of the host side, kept dependency-free) ---

func encodeHeaders(h http.Header) map[string]any {
	if h == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(h))
	for k, vs := range h {
		if len(vs) == 1 {
			out[k] = vs[0]
		} else if len(vs) > 1 {
			out[k] = vs
		}
	}
	return out
}

func decodeHeaders(v any, h http.Header) {
	switch hs := v.(type) {
	case map[string]string:
		for k, val := range hs {
			h.Add(k, val)
		}
	case map[string][]string:
		for k, vals := range hs {
			for _, s := range vals {
				h.Add(k, s)
			}
		}
	case map[string]any:
		for k, val := range hs {
			switch tv := val.(type) {
			case string:
				h.Add(k, tv)
			case []string:
				for _, s := range tv {
					h.Add(k, s)
				}
			case []any:
				for _, item := range tv {
					h.Add(k, asString(item))
				}
			}
		}
	}
}

func encodeRequest(req *spi.Request) map[string]any {
	return map[string]any{
		"method": req.Method, "url": req.URL, "headers": encodeHeaders(req.Headers),
		"query": req.Query, "cookies": req.Cookies,
		"body":   base64.StdEncoding.EncodeToString(req.Body),
		"config": req.Config,
	}
}

func decodeRequest(v any) (*spi.Request, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return &spi.Request{Headers: http.Header{}, Query: map[string]string{}, Cookies: map[string]string{}}, nil
	}
	req := &spi.Request{
		Method: asString(m["method"]), URL: asString(m["url"]),
		Headers: http.Header{}, Query: map[string]string{}, Cookies: map[string]string{},
	}
	decodeHeaders(m["headers"], req.Headers)
	if q, ok := m["query"].(map[string]string); ok {
		for k, v := range q {
			req.Query[k] = v
		}
	} else if q, ok := m["query"].(map[string]any); ok {
		for k, v := range q {
			req.Query[k] = asString(v)
		}
	}
	if c, ok := m["cookies"].(map[string]string); ok {
		for k, v := range c {
			req.Cookies[k] = v
		}
	} else if c, ok := m["cookies"].(map[string]any); ok {
		for k, v := range c {
			req.Cookies[k] = asString(v)
		}
	}
	if b := asString(m["body"]); b != "" {
		dec, err := base64.StdEncoding.DecodeString(b)
		if err != nil {
			return nil, verr.Wrap(verr.CodePlugin, err, "decode request body (base64)")
		}
		req.Body = dec
	}
	if cfg, ok := m["config"].(map[string]any); ok {
		req.Config = cfg
	}
	return req, nil
}

func encodeResponse(resp *spi.Response) map[string]any {
	return map[string]any{
		"statusCode": resp.StatusCode, "status": resp.Status, "headers": encodeHeaders(resp.Headers),
		"body":        base64.StdEncoding.EncodeToString(resp.Body),
		"durationMs":  resp.Duration.Milliseconds(),
		"contentType": resp.ContentType,
	}
}

func decodeResponse(m map[string]any) (*spi.Response, error) {
	resp := &spi.Response{Headers: http.Header{}, Meta: map[string]any{}}
	if m == nil {
		return resp, nil
	}
	resp.StatusCode = int(asFloat(m["statusCode"]))
	resp.Status = asString(m["status"])
	resp.ContentType = asString(m["contentType"])
	decodeHeaders(m["headers"], resp.Headers)
	if ms := asFloat(m["durationMs"]); ms > 0 {
		resp.Duration = time.Duration(ms) * time.Millisecond
	}
	if b := asString(m["body"]); b != "" {
		dec, err := base64.StdEncoding.DecodeString(b)
		if err != nil {
			return nil, verr.Wrap(verr.CodePlugin, err, "decode response body (base64)")
		}
		resp.Body = dec
	}
	return resp, nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

func remarshal(v any, target any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(target)
}
