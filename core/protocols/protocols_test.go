package protocols

import (
	"context"
	"strings"
	"testing"
)

func TestRESTBuildRequestJSON(t *testing.T) {
	p := RESTProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"method":  "POST",
		"url":     "http://x/api",
		"json":    map[string]any{"a": 1},
		"headers": map[string]any{"X-Test": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" {
		t.Fatalf("method: %q", req.Method)
	}
	if req.Headers.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type not set: %q", req.Headers.Get("Content-Type"))
	}
	if !strings.Contains(string(req.Body), `"a":1`) {
		t.Fatalf("json body not marshaled: %s", req.Body)
	}
	if req.Headers.Get("X-Test") != "1" {
		t.Fatal("custom header missing")
	}
}

func TestRESTDefaultMethod(t *testing.T) {
	req, _ := RESTProtocol{}.BuildRequest(context.Background(), map[string]any{"url": "http://x"})
	if req.Method != "GET" {
		t.Fatalf("expected default GET, got %q", req.Method)
	}
}

func TestRESTFormBodyURLEncoded(t *testing.T) {
	p := RESTProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"method": "POST",
		"url":    "http://x",
		"form":   map[string]any{"q": "a&b=c d"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(req.Body), "q=a%26b%3Dc+d"; got != want {
		t.Fatalf("form body = %q, want %q", got, want)
	}
	if req.Headers.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type not set: %q", req.Headers.Get("Content-Type"))
	}
}

func TestRESTFormBodyDeterministicOrder(t *testing.T) {
	p := RESTProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"method": "POST",
		"url":    "http://x",
		"form":   map[string]any{"z": "1", "a": "2", "m": "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(req.Body), "a=2&m=3&z=1"; got != want {
		t.Fatalf("form body = %q, want %q (sorted key order)", got, want)
	}
}

func TestSOAPEnvelopeGeneration(t *testing.T) {
	p := SOAPProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"url":        "http://x/soap",
		"operation":  "Add",
		"namespace":  "http://example.com/calc",
		"soapAction": "http://example.com/calc/Add",
		"parameters": map[string]any{"a": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(req.Body)
	if !strings.Contains(body, "<Add") || !strings.Contains(body, "soap:Envelope") {
		t.Fatalf("envelope not generated: %s", body)
	}
	if req.Headers.Get("SOAPAction") == "" {
		t.Fatal("SOAPAction header not set for SOAP 1.1")
	}
	if !strings.Contains(req.Headers.Get("Content-Type"), "text/xml") {
		t.Fatalf("unexpected content-type: %q", req.Headers.Get("Content-Type"))
	}
}

func TestSOAPEnvelopeEscapesParamValues(t *testing.T) {
	p := SOAPProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"url":       "http://x/soap",
		"operation": "Add",
		"parameters": map[string]any{
			"note": "a<b&c",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(req.Body)
	if !strings.Contains(body, "a&lt;b&amp;c") {
		t.Fatalf("param value not escaped: %s", body)
	}
	if strings.Contains(body, "a<b&c") {
		t.Fatalf("unescaped param value leaked into envelope: %s", body)
	}
}

func TestSOAPEnvelopeInvalidOperationName(t *testing.T) {
	p := SOAPProtocol{}
	_, err := p.BuildRequest(context.Background(), map[string]any{
		"url":       "http://x/soap",
		"operation": "1bad name",
	})
	if err == nil {
		t.Fatal("expected error for invalid operation element name")
	}
}

func TestSOAPEnvelopeInvalidParamName(t *testing.T) {
	p := SOAPProtocol{}
	_, err := p.BuildRequest(context.Background(), map[string]any{
		"url":        "http://x/soap",
		"operation":  "Add",
		"parameters": map[string]any{"bad key": "1"},
	})
	if err == nil {
		t.Fatal("expected error for invalid parameter element name")
	}
}

func TestSOAPEnvelopeDeterministicParamOrder(t *testing.T) {
	p := SOAPProtocol{}
	req, err := p.BuildRequest(context.Background(), map[string]any{
		"url":        "http://x/soap",
		"operation":  "Add",
		"parameters": map[string]any{"z": "1", "a": "2", "m": "3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(req.Body)
	ai, mi, zi := strings.Index(body, "<a>"), strings.Index(body, "<m>"), strings.Index(body, "<z>")
	if !(ai < mi && mi < zi) {
		t.Fatalf("expected sorted parameter order a,m,z in: %s", body)
	}
}

func TestSOAPExplicitEnvelope(t *testing.T) {
	custom := `<?xml version="1.0"?><soap:Envelope/>`
	req, _ := SOAPProtocol{}.BuildRequest(context.Background(), map[string]any{
		"url": "http://x", "body": custom,
	})
	if string(req.Body) != custom {
		t.Fatalf("explicit envelope not used verbatim: %s", req.Body)
	}
}

func TestGraphQLBuildRequest(t *testing.T) {
	req, err := GraphQLProtocol{}.BuildRequest(context.Background(), map[string]any{
		"url":       "http://x/graphql",
		"query":     "{ me { id } }",
		"variables": map[string]any{"v": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(req.Body)
	if !strings.Contains(body, `"query"`) || !strings.Contains(body, `"variables"`) {
		t.Fatalf("graphql payload malformed: %s", body)
	}
}

func TestGRPCStubReturnsStructuredError(t *testing.T) {
	_, err := GRPCProtocol{}.Execute(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected stub error from gRPC protocol")
	}
	if !strings.Contains(err.Error(), "plugins/grpc") {
		t.Fatalf("unexpected error: %v", err)
	}
}
