package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const widgetSpec = `
openapi: 3.0.3
info: {title: Widgets, version: "1.0.0"}
servers:
  - url: https://example.com/v1
paths:
  /ping:
    get:
      responses:
        "200":
          description: pong
          content:
            text/plain:
              example: pong
            application/json:
              example: {ok: true}
  /widgets/{id}:
    get:
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string, minLength: 1}
        - name: limit
          in: query
          schema: {type: integer, minimum: 1, maximum: 10}
        - name: X-Tenant
          in: header
          required: true
          schema: {type: string, minLength: 1}
      responses:
        "200":
          description: one
          content:
            application/json:
              examples:
                small:
                  value: {size: s}
                large:
                  value: {size: l}
        "404":
          description: missing
          content:
            application/json:
              example: {error: missing}
  /widgets/special:
    get:
      responses:
        "200":
          description: special
          content:
            application/json:
              example: {kind: special}
  /widgets:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: "#/components/schemas/Widget"
      responses:
        "201":
          description: created
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Widget"
components:
  schemas:
    Widget:
      allOf:
        - type: object
          required: [id]
          properties:
            id: {type: integer, minimum: 1, maximum: 5}
        - type: object
          properties:
            name: {type: string, minLength: 1, maxLength: 8}
            tag:
              oneOf:
                - type: string
                  enum: [a, b]
            flags:
              type: array
              items: {type: string}
              minItems: 1
              maxItems: 2
`

func TestOpenAPIRoutingExamplesAndPrefer(t *testing.T) {
	h := mustHandler(t, []byte(widgetSpec), Options{})

	code, ct, body := call(t, h, http.MethodGet, "/widgets/special", nil, "")
	if code != 200 || !strings.Contains(ct, "application/json") {
		t.Fatalf("special: %d %s %s", code, ct, body)
	}
	if got := jsonField(t, body, "kind"); got != "special" {
		t.Fatalf("static path lost to template: %s", body)
	}

	_, _, body = call(t, h, http.MethodGet, "/widgets/abc", nil, "")
	if jsonField(t, body, "size") != "s" {
		t.Fatalf("first example: %s", body)
	}
	_, _, body = call(t, h, http.MethodGet, "/widgets/abc", map[string]string{"Prefer": "example=large"}, "")
	if jsonField(t, body, "size") != "l" {
		t.Fatalf("named example: %s", body)
	}
	code, _, body = call(t, h, http.MethodGet, "/widgets/abc", map[string]string{"Prefer": "code=404"}, "")
	if code != 404 || jsonField(t, body, "error") != "missing" {
		t.Fatalf("prefer code: %d %s", code, body)
	}

	code, ct, body = call(t, h, http.MethodGet, "/no-such", nil, "")
	if code != 404 || !strings.Contains(ct, "application/problem+json") || !strings.Contains(string(body), "no operation") {
		t.Fatalf("unknown route: %d %s %s", code, ct, body)
	}

	code, ct, body = call(t, h, http.MethodGet, "/ping", map[string]string{"Accept": "text/plain"}, "")
	if code != 200 || !strings.Contains(ct, "text/plain") || string(body) != "pong" {
		t.Fatalf("plain example: %d %s %q", code, ct, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/v1/ping", map[string]string{"Accept": "text/plain"}, "")
	if code != 200 || string(body) != "pong" {
		t.Fatalf("server base path: %d %s", code, body)
	}
}

func TestOpenAPIValidateAndGenerate(t *testing.T) {
	h := mustHandler(t, []byte(widgetSpec), Options{Validate: true, Seed: 1})

	code, _, body := call(t, h, http.MethodPost, "/widgets", map[string]string{"Content-Type": "application/json"}, "")
	if code != 400 || !strings.Contains(string(body), "body: required") {
		t.Fatalf("missing body: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/widgets", map[string]string{"Content-Type": "application/json"}, `{"name":"ok"}`)
	if code != 400 || !strings.Contains(string(body), "body.id: required") {
		t.Fatalf("missing id: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/widgets", map[string]string{"Content-Type": "application/json"}, `{"id":0,"name":"ok","tag":"a","flags":["x"]}`)
	if code != 400 || !strings.Contains(string(body), "body.id:") {
		t.Fatalf("id bound: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/widgets/abc?limit=0", map[string]string{"X-Tenant": "acme"}, "")
	if code != 400 || !strings.Contains(string(body), "query.limit") {
		t.Fatalf("query: %d %s", code, body)
	}
	code, _, _ = call(t, h, http.MethodGet, "/widgets/abc?limit=2", nil, "")
	if code != 400 {
		t.Fatalf("missing header should be 400, got %d", code)
	}
	code, _, _ = call(t, h, http.MethodGet, "/widgets/abc?limit=2", map[string]string{"X-Tenant": "acme"}, "")
	if code != 200 {
		t.Fatalf("valid get: %d", code)
	}

	gen := mustHandler(t, []byte(widgetSpec), Options{Seed: 1})
	code, ct, generated := call(t, gen, http.MethodPost, "/widgets", nil, "")
	if code != 201 || !strings.Contains(ct, "application/json") {
		t.Fatalf("generated: %d %s %s", code, ct, generated)
	}
	assertWidget(t, generated)
	gen2 := mustHandler(t, []byte(widgetSpec), Options{Seed: 1})
	_, _, same := call(t, gen2, http.MethodPost, "/widgets", nil, "")
	if string(generated) != string(same) {
		t.Fatalf("seed unstable:\n%s\n%s", generated, same)
	}
	gen3 := mustHandler(t, []byte(widgetSpec), Options{Seed: 2})
	_, _, diff := call(t, gen3, http.MethodPost, "/widgets", nil, "")
	if string(generated) == string(diff) {
		t.Fatalf("seed did not change generated body: %s", generated)
	}

	code, _, body = call(t, h, http.MethodPost, "/widgets", map[string]string{"Content-Type": "application/json"}, string(same))
	if code != 201 {
		t.Fatalf("generated body rejected: %d %s", code, body)
	}
}

func assertWidget(t *testing.T, body []byte) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	id, ok := doc["id"].(float64)
	if !ok || id < 1 || id > 5 || id != float64(int(id)) {
		t.Fatalf("id: %v", doc["id"])
	}
	name, _ := doc["name"].(string)
	if len(name) < 1 || len(name) > 8 {
		t.Fatalf("name: %q", name)
	}
	tag, _ := doc["tag"].(string)
	if tag != "a" && tag != "b" {
		t.Fatalf("tag: %v", doc["tag"])
	}
	flags, ok := doc["flags"].([]any)
	if !ok || len(flags) < 1 || len(flags) > 2 {
		t.Fatalf("flags: %v", doc["flags"])
	}
}

func TestOpenAPIRejectsSwagger(t *testing.T) {
	_, err := HandlerBytes([]byte("swagger: \"2.0\"\npaths: {}\n"), Options{})
	if err == nil || !strings.Contains(err.Error(), "Swagger 2.0") {
		t.Fatalf("got %v", err)
	}
}

func mustHandler(t *testing.T, data []byte, opts Options) http.Handler {
	t.Helper()
	h, err := HandlerBytes(data, opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func call(t *testing.T, h http.Handler, method, path string, hdr map[string]string, body string) (int, string, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, rdr)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Content-Type"), b
}

func jsonField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	s, _ := doc[key].(string)
	return s
}
