// Package protocols provides Verity's built-in Protocol plugins (REST, SOAP,
// GraphQL) plus stubs for gRPC. Each is a reference implementation of the
// spi.Protocol interface and is registered through the registry like any
// third-party protocol.
package protocols

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pgodlews/verity-api/core/internal/scalar"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// doHTTP executes a fully-built spi.Request over the provided client and
// captures the response. It applies Query and Cookies contributed by auth
// providers and records timing. It is shared by all HTTP-based protocols.
func doHTTP(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	target, err := spi.WireURL(req.URL, req.Query)
	if err != nil {
		return nil, fmt.Errorf("parse URL %q: %w", req.URL, err)
	}

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}

	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = strings.NewReader(string(req.Body))
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if req.Headers != nil {
		httpReq.Header = req.Headers.Clone()
		// Host is sent from Request.Host. A signer puts the signed host here.
		if host := httpReq.Header.Get("Host"); host != "" {
			httpReq.Host = host
		}
	}
	for k, v := range req.Cookies {
		httpReq.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	client := pctx.Client
	if client == nil {
		client = http.DefaultClient
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Answer the challenge for the request the server actually saw: after
		// a redirect that is resp.Request (another URI, maybe another method),
		// not the request this function built.
		challenged := httpReq
		if resp.Request != nil {
			challenged = resp.Request
		}
		for _, responder := range pctx.ChallengeResponders {
			replay := challenged.Clone(ctx)
			retry, challengeErr := responder.RespondChallenge(ctx, replay, resp)
			if challengeErr != nil {
				resp.Body.Close()
				return nil, challengeErr
			}
			if !retry {
				continue
			}
			resp.Body.Close()
			if replay.GetBody != nil {
				replay.Body, err = replay.GetBody()
				if err != nil {
					return nil, fmt.Errorf("replay request body: %w", err)
				}
			} else if replay.Body != nil && replay.Body != http.NoBody {
				return nil, verr.New(verr.CodeAuth, "authentication requires a replayable request body")
			}
			req.Headers = replay.Header.Clone()
			resp, err = client.Do(replay)
			if err != nil {
				return nil, fmt.Errorf("request failed: %w", err)
			}
			break
		}
	}
	defer resp.Body.Close()

	dur := time.Since(start)

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	return &spi.Response{
		StatusCode:  resp.StatusCode,
		Status:      resp.Status,
		Headers:     resp.Header,
		Body:        body,
		Duration:    dur,
		ContentType: resp.Header.Get("Content-Type"),
		Meta:        map[string]any{},
	}, nil
}

// headersFromConfig converts a config map under key "headers" into http.Header.
func headersFromConfig(raw map[string]any) http.Header {
	h := http.Header{}
	hv, ok := raw["headers"]
	if !ok {
		return h
	}
	m, ok := hv.(map[string]any)
	if !ok {
		return h
	}
	for k, v := range m {
		if v == nil {
			continue // a null header is omitted, not sent as "<nil>"
		}
		h.Set(k, scalar.String(v))
	}
	return h
}

// stringField returns a config value as text; absent or null is "".
func stringField(raw map[string]any, key string) string {
	return scalar.String(raw[key])
}

// fmtAny renders an arbitrary value to string for header/query coercion.
func fmtAny(v any) string {
	return scalar.String(v)
}
