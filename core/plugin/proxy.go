package plugin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// executeGrace is extra time the host waits for a protocol plugin to return
// after the attempt deadline, so the plugin can map a deadline to a response
// instead of being killed mid-call.
const executeGrace = 5 * time.Second

// The proxy types below implement the spi interfaces by forwarding to a plugin
// subprocess. From the runner's perspective they are indistinguishable from
// in-process built-ins.

// protocolProxy adapts a plugin Client to spi.Protocol.
type protocolProxy struct {
	client *Client
	typ    string
}

func (p *protocolProxy) Type() string { return p.typ }

// DialHidden marks a protocol whose sockets are opened in another process.
// The load command cannot apply its dial guard there, so it screens the
// step's address fields instead.
func (*protocolProxy) DialHidden() {}

// HidesDial reports whether p opens connections outside the host process.
// A test double implements DialHidden to exercise that screen.
func HidesDial(p spi.Protocol) bool {
	_, ok := p.(interface{ DialHidden() })
	return ok
}

func (p *protocolProxy) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	res, err := p.client.call(MethodBuildRequest, map[string]any{"request": raw})
	if err != nil {
		return nil, err
	}
	return DecodeRequest(res["request"])
}

func (p *protocolProxy) Execute(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	execCtx := ctx
	rpcTimeout := p.client.Timeout
	if rpcTimeout <= 0 {
		rpcTimeout = 30 * time.Second
	}
	// timeoutMs is the HTTP client's Timeout. Protocol plugins do not use that
	// client; pass the same budget as a deadline on the wire.
	if pctx != nil && pctx.Client != nil && pctx.Client.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, pctx.Client.Timeout)
		defer cancel()
		if pctx.Client.Timeout+executeGrace > rpcTimeout {
			rpcTimeout = pctx.Client.Timeout + executeGrace
		}
	}
	if dl, ok := execCtx.Deadline(); ok {
		remain := time.Until(dl)
		if remain < 0 {
			remain = 0
		}
		if remain+executeGrace > rpcTimeout {
			rpcTimeout = remain + executeGrace
		}
	}
	params := map[string]any{"request": EncodeRequest(req)}
	if pctx != nil {
		if pctx.Dir != "" {
			params["dir"] = pctx.Dir
		}
		if pctx.AllowExternalFiles {
			params["allowExternalFiles"] = true
		}
	}
	if dl, ok := execCtx.Deadline(); ok {
		params["deadline"] = dl.Format(time.RFC3339Nano)
	}
	// Pass the caller's context, not the per-attempt timeout child. The
	// timeout child exists so a deadline can still come back as a response
	// during rpcTimeout's grace period. A cancelled run (Ctrl-C) is ctx.
	res, err := p.client.callTimeout(ctx, MethodExecute, params, rpcTimeout)
	if err != nil {
		return nil, err
	}
	return DecodeResponse(res["response"])
}

// assertionProxy adapts a plugin Client to spi.Assertion.
type assertionProxy struct {
	client *Client
	typ    string
}

func (a *assertionProxy) Type() string { return a.typ }

func (a *assertionProxy) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	res, err := a.client.call(MethodEvaluate, map[string]any{
		"response": EncodeResponse(actx.Response),
		"spec": map[string]any{
			"description": spec.Description,
			"expr":        spec.Expr,
			"op":          spec.Op,
			"value":       spec.Value,
			"config":      spec.Config,
		},
	})
	if err != nil {
		return nil, err
	}
	var out spi.AssertionResult
	if err := remarshal(res["result"], &out); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "decode assertion result")
	}
	return &out, nil
}

// dataSourceProxy adapts a plugin Client to spi.DataSource.
type dataSourceProxy struct {
	client *Client
	typ    string
}

func (d *dataSourceProxy) Type() string { return d.typ }

func (d *dataSourceProxy) Rows(dir string, config map[string]any, inline []map[string]string, path string) ([]map[string]string, error) {
	res, err := d.client.call(MethodRows, map[string]any{
		"dir": dir, "config": config, "inline": inline, "path": path,
	})
	if err != nil {
		return nil, err
	}
	var rows []map[string]string
	if err := remarshal(res["rows"], &rows); err != nil {
		return nil, verr.Wrap(verr.CodePlugin, err, "decode rows")
	}
	return rows, nil
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
	return json.Unmarshal(data, target)
}
