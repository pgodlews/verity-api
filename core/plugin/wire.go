package plugin

import (
	"encoding/base64"
	"net/http"
	"time"

	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// Wire encoding shared by the host proxies and the plugin SDK: requests and
// responses cross the process boundary as JSON objects.

// EncodeHeaders encodes headers for the wire: one value as a string,
// several as a list.
func EncodeHeaders(h http.Header) map[string]any {
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

// DecodeHeaders adds wire-encoded headers (any of the shapes EncodeHeaders
// or a plugin may produce) to h.
func DecodeHeaders(v any, h http.Header) {
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

// EncodeRequest is the wire form of a request; the body is base64.
func EncodeRequest(req *spi.Request) map[string]any {
	return map[string]any{
		"method":  req.Method,
		"url":     req.URL,
		"headers": EncodeHeaders(req.Headers),
		"query":   req.Query,
		"cookies": req.Cookies,
		"body":    base64.StdEncoding.EncodeToString(req.Body),
		"config":  req.Config,
	}
}

// DecodeRequest reads EncodeRequest's form. A value that is not an object
// is an error.
func DecodeRequest(v any) (*spi.Request, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, verr.New(verr.CodePlugin, "plugin returned no request")
	}
	req := &spi.Request{
		Method:  asString(m["method"]),
		URL:     asString(m["url"]),
		Headers: http.Header{},
		Query:   map[string]string{},
		Cookies: map[string]string{},
	}
	DecodeHeaders(m["headers"], req.Headers)
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

// EncodeResponse is the wire form of a response; the body is base64.
func EncodeResponse(resp *spi.Response) map[string]any {
	return map[string]any{
		"statusCode":  resp.StatusCode,
		"status":      resp.Status,
		"headers":     EncodeHeaders(resp.Headers),
		"body":        base64.StdEncoding.EncodeToString(resp.Body),
		"durationMs":  resp.Duration.Milliseconds(),
		"contentType": resp.ContentType,
	}
}

// DecodeResponse reads EncodeResponse's form. A value that is not an
// object is an error.
func DecodeResponse(v any) (*spi.Response, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, verr.New(verr.CodePlugin, "plugin returned no response")
	}
	resp := &spi.Response{
		StatusCode:  int(asFloat(m["statusCode"])),
		Status:      asString(m["status"]),
		Headers:     http.Header{},
		ContentType: asString(m["contentType"]),
		Meta:        map[string]any{},
	}
	DecodeHeaders(m["headers"], resp.Headers)
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
