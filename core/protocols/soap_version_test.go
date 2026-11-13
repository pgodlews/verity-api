package protocols

import (
	"context"
	"strings"
	"testing"

	"github.com/pgodlews/verity-api/core/verr"
)

func TestSOAPVersionEnvelopeAndAction(t *testing.T) {
	build := func(raw map[string]any) (string, string, string, error) {
		raw["url"] = "http://127.0.0.1/soap"
		req, err := SOAPProtocol{}.BuildRequest(context.Background(), raw)
		if err != nil {
			return "", "", "", err
		}
		return string(req.Body), req.Headers.Get("Content-Type"), req.Headers.Get("SOAPAction"), nil
	}

	body, ct, _, err := build(map[string]any{"operation": "Op", "version": "1.2", "soapAction": "urn:op"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, soap12Namespace) || strings.Contains(body, soap11Namespace) {
		t.Fatalf("1.2 envelope uses wrong namespace: %s", body)
	}
	if !strings.HasPrefix(ct, "application/soap+xml") {
		t.Fatalf("1.2 content type %q", ct)
	}

	body, _, action, err := build(map[string]any{"operation": "Op"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, soap11Namespace) || action != `""` {
		t.Fatalf("1.1 without soapAction: envelope %s, SOAPAction %q (want \"\")", body, action)
	}

	_, _, _, err = build(map[string]any{"operation": "Op", "version": "2.0"})
	if got, ok := verr.As(err); !ok || got.Code != verr.CodeConfig {
		t.Fatalf("version 2.0 error = %v, want %s", err, verr.CodeConfig)
	}
}
