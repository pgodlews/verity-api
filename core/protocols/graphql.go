package protocols

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// GraphQLProtocol is the GraphQL protocol plugin. It posts a standard GraphQL
// JSON request. Config fields:
//
//	url           string            endpoint URL (required)
//	query         string            GraphQL query or mutation (required)
//	variables     map[string]any    query variables
//	operationName string            operation name (optional)
//	headers       map               extra headers
type GraphQLProtocol struct{}

func (GraphQLProtocol) Type() string { return "graphql" }

func (GraphQLProtocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	if strings.TrimSpace(stringField(raw, "url")) == "" {
		return nil, verr.New(verr.CodeConfig, "graphql request requires 'url'")
	}
	if strings.TrimSpace(stringField(raw, "query")) == "" {
		return nil, verr.New(verr.CodeConfig, "graphql request requires a non-empty 'query'")
	}
	if err := requireMaps(raw, "headers"); err != nil {
		return nil, err
	}
	req := &spi.Request{
		Method:  http.MethodPost,
		URL:     stringField(raw, "url"),
		Headers: headersFromConfig(raw),
		Query:   map[string]string{},
		Cookies: map[string]string{},
		Config:  raw,
	}
	payload := map[string]any{
		"query": stringField(raw, "query"),
	}
	if vars, ok := raw["variables"]; ok {
		payload["variables"] = vars
	}
	if op := stringField(raw, "operationName"); op != "" {
		payload["operationName"] = op
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req.Body = b
	if req.Headers.Get("Content-Type") == "" {
		req.Headers.Set("Content-Type", "application/json")
	}
	if req.Headers.Get("Accept") == "" {
		req.Headers.Set("Accept", "application/json")
	}
	return req, nil
}

func (GraphQLProtocol) Execute(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	return doHTTP(ctx, pctx, req)
}

// RegisterGraphQL registers the GraphQL protocol.
func RegisterGraphQL(r *registry.Registry) {
	r.RegisterProtocol(GraphQLProtocol{})
}
