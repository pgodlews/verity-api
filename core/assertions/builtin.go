package assertions

import (
	"context"
	"fmt"
	"strings"

	"github.com/antchfx/xmlquery"

	"github.com/pgodlews/verity-api/core/internal/jsonq"
	"github.com/pgodlews/verity-api/core/registry"
	"github.com/pgodlews/verity-api/core/spi"
)

// Register installs all built-in assertions into the registry. Call this from
// the host's bootstrap to make the reference assertions available.
func Register(r *registry.Registry) {
	r.RegisterAssertion(&statusAssertion{})
	r.RegisterAssertion(&responseTimeAssertion{})
	r.RegisterAssertion(&jsonPathAssertion{})
	r.RegisterAssertion(&xPathAssertion{})
	r.RegisterAssertion(&containsAssertion{})
	r.RegisterAssertion(&regexAssertion{})
	r.RegisterAssertion(&headerAssertion{})
	r.RegisterAssertion(&jsonSchemaAssertion{})
	r.RegisterAssertion(&xsdAssertion{})
	r.RegisterAssertion(&graphqlSchemaAssertion{})
}

func result(typ, desc string, passed bool, msg string, expected, actual any) *spi.AssertionResult {
	return &spi.AssertionResult{
		Passed: passed, Type: typ, Description: desc,
		Message: msg, Expected: expected, Actual: actual,
	}
}

// statusAssertion checks the response status code. Default op is "eq".
type statusAssertion struct{}

func (statusAssertion) Type() string { return "status" }
func (a statusAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	actual := actx.Response.StatusCode
	expected := spec.Value
	if expected == nil && spec.Expr != "" {
		expected = spec.Expr
	}
	op := spec.Op
	if op == "" {
		op = "eq"
	}
	passed, msg := compare(op, actual, expected)
	return result(a.Type(), spec.Description, passed, msg, expected, actual), nil
}

// responseTimeAssertion checks the response duration in milliseconds.
// Default op is "lte".
type responseTimeAssertion struct{}

func (responseTimeAssertion) Type() string { return "response_time" }
func (a responseTimeAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	actualMS := actx.Response.Duration.Milliseconds()
	op := spec.Op
	if op == "" {
		op = "lte"
	}
	passed, msg := compare(op, actualMS, spec.Value)
	return result(a.Type(), spec.Description, passed,
		msg+fmt.Sprintf(" (took %dms)", actualMS), spec.Value, actualMS), nil
}

// jsonPathAssertion evaluates a JSONPath against a JSON body, then compares.
// With op "exists"/"not_exists" only presence is checked.
type jsonPathAssertion struct{}

func (jsonPathAssertion) Type() string { return "jsonpath" }
func (a jsonPathAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	val, found, err := evalJSONPath(actx.Response.Body, spec.Expr)
	if err != nil {
		return result(a.Type(), spec.Description, false,
			actx.Redactor.Redact(err.Error()), spec.Value, nil), nil
	}
	op := spec.Op
	if op == "" {
		if spec.Value == nil {
			op = "exists"
		} else {
			op = "eq"
		}
	}
	if op == "exists" || op == "not_exists" {
		passed := found
		msg := fmt.Sprintf("expected JSONPath %q to exist", spec.Expr)
		if op == "not_exists" {
			passed = !found
			msg = fmt.Sprintf("expected JSONPath %q to be absent", spec.Expr)
		}
		var actual any
		if found {
			actual = val
		}
		return result(a.Type(), spec.Description, passed, msg, nil, actual), nil
	}
	if !found {
		return result(a.Type(), spec.Description, false,
			fmt.Sprintf("JSONPath %q matched nothing", spec.Expr), spec.Value, nil), nil
	}
	passed, msg := compare(op, val, spec.Value)
	return result(a.Type(), spec.Description, passed, msg, spec.Value, val), nil
}

// xPathAssertion evaluates an XPath against an XML body, then compares.
type xPathAssertion struct{}

func (xPathAssertion) Type() string { return "xpath" }
func (a xPathAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	doc, err := xmlquery.Parse(strings.NewReader(string(actx.Response.Body)))
	if err != nil {
		return result(a.Type(), spec.Description, false,
			actx.Redactor.Redact("parse XML: "+err.Error()), spec.Value, nil), nil
	}
	node, err := xmlquery.Query(doc, spec.Expr)
	if err != nil {
		return result(a.Type(), spec.Description, false,
			fmt.Sprintf("invalid XPath %q: %v", spec.Expr, err), spec.Value, nil), nil
	}
	op := spec.Op
	if op == "" {
		if spec.Value == nil {
			op = "exists"
		} else {
			op = "eq"
		}
	}
	if op == "exists" || op == "not_exists" {
		passed := node != nil
		msg := fmt.Sprintf("expected XPath %q to exist", spec.Expr)
		if op == "not_exists" {
			passed = node == nil
			msg = fmt.Sprintf("expected XPath %q to be absent", spec.Expr)
		}
		var actual any
		if node != nil {
			actual = node.InnerText()
		}
		return result(a.Type(), spec.Description, passed, msg, nil, actual), nil
	}
	if node == nil {
		return result(a.Type(), spec.Description, false,
			fmt.Sprintf("XPath %q matched nothing", spec.Expr), spec.Value, nil), nil
	}
	actual := node.InnerText()
	passed, msg := compare(op, actual, spec.Value)
	return result(a.Type(), spec.Description, passed, msg, spec.Value, actual), nil
}

// containsAssertion checks the raw body contains the given substring.
type containsAssertion struct{}

func (containsAssertion) Type() string { return "contains" }
func (a containsAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	body := string(actx.Response.Body)
	needle := toString(spec.Value)
	if needle == "" {
		needle = spec.Expr
	}
	if needle == "" {
		return result(a.Type(), spec.Description, false,
			"contains assertion requires a non-empty value or expr", spec.Value, nil), nil
	}
	passed := strings.Contains(body, needle)
	msg := fmt.Sprintf("expected body to contain %q", needle)
	if spec.Op == "not_exists" {
		passed = !passed
		msg = fmt.Sprintf("expected body NOT to contain %q", needle)
	}
	return result(a.Type(), spec.Description, passed, actx.Redactor.Redact(msg), needle, nil), nil
}

// regexAssertion checks the raw body matches a regex.
type regexAssertion struct{}

func (regexAssertion) Type() string { return "regex" }
func (a regexAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	pattern := spec.Expr
	if pattern == "" {
		pattern = toString(spec.Value)
	}
	passed, msg := compare("matches", string(actx.Response.Body), pattern)
	return result(a.Type(), spec.Description, passed, actx.Redactor.Redact(msg), pattern, nil), nil
}

// headerAssertion checks a response header. Expr is the header name.
type headerAssertion struct{}

func (headerAssertion) Type() string { return "header" }
func (a headerAssertion) Evaluate(_ context.Context, actx *spi.AssertionContext, spec spi.AssertionSpec) (*spi.AssertionResult, error) {
	actual := actx.Response.Headers.Get(spec.Expr)
	op := spec.Op
	if op == "" {
		if spec.Value == nil {
			op = "exists"
		} else {
			op = "eq"
		}
	}
	if op == "exists" {
		passed := actual != ""
		return result(a.Type(), spec.Description, passed,
			fmt.Sprintf("expected header %q to be present", spec.Expr), nil, actual), nil
	}
	if op == "not_exists" {
		passed := actual == ""
		return result(a.Type(), spec.Description, passed,
			fmt.Sprintf("expected header %q to be absent", spec.Expr), nil, actual), nil
	}
	passed, msg := compare(op, actual, spec.Value)
	return result(a.Type(), spec.Description, passed,
		fmt.Sprintf("header %q: %s", spec.Expr, msg), spec.Value, actual), nil
}

// evalJSONPath compiles and evaluates a JSONPath expression. Returns the value,
// whether it matched, and any error. Supports both "$.foo" and "foo".
func evalJSONPath(body []byte, expr string) (any, bool, error) {
	v, err := parseJSON(body)
	if err != nil {
		return nil, false, fmt.Errorf("parse JSON: %w", err)
	}
	return jsonq.Get(v, expr)
}
