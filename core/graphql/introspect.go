package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
	"github.com/pgodlews/verity-api/core/verr"
)

// introspectionQuery is the GraphQL introspection query. It avoids newer
// fields (isOneOf, isRepeatable, specifiedByURL) so older servers still answer.
const introspectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: true) {
        name
        description
        args {
          name
          description
          type { ...TypeRef }
          defaultValue
        }
        type { ...TypeRef }
        isDeprecated
        deprecationReason
      }
      inputFields {
        name
        description
        type { ...TypeRef }
        defaultValue
      }
      interfaces { ...TypeRef }
      enumValues(includeDeprecated: true) {
        name
        description
        isDeprecated
        deprecationReason
      }
      possibleTypes { ...TypeRef }
    }
    directives {
      name
      description
      locations
      args {
        name
        description
        type { ...TypeRef }
        defaultValue
      }
    }
  }
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType { kind name }
            }
          }
        }
      }
    }
  }
}`

type introEnvelope struct {
	Data struct {
		Schema *introSchema `json:"__schema"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type introSchema struct {
	QueryType        *introName       `json:"queryType"`
	MutationType     *introName       `json:"mutationType"`
	SubscriptionType *introName       `json:"subscriptionType"`
	Types            []introFullType  `json:"types"`
	Directives       []introDirective `json:"directives"`
}

type introName struct {
	Name string `json:"name"`
}

type introTypeRef struct {
	Kind   string        `json:"kind"`
	Name   *string       `json:"name"`
	OfType *introTypeRef `json:"ofType"`
}

type introFullType struct {
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Description   *string        `json:"description"`
	Fields        []introField   `json:"fields"`
	InputFields   []introValue   `json:"inputFields"`
	Interfaces    []introTypeRef `json:"interfaces"`
	EnumValues    []introEnum    `json:"enumValues"`
	PossibleTypes []introTypeRef `json:"possibleTypes"`
}

type introField struct {
	Name              string       `json:"name"`
	Description       *string      `json:"description"`
	Args              []introValue `json:"args"`
	Type              introTypeRef `json:"type"`
	IsDeprecated      bool         `json:"isDeprecated"`
	DeprecationReason *string      `json:"deprecationReason"`
}

type introValue struct {
	Name         string       `json:"name"`
	Description  *string      `json:"description"`
	Type         introTypeRef `json:"type"`
	DefaultValue *string      `json:"defaultValue"`
}

type introEnum struct {
	Name              string  `json:"name"`
	Description       *string `json:"description"`
	IsDeprecated      bool    `json:"isDeprecated"`
	DeprecationReason *string `json:"deprecationReason"`
}

type introDirective struct {
	Name        string       `json:"name"`
	Description *string      `json:"description"`
	Locations   []string     `json:"locations"`
	Args        []introValue `json:"args"`
}

func withQuery(raw string, query map[string]string) (string, error) {
	if len(query) == 0 {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func introspect(ctx context.Context, client *http.Client, endpoint string, req *spi.Request, sign Signer, responders []spi.ChallengeResponder, red *secrets.Redactor) (*ast.Schema, string, error) {
	if red == nil {
		red = secrets.NewRedactor()
	}
	if client == nil {
		client = &http.Client{}
	}
	payload, err := json.Marshal(map[string]string{"query": introspectionQuery})
	if err != nil {
		return nil, "", verr.New(verr.CodeInternal, "encode introspection query")
	}
	body, status, err := postGraphQL(ctx, client, endpoint, req, sign, payload, responders, red)
	if err != nil {
		return nil, "", err
	}
	if status != http.StatusOK {
		return nil, "", verr.Newf(verr.CodeValidation, "introspection returned HTTP %d", status)
	}
	sdl, schema, err := SDLFromIntrospection(body, red)
	if err != nil {
		return nil, "", err
	}
	return schema, sdl, nil
}

// postGraphQL POSTs body and, on HTTP 401, lets one challenge responder
// replay the request the server saw (after redirects). Errors never include headers.
func postGraphQL(ctx context.Context, client *http.Client, endpoint string, req *spi.Request, sign Signer, payload []byte, responders []spi.ChallengeResponder, red *secrets.Redactor) ([]byte, int, error) {
	resp, err := sendGraphQL(ctx, client, endpoint, req, sign, payload, red)
	if err != nil {
		return nil, 0, netErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && resp.Request != nil {
		challenged := resp.Request
		for _, responder := range responders {
			replay := challenged.Clone(ctx)
			ok, cerr := responder.RespondChallenge(ctx, replay, resp)
			if cerr != nil {
				return nil, 0, verr.New(verr.CodeValidation, red.Redact("introspection authentication failed"))
			}
			if !ok {
				continue
			}
			resp.Body.Close()
			if replay.GetBody != nil {
				replay.Body, err = replay.GetBody()
				if err != nil {
					return nil, 0, verr.New(verr.CodeValidation, "introspection authentication failed")
				}
			} else if replay.Body != nil && replay.Body != http.NoBody {
				return nil, 0, verr.New(verr.CodeAuth, "authentication requires a replayable request body")
			}
			resp, err = client.Do(replay)
			if err != nil {
				return nil, 0, netErr(err)
			}
			defer resp.Body.Close()
			break
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSchemaBytes+1))
	if err != nil {
		return nil, 0, verr.New(verr.CodeValidation, "read introspection response")
	}
	if len(body) > maxSchemaBytes {
		return nil, 0, verr.New(verr.CodeValidation, "introspection response is too large")
	}
	return body, resp.StatusCode, nil
}

func sendGraphQL(ctx context.Context, client *http.Client, endpoint string, req *spi.Request, sign Signer, payload []byte, red *secrets.Redactor) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, verr.New(verr.CodeValidation, "introspection URL is invalid")
	}
	copyHeaders(httpReq, req)
	if sign != nil {
		// The step's signature covers its own operation body. Sign this
		// request (its URL, headers and introspection body) again.
		sreq := &spi.Request{Method: http.MethodPost, URL: endpoint, Headers: httpReq.Header, Body: payload}
		if err := sign(ctx, sreq); err != nil {
			return nil, verr.New(verr.CodeAuth, red.Redact("introspection signing failed: "+err.Error()))
		}
		httpReq.Header = sreq.Headers
		if host := sreq.Headers.Get("Host"); host != "" {
			httpReq.Host = host
		}
	}
	return client.Do(httpReq)
}

func copyHeaders(dst *http.Request, src *spi.Request) {
	if src != nil && src.Headers != nil {
		dst.Header = src.Headers.Clone()
	}
	dst.Header.Set("Content-Type", "application/json")
	dst.Header.Set("Accept", "application/json")
	if src == nil {
		return
	}
	for name, value := range src.Cookies {
		dst.AddCookie(&http.Cookie{Name: name, Value: value})
	}
}

func netErr(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return verr.Newf(verr.CodeValidation, "introspection request failed: %s", ue.Err.Error())
	}
	return verr.New(verr.CodeValidation, "introspection request failed")
}

// SDLFromIntrospection converts an introspection HTTP body to SDL and parses
// it. Error text is redacted and does not include the raw body.
func SDLFromIntrospection(body []byte, red *secrets.Redactor) (string, *ast.Schema, error) {
	if red == nil {
		red = secrets.NewRedactor()
	}
	var env introEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil, verr.New(verr.CodeValidation, "introspection response is not JSON")
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			if e.Message != "" {
				msgs = append(msgs, e.Message)
			}
		}
		text := "introspection failed"
		if len(msgs) > 0 {
			text += ": " + strings.Join(msgs, "; ")
		}
		return "", nil, verr.New(verr.CodeValidation, red.Redact(firstLine(text)))
	}
	if env.Data.Schema == nil {
		return "", nil, verr.New(verr.CodeValidation, "introspection response has no schema")
	}
	sdl := schemaSDL(env.Data.Schema)
	parsed, err := ParseSDL(sdl, "introspection.graphql")
	if err != nil {
		return "", nil, verr.New(verr.CodeValidation, red.Redact(err.Error()))
	}
	return sdl, parsed, nil
}

func schemaSDL(s *introSchema) string {
	var b strings.Builder
	b.WriteString("schema {\n")
	if s.QueryType != nil && s.QueryType.Name != "" {
		b.WriteString("  query: " + s.QueryType.Name + "\n")
	}
	if s.MutationType != nil && s.MutationType.Name != "" {
		b.WriteString("  mutation: " + s.MutationType.Name + "\n")
	}
	if s.SubscriptionType != nil && s.SubscriptionType.Name != "" {
		b.WriteString("  subscription: " + s.SubscriptionType.Name + "\n")
	}
	b.WriteString("}\n")
	for i := range s.Types {
		t := &s.Types[i]
		if skipType(t.Name) {
			continue
		}
		writeType(&b, t)
	}
	for _, d := range s.Directives {
		if d.Name == "" || builtinDirective(d.Name) {
			continue
		}
		writeDirective(&b, &d)
	}
	return b.String()
}

func skipType(name string) bool {
	switch name {
	case "", "String", "Int", "Float", "Boolean", "ID":
		return true
	}
	return strings.HasPrefix(name, "__")
}

func builtinDirective(name string) bool {
	switch name {
	case "skip", "include", "deprecated", "specifiedBy":
		return true
	}
	return false
}

func (t *introTypeRef) sdl() string {
	if t == nil {
		return ""
	}
	switch t.Kind {
	case "NON_NULL":
		inner := ""
		if t.OfType != nil {
			inner = t.OfType.sdl()
		}
		if inner == "" {
			return ""
		}
		return inner + "!"
	case "LIST":
		inner := ""
		if t.OfType != nil {
			inner = t.OfType.sdl()
		}
		if inner == "" {
			return ""
		}
		return "[" + inner + "]"
	default:
		if t.Name == nil || !validName(*t.Name) {
			return ""
		}
		return *t.Name
	}
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		ok := r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func writeType(b *strings.Builder, t *introFullType) {
	if !validName(t.Name) {
		return
	}
	if t.Description != nil {
		b.WriteString(gqlDesc(*t.Description, ""))
	}
	switch t.Kind {
	case "SCALAR":
		b.WriteString("scalar " + t.Name + "\n\n")
	case "ENUM":
		b.WriteString("enum " + t.Name + " {\n")
		for _, v := range t.EnumValues {
			if !validName(v.Name) {
				continue
			}
			if v.Description != nil {
				b.WriteString(gqlDesc(*v.Description, "  "))
			}
			b.WriteString("  " + v.Name)
			if v.IsDeprecated {
				b.WriteString(" @deprecated(reason: " + gqlString(reason(v.DeprecationReason)) + ")")
			}
			b.WriteByte('\n')
		}
		b.WriteString("}\n\n")
	case "UNION":
		var members []string
		for _, m := range t.PossibleTypes {
			if m.Name != nil && validName(*m.Name) {
				members = append(members, *m.Name)
			}
		}
		if len(members) == 0 {
			return
		}
		b.WriteString("union " + t.Name + " = " + strings.Join(members, " | ") + "\n\n")
	case "INPUT_OBJECT":
		b.WriteString("input " + t.Name + " {\n")
		for _, f := range t.InputFields {
			writeInput(b, f)
		}
		b.WriteString("}\n\n")
	case "INTERFACE", "OBJECT":
		kw := "type"
		if t.Kind == "INTERFACE" {
			kw = "interface"
		}
		b.WriteString(kw + " " + t.Name)
		var ifaces []string
		for _, iface := range t.Interfaces {
			if iface.Name != nil && validName(*iface.Name) {
				ifaces = append(ifaces, *iface.Name)
			}
		}
		if len(ifaces) > 0 {
			b.WriteString(" implements " + strings.Join(ifaces, " & "))
		}
		b.WriteString(" {\n")
		for _, f := range t.Fields {
			writeField(b, f)
		}
		b.WriteString("}\n\n")
	}
}

func writeField(b *strings.Builder, f introField) {
	typ := f.Type.sdl()
	if !validName(f.Name) || typ == "" || strings.HasPrefix(f.Name, "__") {
		return
	}
	if f.Description != nil {
		b.WriteString(gqlDesc(*f.Description, "  "))
	}
	b.WriteString("  " + f.Name)
	writeArgs(b, f.Args)
	b.WriteString(": " + typ)
	if f.IsDeprecated {
		b.WriteString(" @deprecated(reason: " + gqlString(reason(f.DeprecationReason)) + ")")
	}
	b.WriteByte('\n')
}

func writeInput(b *strings.Builder, f introValue) {
	typ := f.Type.sdl()
	if !validName(f.Name) || typ == "" {
		return
	}
	if f.Description != nil {
		b.WriteString(gqlDesc(*f.Description, "  "))
	}
	b.WriteString("  " + f.Name + ": " + typ)
	if def := defaultLit(f.DefaultValue); def != "" {
		b.WriteString(" = " + def)
	}
	b.WriteByte('\n')
}

func writeArgs(b *strings.Builder, args []introValue) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		typ := a.Type.sdl()
		if !validName(a.Name) || typ == "" {
			continue
		}
		part := a.Name + ": " + typ
		if def := defaultLit(a.DefaultValue); def != "" {
			part += " = " + def
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return
	}
	b.WriteByte('(')
	b.WriteString(strings.Join(parts, ", "))
	b.WriteByte(')')
}

func writeDirective(b *strings.Builder, d *introDirective) {
	if !validName(d.Name) || len(d.Locations) == 0 {
		return
	}
	if d.Description != nil {
		b.WriteString(gqlDesc(*d.Description, ""))
	}
	b.WriteString("directive @" + d.Name)
	writeArgs(b, d.Args)
	var locs []string
	for _, loc := range d.Locations {
		if validName(loc) {
			locs = append(locs, loc)
		}
	}
	if len(locs) == 0 {
		return
	}
	b.WriteString(" on " + strings.Join(locs, " | ") + "\n\n")
}

func defaultLit(v *string) string {
	if v == nil || *v == "" {
		return ""
	}
	s := *v
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	if strings.HasPrefix(s, `"""`) && strings.HasSuffix(s, `"""`) && len(s) >= 6 {
		return s
	}
	if inner, ok := unquoteGraphQLString(s); ok {
		return gqlString(inner)
	}
	return blockLiteral(s)
}

// unquoteGraphQLString reads a GraphQL string that may contain a raw newline.
func unquoteGraphQLString(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || strings.HasPrefix(s, `"""`) {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			if i+1 >= len(s) {
				return "", false
			}
			switch s[i+1] {
			case '"', '\\', '/':
				b.WriteByte(s[i+1])
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		if c == '"' {
			if i != len(s)-1 {
				return "", false
			}
			return b.String(), true
		}
		b.WriteByte(c)
	}
	return "", false
}

func blockLiteral(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, `"""`, `\"""`)
	return "\"\"\"\n" + s + "\n\"\"\""
}

func reason(v *string) string {
	if v == nil || *v == "" {
		return "No longer supported"
	}
	return *v
}

func gqlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func gqlDesc(s, indent string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, `"""`, `""\"`)
	if strings.Contains(s, "\n") {
		return indent + "\"\"\"\n" + indent + strings.ReplaceAll(s, "\n", "\n"+indent) + "\n" + indent + "\"\"\"\n"
	}
	return indent + "\"\"\"" + s + "\"\"\"\n"
}
