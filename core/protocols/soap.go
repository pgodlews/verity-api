package protocols

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/pgodlews/verity-api/core/internal/scalar"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
)

// SOAPProtocol is the SOAP protocol plugin. It posts an XML envelope and sets
// SOAP-specific headers. Request config fields:
//
//	url        string  endpoint URL (required)
//	body       string  full SOAP envelope (used verbatim if provided)
//	envelope   string  alias for body
//	soapAction string  SOAPAction header (SOAP 1.1)
//	version    string  "1.1" (default) or "1.2"
//	headers    map     extra headers
//
// When no explicit envelope is provided but "operation"/"parameters" are set,
// a minimal envelope is generated (see buildEnvelope) — convenient for
// WSDL-imported templates.
type SOAPProtocol struct{}

func (SOAPProtocol) Type() string { return "soap" }

func (SOAPProtocol) BuildRequest(_ context.Context, raw map[string]any) (*spi.Request, error) {
	req := &spi.Request{
		Method:  http.MethodPost,
		URL:     stringField(raw, "url"),
		Headers: headersFromConfig(raw),
		Query:   map[string]string{},
		Cookies: map[string]string{},
		Config:  raw,
	}

	envelope := stringField(raw, "body")
	if envelope == "" {
		envelope = stringField(raw, "envelope")
	}
	if envelope == "" {
		built, err := buildEnvelope(raw)
		if err != nil {
			return nil, err
		}
		envelope = built
	}
	req.Body = []byte(envelope)

	version := stringField(raw, "version")
	if version == "1.2" {
		// SOAP 1.2 uses application/soap+xml and an action parameter.
		ct := "application/soap+xml; charset=utf-8"
		if action := stringField(raw, "soapAction"); action != "" {
			ct += fmt.Sprintf("; action=%q", action)
		}
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", ct)
		}
	} else {
		if req.Headers.Get("Content-Type") == "" {
			req.Headers.Set("Content-Type", "text/xml; charset=utf-8")
		}
		if action := stringField(raw, "soapAction"); action != "" && req.Headers.Get("SOAPAction") == "" {
			req.Headers.Set("SOAPAction", fmt.Sprintf("%q", action))
		}
	}
	return req, nil
}

func (SOAPProtocol) Execute(ctx context.Context, pctx *spi.ProtocolContext, req *spi.Request) (*spi.Response, error) {
	return doHTTP(ctx, pctx, req)
}

// xmlNamePattern is a practical (non-exhaustive) subset of the XML Name
// production used to validate operation and parameter names before they are
// written as element tags: it requires an ASCII-letter/underscore start and
// letter/digit/underscore/hyphen/period thereafter. This rejects names that
// would produce malformed XML (spaces, angle brackets, leading digits, etc.)
// with a clear error instead of emitting broken markup.
var xmlNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// validXMLName reports whether name is safe to use as an XML element name.
func validXMLName(name string) bool {
	return xmlNamePattern.MatchString(name)
}

// escapeXMLText returns s with XML special characters escaped, safe for use
// both as element text content and (double-quoted) attribute values.
func escapeXMLText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// buildEnvelope generates a minimal SOAP 1.1 envelope from an operation name,
// target namespace, and a map of parameters. This supports WSDL-imported
// request templates without requiring the user to hand-write XML. Parameter
// values and the namespace are XML-escaped; operation/parameter names are
// validated as XML names so untrusted or feed-supplied data cannot produce
// malformed or injected markup.
func buildEnvelope(raw map[string]any) (string, error) {
	op := stringField(raw, "operation")
	if op == "" {
		// Empty envelope as a safe default.
		return `<?xml version="1.0" encoding="UTF-8"?>` +
			`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body/></soap:Envelope>`, nil
	}
	if !validXMLName(op) {
		return "", fmt.Errorf("soap: %q is not a valid XML element name for 'operation'", op)
	}
	ns := stringField(raw, "namespace")

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">`)
	b.WriteString(`<soap:Body>`)
	if ns != "" {
		fmt.Fprintf(&b, `<%s xmlns="%s">`, op, escapeXMLText(ns))
	} else {
		fmt.Fprintf(&b, `<%s>`, op)
	}
	if params, ok := raw["parameters"].(map[string]any); ok {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !validXMLName(k) {
				return "", fmt.Errorf("soap: %q is not a valid XML element name for a parameter", k)
			}
			fmt.Fprintf(&b, `<%s>%s</%s>`, k, escapeXMLText(scalar.String(params[k])), k)
		}
	}
	fmt.Fprintf(&b, `</%s>`, op)
	b.WriteString(`</soap:Body></soap:Envelope>`)
	return b.String(), nil
}

// RegisterSOAP registers the SOAP protocol.
func RegisterSOAP(r *registry.Registry) {
	r.RegisterProtocol(SOAPProtocol{})
}
