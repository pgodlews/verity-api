package plugin

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// reporterProxy adapts a plugin Client to spi.Reporter. The host serializes the
// RunResult to JSON and the plugin writes its artifact; the host passes the
// destination path so file-writing reporters work unchanged.
type reporterProxy struct {
	client *Client
	typ    string
}

func (r *reporterProxy) Type() string { return r.typ }

func (r *reporterProxy) Report(result *spi.RunResult, out string) error {
	data, err := json.Marshal(result)
	if err != nil {
		return verr.Wrap(verr.CodePlugin, err, "marshal result for reporter plugin")
	}
	// RawMessage is written as is when the request is encoded, so the
	// result is marshaled once instead of marshal, unmarshal, marshal.
	_, err = r.client.call(MethodReport, map[string]any{
		"result": json.RawMessage(data),
		"out":    out,
	})
	return err
}

// authProxy adapts a plugin Client to spi.AuthProvider. It caches the provider
// layer (queried once) and forwards Apply with the resolved request. The
// redactor registers any secret material the host knows about; plugins are
// expected to redact their own diagnostics too.
type authProxy struct {
	client *Client
	typ    string
	config map[string]any
	red    *secrets.Redactor
	mu     sync.Mutex
	layer  *spi.AuthLayer
}

func (a *authProxy) Type() string { return a.typ }

func (a *authProxy) Layer() spi.AuthLayer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.layer != nil {
		return *a.layer
	}
	res, err := a.client.call(MethodLayer, map[string]any{"config": a.config})
	layer := spi.LayerMessage
	if err == nil {
		if int(asFloat(res["layer"])) == int(spi.LayerTransport) {
			layer = spi.LayerTransport
		}
	}
	a.layer = &layer
	return layer
}

func (a *authProxy) Apply(_ context.Context, req *spi.Request) error {
	res, err := a.client.call(MethodApply, map[string]any{
		"config":  a.config,
		"request": encodeRequest(req),
	})
	if err != nil {
		return err
	}
	// The plugin returns the mutated request; merge headers/query/cookies back.
	mutated, derr := decodeRequest(res["request"])
	if derr != nil {
		return derr
	}
	// Every value the plugin added or changed is credential material as far
	// as the host knows, so register it before it can reach a capture,
	// report, or error.
	for k, vs := range mutated.Headers {
		old := map[string]bool{}
		for _, v := range req.Headers.Values(k) {
			old[v] = true
		}
		req.Headers.Del(k)
		for _, v := range vs {
			if !old[v] {
				a.addSecret(v)
			}
			req.Headers.Add(k, v)
		}
	}
	for k, v := range mutated.Query {
		if req.Query == nil {
			req.Query = map[string]string{}
		}
		if prev, ok := req.Query[k]; !ok || prev != v {
			a.addSecret(v)
		}
		req.Query[k] = v
	}
	for k, v := range mutated.Cookies {
		if req.Cookies == nil {
			req.Cookies = map[string]string{}
		}
		if prev, ok := req.Cookies[k]; !ok || prev != v {
			a.addSecret(v)
		}
		req.Cookies[k] = v
	}
	return nil
}

func (a *authProxy) addSecret(v string) {
	if a.red != nil {
		a.red.Add(v)
	}
}
