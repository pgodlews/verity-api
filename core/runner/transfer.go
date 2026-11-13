package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/PaesslerAG/jsonpath"
	"github.com/antchfx/xmlquery"

	"github.com/pgodlews/verity-api/core/internal/jsonnum"
	"github.com/pgodlews/verity-api/core/model"
	"github.com/pgodlews/verity-api/core/spi"
)

// extractTransfer evaluates a TransferSpec against a response, returning the
// captured string value. Supported sources: jsonpath, xpath, header, status,
// body, regex (first capture group). The Default is used when extraction
// yields nothing. When the source matches nothing the Default is returned
// with an error, so a transfer without a default fails instead of silently
// capturing an empty value.
func extractTransfer(t model.TransferSpec, resp *spi.Response) (string, error) {
	var val string
	switch t.From {
	case "jsonpath":
		v, found, err := jsonPathValue(resp.Body, t.Expr)
		if err != nil {
			return t.Default, err
		}
		if !found {
			return t.Default, notFound(t)
		}
		val = jsonnum.String(v)
	case "xpath":
		doc, err := xmlquery.Parse(strings.NewReader(string(resp.Body)))
		if err != nil {
			return t.Default, fmt.Errorf("parse XML for transfer: %w", err)
		}
		node, err := xmlquery.Query(doc, t.Expr)
		if err != nil {
			return t.Default, fmt.Errorf("xpath %q: %w", t.Expr, err)
		}
		if node == nil {
			return t.Default, notFound(t)
		}
		val = node.InnerText()
	case "header":
		if len(resp.Headers.Values(t.Expr)) == 0 {
			return t.Default, notFound(t)
		}
		val = resp.Headers.Get(t.Expr)
	case "status":
		val = strconv.Itoa(resp.StatusCode)
	case "body":
		val = string(resp.Body)
	case "regex":
		re, err := regexp.Compile(t.Expr)
		if err != nil {
			return t.Default, fmt.Errorf("invalid transfer regex %q: %w", t.Expr, err)
		}
		m := re.FindStringSubmatch(string(resp.Body))
		if m == nil {
			return t.Default, notFound(t)
		}
		if len(m) > 1 {
			val = m[1]
		} else if len(m) == 1 {
			val = m[0]
		}
	default:
		return t.Default, fmt.Errorf("unknown transfer source %q", t.From)
	}
	if val == "" {
		return t.Default, nil
	}
	return val, nil
}

// notFound reports a transfer source that matched nothing.
func notFound(t model.TransferSpec) error {
	return fmt.Errorf("transfer %q: %s %q matched nothing (set a default to allow this)", t.Name, t.From, t.Expr)
}

func jsonPathValue(body []byte, expr string) (any, bool, error) {
	v, err := jsonnum.Decode(body)
	if err != nil {
		return nil, false, fmt.Errorf("parse JSON for transfer: %w", err)
	}
	if expr != "" && !strings.HasPrefix(expr, "$") {
		expr = "$." + expr
	}
	res, err := jsonpath.Get(expr, v)
	if err != nil {
		if strings.Contains(err.Error(), "unknown key") || strings.Contains(err.Error(), "no match") {
			return nil, false, nil
		}
		return nil, false, err
	}
	return res, true, nil
}
