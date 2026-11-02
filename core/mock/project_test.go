package mock

import (
	"net/http"
	"strings"
	"testing"
)

const projectYAML = `
schemaVersion: "1.0"
name: Mocks
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: st
            protocol: rest
mocks:
  - method: GET
    path: /widgets/{id}
    status: 200
    json: {id: any}
  - method: GET
    path: /widgets/{id}
    status: 200
    json: {id: one}
    match:
      query: {active: "true"}
      headers: {X-Tenant: acme}
  - method: GET
    path: /widgets/special
    status: 200
    body: special
    headers:
      X-Kind: static
  - method: POST
    path: /widgets
    status: 201
    body: '{"ok":true}'
    match:
      bodyContains: widget
`

func TestProjectMockMatchAndPrecedence(t *testing.T) {
	h := mustHandler(t, []byte(projectYAML), Options{})

	code, _, body := call(t, h, http.MethodGet, "/widgets/special", nil, "")
	if code != 200 || string(body) != "special" {
		t.Fatalf("static: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/widgets/9", nil, "")
	if code != 200 || !strings.Contains(string(body), "any") {
		t.Fatalf("template: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/widgets/9?active=true", map[string]string{"X-Tenant": "acme"}, "")
	if code != 200 || !strings.Contains(string(body), "one") {
		t.Fatalf("match rules: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodGet, "/widgets/9?active=true", nil, "")
	if code != 200 || !strings.Contains(string(body), "any") {
		t.Fatalf("partial match should fall through: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/widgets", nil, `{"name":"widget"}`)
	if code != 201 || !strings.Contains(string(body), "ok") {
		t.Fatalf("body contains: %d %s", code, body)
	}
	code, _, body = call(t, h, http.MethodPost, "/widgets", nil, `{"name":"other"}`)
	if code != 404 || !strings.Contains(string(body), "no mock") {
		t.Fatalf("unmatched body: %d %s", code, body)
	}
}

func TestProjectMockValidation(t *testing.T) {
	bad := `
schemaVersion: "1.0"
name: Mocks
suites:
  - name: s
    cases:
      - name: c
        steps:
          - name: st
            protocol: rest
mocks:
  - method: GET
    path: widgets
    body: x
    json: {a: 1}
`
	_, err := HandlerBytes([]byte(bad), Options{})
	if err == nil || !strings.Contains(err.Error(), "validation_error") {
		t.Fatalf("got %v", err)
	}
}
