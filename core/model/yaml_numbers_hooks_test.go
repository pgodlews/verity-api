package model

import (
	"encoding/json"
	"testing"
)

const bigInt = "9007199254740993"

// Big JSON numbers must survive YAML loading in hook steps and mock bodies,
// not only in suite case steps, and an explicitly tagged string stays a
// string.
func TestYAMLNumbersInHooksMocksAndTaggedStrings(t *testing.T) {
	src := `schemaVersion: "1.0"
name: n
mocks:
  - method: GET
    path: /x
    json: { id: ` + bigInt + ` }
suites:
  - name: s
    setup:
      name: up
      steps:
        - name: create
          protocol: rest
          request: { url: "http://127.0.0.1/", json: { id: ` + bigInt + ` } }
    teardown:
      name: down
      steps:
        - name: delete
          protocol: rest
          request: { url: "http://127.0.0.1/", json: { id: ` + bigInt + ` } }
    cases:
      - name: c
        steps:
          - name: tagged
            protocol: rest
            request: { url: "http://127.0.0.1/", json: { code: !!str 123, n: 7 } }
`
	p, err := Decode([]byte(src), "p.yaml")
	if err != nil {
		t.Fatal(err)
	}
	check := func(where string, v any) {
		t.Helper()
		m, _ := v.(map[string]any)
		if m["id"] != json.Number(bigInt) {
			t.Errorf("%s id = %#v, want json.Number(%s)", where, m["id"], bigInt)
		}
	}
	check("setup", p.Suites[0].Setup.Steps[0].Request["json"])
	check("teardown", p.Suites[0].Teardown.Steps[0].Request["json"])
	check("mock", p.Mocks[0].JSON)
	body := p.Suites[0].Cases[0].Steps[0].Request["json"].(map[string]any)
	if body["code"] != "123" {
		t.Errorf("!!str 123 = %#v, want string", body["code"])
	}
	if body["n"] != json.Number("7") {
		t.Errorf("n = %#v", body["n"])
	}
}
