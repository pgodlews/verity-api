package mock

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCyclicRefDoesNotCrash(t *testing.T) {
	specs := []string{
		`
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      parameters:
        - {name: q, in: query, schema: {$ref: "#/components/schemas/S"}}
      responses: {"200": {description: ok}}
components:
  schemas:
    S: {$ref: "#/components/schemas/S"}
`,
		`
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      parameters:
        - {name: q, in: query, schema: {$ref: "#/components/schemas/A"}}
      responses: {"200": {description: ok}}
components:
  schemas:
    A: {$ref: "#/components/schemas/B"}
    B: {$ref: "#/components/schemas/A"}
`,
		`
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      parameters:
        - name: q
          in: query
          schema:
            type: array
            items: {$ref: "#/components/schemas/S"}
      responses: {"200": {description: ok}}
components:
  schemas:
    S:
      type: array
      items: {$ref: "#/components/schemas/S"}
`,
	}
	for i, spec := range specs {
		h := mustHandler(t, []byte(spec), Options{Validate: true})
		code, ct, body := call(t, h, http.MethodGet, "/p?q=1", nil, "")
		if code == http.StatusInternalServerError || strings.Contains(string(body), "internal error") {
			t.Fatalf("spec %d panicked: %d %s %s", i, code, ct, body)
		}
		if code != http.StatusOK && code != http.StatusBadRequest {
			t.Fatalf("spec %d: %d %s", i, code, body)
		}
	}
}

func TestHandlerRecover(t *testing.T) {
	h := guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	code, ct, body := call(t, h, http.MethodGet, "/", nil, "")
	if code != 500 || !strings.Contains(ct, "application/problem+json") || !strings.Contains(string(body), "internal error") {
		t.Fatalf("recover: %d %s %s", code, ct, body)
	}
	if strings.Contains(string(body), "boom") {
		t.Fatalf("panic value leaked: %s", body)
	}
}

func TestSampleBoundsAndCaps(t *testing.T) {
	g := newGen(map[string]any{}, 1)
	n := g.integer(map[string]any{"minimum": math.MinInt, "maximum": 0})
	if n > 0 {
		t.Fatalf("span sample %d", n)
	}
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /n:
    get:
      responses:
        "200":
          description: n
          content:
            application/json:
              schema: {type: integer, minimum: -9223372036854775808, maximum: 0}
`
	h := mustHandler(t, []byte(spec), Options{})
	code, _, body := call(t, h, http.MethodGet, "/n", nil, "")
	if code != 200 {
		t.Fatalf("startup sample: %d %s", code, body)
	}
	arr, ok := g.sample(map[string]any{
		"type": "array", "minItems": 100000000,
		"items": map[string]any{"type": "integer"},
	}).([]any)
	if !ok || len(arr) == 0 || len(arr) > maxSampleItems {
		t.Fatalf("minItems cap: %#v", arr)
	}
	s, ok := g.sample(map[string]any{"type": "string", "minLength": 10000000000}).(string)
	if !ok || s == "" || len(s) > maxSampleLen {
		t.Fatalf("minLength cap: %d", len(s))
	}
}

func TestCyclicSampleOmitsNull(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /n:
    get:
      responses:
        "200":
          description: n
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Node"}
components:
  schemas:
    Node:
      type: object
      required: [name, req]
      properties:
        name: {type: string}
        parent: {$ref: "#/components/schemas/Node"}
        req: {$ref: "#/components/schemas/Node"}
        parentN:
          nullable: true
          $ref: "#/components/schemas/Node"
        children:
          type: array
          items: {$ref: "#/components/schemas/Node"}
`
	h := mustHandler(t, []byte(spec), Options{Seed: 0})
	code, _, body := call(t, h, http.MethodGet, "/n", nil, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["parent"]; ok {
		t.Fatalf("optional cycle emitted: %s", body)
	}
	req, ok := doc["req"].(map[string]any)
	if !ok {
		t.Fatalf("required cycle: %#v", doc["req"])
	}
	if _, null := req["parent"]; null {
		t.Fatalf("nested null: %s", body)
	}
	if _, ok := doc["parentN"]; !ok || doc["parentN"] != nil {
		t.Fatalf("nullable cycle: %#v", doc["parentN"])
	}
	children, ok := doc["children"].([]any)
	if !ok || len(children) != 0 {
		t.Fatalf("children: %#v", doc["children"])
	}
}

func TestResponseRangesAndDefault(t *testing.T) {
	rangeSpec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      responses:
        "2XX":
          description: ok
          content:
            application/json:
              example: {cls: range}
        "400":
          description: bad
          content:
            application/json:
              example: {cls: bad}
        "4XX":
          description: class
          content:
            application/json:
              example: {cls: client}
`
	h := mustHandler(t, []byte(rangeSpec), Options{})
	code, _, body := call(t, h, http.MethodGet, "/p", nil, "")
	if code != 200 || jsonField(t, body, "cls") != "range" {
		t.Fatalf("no prefer: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p", map[string]string{"Prefer": "code=201"}, "")
	if code != 201 || jsonField(t, body, "cls") != "range" {
		t.Fatalf("2XX: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p", map[string]string{"Prefer": "code=400"}, "")
	if code != 400 || jsonField(t, body, "cls") != "bad" {
		t.Fatalf("exact before range: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p", map[string]string{"Prefer": "code=404"}, "")
	if code != 404 || jsonField(t, body, "cls") != "client" {
		t.Fatalf("4XX: %d %s", code, body)
	}

	defSpec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      responses:
        default:
          description: d
          content:
            application/json:
              example: {cls: def}
`
	h = mustHandler(t, []byte(defSpec), Options{})
	code, _, body = call(t, h, http.MethodGet, "/p", nil, "")
	if code != 200 || jsonField(t, body, "cls") != "def" {
		t.Fatalf("default: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p", map[string]string{"Prefer": "code=200"}, "")
	if code != 200 || jsonField(t, body, "cls") != "def" {
		t.Fatalf("prefer default: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p", map[string]string{"Prefer": "code=404"}, "")
	if code != 404 || jsonField(t, body, "cls") != "def" {
		t.Fatalf("prefer other default: %d %s", code, body)
	}
}

func TestOneOfExactlyOne(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              oneOf:
                - type: object
                  required: [a]
                  properties: {a: {type: string}}
                - type: object
                  required: [b]
                  properties: {b: {type: string}}
      responses: {"200": {description: ok, content: {application/json: {example: {ok: true}}}}}
  /q:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              anyOf:
                - type: object
                  required: [a]
                  properties: {a: {type: string}}
                - type: object
                  required: [b]
                  properties: {b: {type: string}}
      responses: {"200": {description: ok, content: {application/json: {example: {ok: true}}}}}
`
	h := mustHandler(t, []byte(spec), Options{Validate: true})
	code, _, body := call(t, h, http.MethodPost, "/p", map[string]string{"Content-Type": "application/json"}, `{"a":"x","b":"y"}`)
	if code != 400 || !strings.Contains(string(body), "exactly one") {
		t.Fatalf("oneOf: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/p", map[string]string{"Content-Type": "application/json"}, `{"a":"x"}`)
	if code != 200 {
		t.Fatalf("one branch: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/q", map[string]string{"Content-Type": "application/json"}, `{"a":"x","b":"y"}`)
	if code != 200 {
		t.Fatalf("anyOf: %d %s", code, body)
	}
}

func TestQueryArrayStyle(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      parameters:
        - name: ids
          in: query
          schema: {type: array, minItems: 2, items: {type: integer}}
      responses: {"200": {description: ok}}
  /csv:
    get:
      parameters:
        - name: ids
          in: query
          explode: false
          schema: {type: array, minItems: 2, items: {type: integer}}
      responses: {"200": {description: ok}}
  /pipe:
    get:
      parameters:
        - name: ids
          in: query
          style: pipeDelimited
          schema: {type: array, minItems: 2, items: {type: integer}}
      responses: {"200": {description: ok}}
`
	h := mustHandler(t, []byte(spec), Options{Validate: true})
	code, _, body := call(t, h, http.MethodGet, "/p?ids=1&ids=2", nil, "")
	if code != 200 {
		t.Fatalf("explode: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p?ids=1", nil, "")
	if code != 400 || !strings.Contains(string(body), "length") {
		t.Fatalf("one value: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/p?ids=1,2", nil, "")
	if code != 400 {
		t.Fatalf("comma is not exploded form: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/csv?ids=1,2", nil, "")
	if code != 200 {
		t.Fatalf("explode false: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/pipe?ids=1|2", nil, "")
	if code != 200 {
		t.Fatalf("pipe: %d %s", code, body)
	}
}

func TestAcceptQuality(t *testing.T) {
	h := mustHandler(t, []byte(widgetSpec), Options{})
	code, ct, body := call(t, h, http.MethodGet, "/ping", map[string]string{"Accept": "application/json;q=0.1, text/plain;q=0.9"}, "")
	if code != 200 || !strings.Contains(ct, "text/plain") || string(body) != "pong" {
		t.Fatalf("q order: %d %s %q", code, ct, body)
	}
	code, ct, body = call(t, h, http.MethodGet, "/ping", map[string]string{"Accept": "application/json;q=0, text/plain"}, "")
	if code != 200 || !strings.Contains(ct, "text/plain") || string(body) != "pong" {
		t.Fatalf("q=0: %d %s %q", code, ct, body)
	}
	code, ct, body = call(t, h, http.MethodGet, "/ping", map[string]string{"Accept": "text/plain;q=0.2, application/json;q=0.8"}, "")
	if code != 200 || !strings.Contains(ct, "json") {
		t.Fatalf("higher q: %d %s %q", code, ct, body)
	}
}

func TestExternalValue(t *testing.T) {
	root := t.TempDir()
	specDir := filepath.Join(root, "spec")
	if err := os.Mkdir(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specDir, "pet.json"), []byte(`{"id":"inside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.json"), []byte(`{"id":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.json"), filepath.Join(specDir, "link.json")); err != nil {
		t.Fatal(err)
	}
	spec := func(ref string) string {
		return `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: {type: string, example: generated}
              examples:
                pet:
                  externalValue: ` + ref + `
`
	}
	path := filepath.Join(specDir, "spec.yaml")
	if err := os.WriteFile(path, []byte(spec("./pet.json")), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := call(t, h, http.MethodGet, "/p", nil, "")
	if jsonField(t, body, "id") != "inside" {
		t.Fatalf("fixture: %s", body)
	}

	stderr := captureStderr(t, func() {
		if err := os.WriteFile(path, []byte(spec("../outside.json")), 0o600); err != nil {
			t.Fatal(err)
		}
		h, err = Handler(path, Options{})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "externalValue") {
		t.Fatalf("stderr: %q", stderr)
	}
	_, _, body = call(t, h, http.MethodGet, "/p", nil, "")
	if strings.Contains(string(body), "outside") {
		t.Fatalf("escaped: %s", body)
	}

	stderr = captureStderr(t, func() {
		if err := os.WriteFile(path, []byte(spec("link.json")), 0o600); err != nil {
			t.Fatal(err)
		}
		h, err = Handler(path, Options{})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "externalValue") {
		t.Fatalf("symlink stderr: %q", stderr)
	}
	_, _, body = call(t, h, http.MethodGet, "/p", nil, "")
	if strings.Contains(string(body), "outside") {
		t.Fatalf("symlink: %s", body)
	}

	stderr = captureStderr(t, func() {
		if err := os.WriteFile(path, []byte(spec("https://example.com/pet.json")), 0o600); err != nil {
			t.Fatal(err)
		}
		h, err = Handler(path, Options{})
		if err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stderr, "externalValue") {
		t.Fatalf("url stderr: %q", stderr)
	}
}

func TestServerURLVariable(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
servers:
  - url: https://api.example.com/v1/{version}
    variables:
      version:
        default: "2"
paths:
  /pets:
    get:
      responses:
        "200":
          description: ok
          content:
            text/plain:
              example: pets
`
	h := mustHandler(t, []byte(spec), Options{})
	code, _, body := call(t, h, http.MethodGet, "/v1/2/pets", nil, "")
	if code != 200 || string(body) != "pets" {
		t.Fatalf("var default: %d %s", code, body)
	}
}

func TestOpenAPIBodyTooLarge(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /p:
    post:
      requestBody:
        content:
          application/json:
            schema: {type: object}
      responses: {"200": {description: ok}}
`
	h := mustHandler(t, []byte(spec), Options{Validate: true})
	code, ct, body := call(t, h, http.MethodPost, "/p", map[string]string{"Content-Type": "application/json"}, strings.Repeat("x", maxBody+1))
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(ct, "problem+json") {
		t.Fatalf("oversize: %d %s %s", code, ct, body)
	}
}

func TestInvalidPattern(t *testing.T) {
	spec := `
openapi: "3.0.3"
info: {title: t, version: "1"}
paths:
  /bad:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string, pattern: "["}
      responses: {"200": {description: ok}}
  /ok:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string, pattern: "^a+$"}
      responses: {"200": {description: ok}}
`
	h := mustHandler(t, []byte(spec), Options{Validate: true})
	code, _, body := call(t, h, http.MethodPost, "/bad", map[string]string{"Content-Type": "application/json"}, `{"name":"a"}`)
	if code != 400 || !strings.Contains(string(body), "invalid pattern") {
		t.Fatalf("bad pattern: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/ok", map[string]string{"Content-Type": "application/json"}, `{"name":"b"}`)
	if code != 400 || !strings.Contains(string(body), "must match pattern") {
		t.Fatalf("mismatch: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/ok", map[string]string{"Content-Type": "application/json"}, `{"name":"aa"}`)
	if code != 200 {
		t.Fatalf("match: %d %s", code, body)
	}
}

func TestProjectDelayOnMiss(t *testing.T) {
	const doc = `
schemaVersion: "1.0"
name: Mocks
suites:
  - name: s
    cases:
      - name: c
        steps:
          - {name: st, protocol: rest}
mocks:
  - {method: GET, path: /widgets, body: ok}
`
	h := mustHandler(t, []byte(doc), Options{Delay: 80 * time.Millisecond})
	start := time.Now()
	code, _, _ := call(t, h, http.MethodGet, "/missing", nil, "")
	if code != 404 || time.Since(start) < 50*time.Millisecond {
		t.Fatalf("404 delay: %d %s", code, time.Since(start))
	}
	start = time.Now()
	code, _, _ = call(t, h, http.MethodPost, "/widgets", nil, strings.Repeat("x", maxBody+1))
	if code != 413 || time.Since(start) < 50*time.Millisecond {
		t.Fatalf("413 delay: %d %s", code, time.Since(start))
	}
}

func TestWildcardHostAdvertisesLoopback(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "specs", "petstore.openapi.yaml")
	var srv *Server
	stderr := captureStderr(t, func() {
		var err error
		srv, err = Open(path, Options{Host: "0.0.0.0", Port: 0})
		if err != nil {
			t.Fatal(err)
		}
	})
	defer srv.ln.Close()
	if !strings.Contains(srv.URL, "127.0.0.1:") {
		t.Fatalf("url %s", srv.URL)
	}
	addr := srv.ln.Addr().String()
	if !strings.HasPrefix(addr, "0.0.0.0:") && !strings.HasPrefix(addr, "[::]:") {
		t.Fatalf("bind %s", addr)
	}
	if !strings.Contains(stderr, "all interfaces") {
		t.Fatalf("stderr %q", stderr)
	}
	if srv.http.ReadTimeout < 30*time.Second || srv.http.WriteTimeout < 30*time.Second || srv.http.IdleTimeout <= 0 {
		t.Fatalf("timeouts read=%s write=%s idle=%s", srv.http.ReadTimeout, srv.http.WriteTimeout, srv.http.IdleTimeout)
	}
}

func TestListenTimeoutCoversDelay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.yaml")
	doc := `
schemaVersion: "1.0"
name: Mocks
mocks:
  - {method: GET, path: /ping, body: ok, delayMs: 8000}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := Open(path, Options{Port: 0, Delay: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.ln.Close()
	want := 8*time.Second + 30*time.Second
	if srv.http.ReadTimeout != want || srv.http.WriteTimeout != want {
		t.Fatalf("read %s write %s", srv.http.ReadTimeout, srv.http.WriteTimeout)
	}
}

func TestMocksOnlyProject(t *testing.T) {
	const doc = `
schemaVersion: "1.0"
name: Mocks
mocks:
  - {method: GET, path: /ping, body: ok}
`
	h := mustHandler(t, []byte(doc), Options{})
	code, _, body := call(t, h, http.MethodGet, "/ping", nil, "")
	if code != 200 || string(body) != "ok" {
		t.Fatalf("mocks only: %d %s", code, body)
	}
}

func TestWSDLOccursCapAndOptional(t *testing.T) {
	d := &wsdlDoc{}
	got := d.elementXML(xsdElement{Name: "item", MinOccurs: "100000000", Type: "xsd:string"}, 0, 1)
	if n := strings.Count(got, "<item>"); n == 0 || n > maxSampleOccurs {
		t.Fatalf("repeats %d", n)
	}
	if got := d.elementXML(xsdElement{Name: "child", MinOccurs: "0"}, 0, 9); got != "" {
		t.Fatalf("optional at limit: %q", got)
	}
	if got := d.elementXML(xsdElement{Name: "label"}, 0, 9); !strings.Contains(got, "label") {
		t.Fatalf("required at limit: %q", got)
	}
}

func TestSOAP12SelectsBinding(t *testing.T) {
	const both = `<?xml version="1.0" encoding="UTF-8"?>
<definitions targetNamespace="urn:example:calc" xmlns="http://schemas.xmlsoap.org/wsdl/" xmlns:tns="urn:example:calc"
    xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/" xmlns:soap12="http://schemas.xmlsoap.org/wsdl/soap12/">
  <message name="PingRequest"/>
  <message name="PingResponse"/>
  <portType name="P">
    <operation name="Ping"><input message="tns:PingRequest"/><output message="tns:PingResponse"/></operation>
  </portType>
  <binding name="B11" type="tns:P">
    <soap:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <operation name="Ping"><soap:operation soapAction="urn:ping"/></operation>
  </binding>
  <binding name="B12" type="tns:P">
    <soap12:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <operation name="Ping"><soap12:operation soapAction="urn:ping"/></operation>
  </binding>
  <service name="S"><port name="P" binding="tns:B12"><soap12:address location="http://example.com/calc"/></port></service>
</definitions>`
	h := mustHandler(t, []byte(both), Options{})
	code, ct, body := call(t, h, http.MethodPost, "/", map[string]string{
		"Content-Type": `application/soap+xml; charset=utf-8; action="urn:ping"`,
	}, "")
	if code != 200 || !strings.Contains(ct, "application/soap+xml") || !strings.Contains(string(body), "http://www.w3.org/2003/05/soap-envelope") {
		t.Fatalf("1.2: %d %s %s", code, ct, body)
	}
	if strings.Contains(string(body), "http://schemas.xmlsoap.org/soap/envelope/") {
		t.Fatalf("1.1 envelope for soap+xml: %s", body)
	}
	code, ct, body = call(t, h, http.MethodPost, "/", map[string]string{
		"Content-Type": "text/xml",
		"SOAPAction":   `"urn:ping"`,
	}, "")
	if code != 200 || !strings.Contains(ct, "text/xml") || !strings.Contains(string(body), "http://schemas.xmlsoap.org/soap/envelope/") {
		t.Fatalf("1.1: %d %s %s", code, ct, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/", map[string]string{"Content-Type": "application/soap+xml"}, "")
	if code != 500 || !strings.Contains(string(body), "soap:Reason") || !strings.Contains(string(body), "http://www.w3.org/2003/05/soap-envelope") {
		t.Fatalf("1.2 fault: %d %s", code, body)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	b, _ := io.ReadAll(r)
	_ = r.Close()
	return string(b)
}
