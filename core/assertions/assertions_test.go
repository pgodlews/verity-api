package assertions

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/secrets"
	"github.com/pgodlews/verity-api/core/spi"
)

func eval(t *testing.T, a spi.Assertion, resp *spi.Response, spec spi.AssertionSpec) *spi.AssertionResult {
	t.Helper()
	res, err := a.Evaluate(context.Background(), &spi.AssertionContext{
		Response: resp, Redactor: secrets.NewRedactor(),
	}, spec)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func jsonResp(body string) *spi.Response {
	return &spi.Response{
		StatusCode: 200, Status: "200 OK",
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    []byte(body), Duration: 5 * time.Millisecond,
	}
}

func TestStatusAssertion(t *testing.T) {
	a := &statusAssertion{}
	if !eval(t, a, jsonResp("{}"), spi.AssertionSpec{Value: 200}).Passed {
		t.Fatal("status 200 eq 200 should pass")
	}
	if eval(t, a, jsonResp("{}"), spi.AssertionSpec{Value: 404}).Passed {
		t.Fatal("status 200 eq 404 should fail")
	}
	if !eval(t, a, jsonResp("{}"), spi.AssertionSpec{Op: "lt", Value: 300}).Passed {
		t.Fatal("status 200 lt 300 should pass")
	}
}

func TestJSONPathAssertion(t *testing.T) {
	a := &jsonPathAssertion{}
	body := `{"user":{"name":"ada","roles":["admin","dev"]},"count":2}`
	if !eval(t, a, jsonResp(body), spi.AssertionSpec{Expr: "$.user.name", Value: "ada"}).Passed {
		t.Fatal("name should equal ada")
	}
	if !eval(t, a, jsonResp(body), spi.AssertionSpec{Expr: "$.count", Op: "eq", Value: 2}).Passed {
		t.Fatal("count should equal 2")
	}
	if !eval(t, a, jsonResp(body), spi.AssertionSpec{Expr: "$.user.name", Op: "exists"}).Passed {
		t.Fatal("name should exist")
	}
	if eval(t, a, jsonResp(body), spi.AssertionSpec{Expr: "$.missing", Op: "exists"}).Passed {
		t.Fatal("missing should not exist")
	}
}

func TestXPathAssertion(t *testing.T) {
	a := &xPathAssertion{}
	resp := &spi.Response{Body: []byte(`<root><status>OK</status></root>`)}
	if !eval(t, a, resp, spi.AssertionSpec{Expr: "//status", Value: "OK"}).Passed {
		t.Fatal("xpath //status should equal OK")
	}
}

func TestContainsAndRegex(t *testing.T) {
	c := &containsAssertion{}
	if !eval(t, c, jsonResp("hello world"), spi.AssertionSpec{Value: "world"}).Passed {
		t.Fatal("contains world should pass")
	}
	re := &regexAssertion{}
	if !eval(t, re, jsonResp("order-12345"), spi.AssertionSpec{Expr: `order-\d+`}).Passed {
		t.Fatal("regex should match")
	}
}

func TestHeaderAssertion(t *testing.T) {
	a := &headerAssertion{}
	resp := jsonResp("{}")
	if !eval(t, a, resp, spi.AssertionSpec{Expr: "Content-Type", Op: "contains", Value: "json"}).Passed {
		t.Fatal("content-type should contain json")
	}
}

func TestHeaderAssertionNotExists(t *testing.T) {
	a := &headerAssertion{}
	resp := jsonResp("{}")

	absent := eval(t, a, resp, spi.AssertionSpec{Expr: "X-Missing", Op: "not_exists"})
	if !absent.Passed {
		t.Fatalf("absent header should pass not_exists, got message: %s", absent.Message)
	}

	present := eval(t, a, resp, spi.AssertionSpec{Expr: "Content-Type", Op: "not_exists"})
	if present.Passed {
		t.Fatal("present header should fail not_exists")
	}
	if present.Message == "" {
		t.Fatal("failure message should name the header")
	}
}

func TestJSONSchemaAssertion(t *testing.T) {
	a := &jsonSchemaAssertion{}
	schema := map[string]any{
		"type":     "object",
		"required": []any{"id"},
		"properties": map[string]any{
			"id": map[string]any{"type": "number"},
		},
	}
	good := eval(t, a, jsonResp(`{"id":1}`), spi.AssertionSpec{Config: map[string]any{"schema": schema}})
	if !good.Passed {
		t.Fatalf("valid doc should pass: %s", good.Message)
	}
	bad := eval(t, a, jsonResp(`{"name":"x"}`), spi.AssertionSpec{Config: map[string]any{"schema": schema}})
	if bad.Passed {
		t.Fatal("doc missing required id should fail")
	}
}

func TestXSDWellformed(t *testing.T) {
	a := &xsdAssertion{}
	if !eval(t, a, &spi.Response{Body: []byte(`<a><b/></a>`)}, spi.AssertionSpec{}).Passed {
		t.Fatal("well-formed XML should pass")
	}
	if eval(t, a, &spi.Response{Body: []byte(`<a><b></a>`)}, spi.AssertionSpec{}).Passed {
		t.Fatal("malformed XML should fail")
	}
	if !eval(t, a, &spi.Response{Body: []byte(`<Resp><x/></Resp>`)},
		spi.AssertionSpec{Config: map[string]any{"rootElement": "Resp"}}).Passed {
		t.Fatal("root element check should pass")
	}
}

func TestRegisterInstallsAll(t *testing.T) {
	reg := registry.New()
	Register(reg)
	for _, typ := range []string{"status", "response_time", "jsonpath", "xpath",
		"contains", "regex", "header", "json_schema", "xsd", "graphql_schema"} {
		if _, err := reg.Assertion(typ); err != nil {
			t.Errorf("assertion %q not registered", typ)
		}
	}
}

func TestJSONPathExistsNull(t *testing.T) {
	a := &jsonPathAssertion{}
	resp := jsonResp(`{"user":null,"empty":""}`)

	// An existing key with null value must pass exists and fail not_exists
	ex := eval(t, a, resp, spi.AssertionSpec{Expr: "$.user", Op: "exists"})
	if !ex.Passed {
		t.Fatalf("$.user (null) should pass exists, got: %s", ex.Message)
	}
	nex := eval(t, a, resp, spi.AssertionSpec{Expr: "$.user", Op: "not_exists"})
	if nex.Passed {
		t.Fatal("$.user (null) should fail not_exists")
	}

	// Non-existent key
	missEx := eval(t, a, resp, spi.AssertionSpec{Expr: "$.missing", Op: "exists"})
	if missEx.Passed {
		t.Fatal("$.missing should fail exists")
	}
	missNex := eval(t, a, resp, spi.AssertionSpec{Expr: "$.missing", Op: "not_exists"})
	if !missNex.Passed {
		t.Fatalf("$.missing should pass not_exists, got: %s", missNex.Message)
	}
}

func TestXPathExists(t *testing.T) {
	a := &xPathAssertion{}
	resp := &spi.Response{Body: []byte(`<root><empty></empty></root>`)}

	ex := eval(t, a, resp, spi.AssertionSpec{Expr: "//empty", Op: "exists"})
	if !ex.Passed {
		t.Fatalf("//empty should pass exists, got: %s", ex.Message)
	}
	nex := eval(t, a, resp, spi.AssertionSpec{Expr: "//empty", Op: "not_exists"})
	if nex.Passed {
		t.Fatal("//empty should fail not_exists")
	}

	missEx := eval(t, a, resp, spi.AssertionSpec{Expr: "//missing", Op: "exists"})
	if missEx.Passed {
		t.Fatal("//missing should fail exists")
	}
	missNex := eval(t, a, resp, spi.AssertionSpec{Expr: "//missing", Op: "not_exists"})
	if !missNex.Passed {
		t.Fatalf("//missing should pass not_exists, got: %s", missNex.Message)
	}
}

func TestContainsEmptyNeedle(t *testing.T) {
	c := &containsAssertion{}
	res := eval(t, c, jsonResp("any body"), spi.AssertionSpec{})
	if res.Passed {
		t.Fatal("contains with empty value and expr should fail")
	}
}
