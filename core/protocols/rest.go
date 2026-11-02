package protocols

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
)

// REST is the REST/HTTP protocol plugin. Request config fields:
//
//	method   string            HTTP method (default GET)
//	url      string            target URL (required)
//	headers  map[string]string request headers
//	query    map[string]string query parameters
//	body     string            raw request body
//	json     any               body serialized as JSON (sets Content-Type)
//	form     map[string]string url-encoded form body
//	multipart []any            ordered text or file parts, read during Execute
//	bodyFile string            raw body read from a file during Execute
type RESTProtocol struct{}

func (RESTProtocol) Type() string { return "rest" }

func (RESTProtocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	if err := validateUpload(raw); err != nil {
		return nil, err
	}
	req := &spi.Request{
		Method:  stringField(raw, "method"),
		URL:     stringField(raw, "url"),
		Headers: headersFromConfig(raw),
		Query:   map[string]string{},
		Cookies: map[string]string{},
		Config:  raw,
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	if q, ok := raw["query"].(map[string]any); ok {
		for k, v := range q {
			req.Query[k] = toStr(v)
		}
	}

	switch {
	case raw["multipart"] != nil || raw["bodyFile"] != nil:
		// Files are read in Execute so BuildRequest stays free of I/O and a
		// missing file is reported at send time.
	case raw["json"] != nil:
		b, err := json.Marshal(raw["json"])
		if err != nil {
			return nil, err
		}
		req.Body = b
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", "application/json")
		}
	case raw["form"] != nil:
		if form, ok := raw["form"].(map[string]any); ok {
			vals := url.Values{}
			for k, v := range form {
				vals.Set(k, toStr(v))
			}
			req.Body = []byte(vals.Encode())
			if req.Headers.Get("Content-Type") == "" {
				req.Headers.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		}
	case raw["body"] != nil:
		req.Body = []byte(stringField(raw, "body"))
	}
	return req, nil
}

func (RESTProtocol) Execute(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	if err := prepareUpload(pctx.Dir, pctx.AllowExternalFiles, req); err != nil {
		return nil, err
	}
	return doHTTP(ctx, pctx, req)
}

// RegisterREST registers the REST protocol.
func RegisterREST(r *registry.Registry) {
	r.RegisterProtocol(RESTProtocol{})
}

func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmtAny(v)
}
