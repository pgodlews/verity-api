package protocols

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
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
	if err := validateRESTConfig(raw); err != nil {
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
		form := raw["form"].(map[string]any) // type-checked by validateRESTConfig
		vals := url.Values{}
		for k, v := range form {
			vals.Set(k, toStr(v))
		}
		req.Body = []byte(vals.Encode())
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", "application/x-www-form-urlencoded")
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

// validateRESTConfig rejects request shapes that would otherwise be sent
// differently from what the author wrote: more than one body field (json
// silently won over form and body), a form/query/headers value that is not a
// map (silently sent nothing), and an empty url.
func validateRESTConfig(raw map[string]any) error {
	var set []string
	for _, key := range []string{"body", "json", "form", "multipart", "bodyFile"} {
		if raw[key] != nil {
			set = append(set, key)
		}
	}
	if len(set) > 1 {
		return verr.Newf(verr.CodeConfig, "use only one of body, json, form, multipart, or bodyFile (got %s)", strings.Join(set, ", "))
	}
	if err := requireMaps(raw, "form", "query", "headers"); err != nil {
		return err
	}
	if strings.TrimSpace(stringField(raw, "url")) == "" {
		return verr.New(verr.CodeConfig, "rest request requires 'url'")
	}
	return nil
}

// requireMaps reports a config_error when one of keys is set to something
// other than a map.
func requireMaps(raw map[string]any, keys ...string) error {
	for _, key := range keys {
		if v := raw[key]; v != nil {
			if _, ok := v.(map[string]any); !ok {
				return verr.Newf(verr.CodeConfig, "'%s' must be a map of names to values", key)
			}
		}
	}
	return nil
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
