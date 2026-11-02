package importers

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/model"
)

// Postman test scripts are JavaScript, which Verity does not execute. This
// file recognises the common one-statement idioms and turns them into
// declarative assertions and transfers; every other line is reported back so
// the caller can warn about it.

const (
	jsStr   = `["'\x60]([^"'\x60]*)["'\x60]`
	jsNum   = `(\d+(?:\.\d+)?)`
	jsChain = `((?:\.[A-Za-z_$][\w$]*|\[\d+\]|\[["'][^"']+["']\])*)`
)

var (
	reTestOpen    = regexp.MustCompile(`^pm\.test\(\s*` + jsStr + `\s*,\s*(?:function\s*\(\s*\)|\(\s*\)\s*=>)\s*\{\s*$`)
	reBlockClose  = regexp.MustCompile(`^\}\s*\)?\s*;?$`)
	reJSONAlias   = regexp.MustCompile(`^(?:var|let|const)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:pm\.response\.json\(\)|JSON\.parse\(\s*(?:responseBody|pm\.response\.text\(\))\s*\))\s*;?$`)
	reStatus      = regexp.MustCompile(`^pm\.response\.to\.(?:have|be)\.status\(\s*(\d{3})\s*\)\s*;?$`)
	reStatusCode  = regexp.MustCompile(`^pm\.expect\(\s*pm\.response\.(?:code|status)\s*\)\.to\.(?:eql|equal|eq|be\.equal|be\.eql)\(\s*(\d{3})\s*\)\s*;?$`)
	reStatusOneOf = regexp.MustCompile(`^pm\.expect\(\s*pm\.response\.code\s*\)\.to\.be\.oneOf\(\s*\[([\d\s,]+)\]\s*\)\s*;?$`)
	reOK          = regexp.MustCompile(`^pm\.response\.to\.be\.(ok|success)\s*;?$`)
	reRespTime    = regexp.MustCompile(`^pm\.expect\(\s*pm\.response\.responseTime\s*\)\.to\.be\.(below|lessThan|lt|at\.most|most|lte)\(\s*` + jsNum + `\s*\)\s*;?$`)
	reHeader      = regexp.MustCompile(`^pm\.response\.to\.have\.header\(\s*` + jsStr + `\s*(?:,\s*` + jsStr + `\s*)?\)\s*;?$`)
	reBodyHas     = regexp.MustCompile(`^pm\.expect\(\s*(?:pm\.response\.text\(\)|responseBody)\s*\)\.to\.(?:include|contain|have\.string)\(\s*` + jsStr + `\s*\)\s*;?$`)
	reBodyHas2    = regexp.MustCompile(`^pm\.response\.to\.have\.body\(\s*` + jsStr + `\s*\)\s*;?$`)
	reExpectJSON  = regexp.MustCompile(`^pm\.expect\(\s*(pm\.response\.json\(\)|[A-Za-z_$][\w$]*)` + jsChain + `\s*\)\.to\.(?:(?:be\.)?(?:eql|equal|eq)\(\s*(.+?)\s*\)|(exist|not\.exist|be\.undefined|not\.be\.undefined))\s*;?$`)
	reSetVar      = regexp.MustCompile(`^(?:pm\.(?:environment|collectionVariables|globals|variables)\.set|postman\.set(?:Environment|Global)Variable)\(\s*` + jsStr + `\s*,\s*(.+?)\s*\)\s*;?$`)
	reSrcJSON     = regexp.MustCompile(`^(pm\.response\.json\(\)|[A-Za-z_$][\w$]*)` + jsChain + `$`)
	reSrcHeader   = regexp.MustCompile(`^(?:pm\.response\.headers\.get|postman\.getResponseHeader)\(\s*` + jsStr + `\s*\)$`)
	reChainPart   = regexp.MustCompile(`\.([A-Za-z_$][\w$]*)|\[(\d+)\]|\[["']([^"']+)["']\]`)
)

// scriptResult is what convertTestScript extracted from one script.
type scriptResult struct {
	Assertions  []model.Assertion
	Transfers   []model.TransferSpec
	Unconverted []string // trimmed source lines that were not understood
}

// convertTestScript translates the recognised idioms of a Postman test script.
func convertTestScript(lines []string) scriptResult {
	var res scriptResult
	aliases := map[string]bool{}
	label := ""
	for _, raw := range lines {
		line := strings.TrimSpace(stripLineComment(raw))
		if line == "" {
			continue
		}
		if m := reTestOpen.FindStringSubmatch(line); m != nil {
			label = m[1]
			continue
		}
		if reBlockClose.MatchString(line) {
			label = ""
			continue
		}
		if m := reJSONAlias.FindStringSubmatch(line); m != nil {
			aliases[m[1]] = true
			continue
		}
		if a := matchAssertion(line, aliases); len(a) > 0 {
			for i := range a {
				a[i].Description = label
			}
			res.Assertions = append(res.Assertions, a...)
			continue
		}
		if t, ok := matchTransfer(line, aliases); ok {
			res.Transfers = append(res.Transfers, t)
			continue
		}
		res.Unconverted = append(res.Unconverted, line)
	}
	return res
}

func matchAssertion(line string, aliases map[string]bool) []model.Assertion {
	if m := reStatus.FindStringSubmatch(line); m != nil {
		return []model.Assertion{{Type: "status", Value: atoi(m[1])}}
	}
	if m := reStatusCode.FindStringSubmatch(line); m != nil {
		return []model.Assertion{{Type: "status", Value: atoi(m[1])}}
	}
	if m := reStatusOneOf.FindStringSubmatch(line); m != nil {
		codes := strings.Split(m[1], ",")
		for i := range codes {
			codes[i] = strings.TrimSpace(codes[i])
		}
		return []model.Assertion{{Type: "status", Op: "matches", Value: "^(" + strings.Join(codes, "|") + ")$"}}
	}
	if m := reOK.FindStringSubmatch(line); m != nil {
		if m[1] == "ok" {
			return []model.Assertion{{Type: "status", Value: 200}}
		}
		return []model.Assertion{
			{Type: "status", Op: "gte", Value: 200},
			{Type: "status", Op: "lt", Value: 300},
		}
	}
	if m := reRespTime.FindStringSubmatch(line); m != nil {
		op := "lt"
		if strings.Contains(m[1], "most") || m[1] == "lte" {
			op = "lte"
		}
		return []model.Assertion{{Type: "response_time", Op: op, Value: jsonNumber(m[2])}}
	}
	if m := reHeader.FindStringSubmatch(line); m != nil {
		if strings.Contains(line, ",") {
			return []model.Assertion{{Type: "header", Expr: m[1], Op: "eq", Value: m[2]}}
		}
		return []model.Assertion{{Type: "header", Expr: m[1], Op: "exists"}}
	}
	if m := reBodyHas.FindStringSubmatch(line); m != nil {
		return []model.Assertion{{Type: "contains", Value: m[1]}}
	}
	if m := reBodyHas2.FindStringSubmatch(line); m != nil {
		// to.have.body is an exact match, not a substring check.
		return []model.Assertion{{Type: "regex", Value: "^" + regexp.QuoteMeta(m[1]) + "$"}}
	}
	if m := reExpectJSON.FindStringSubmatch(line); m != nil {
		if !isResponseJSON(m[1], aliases) {
			return nil
		}
		path := chainToJSONPath(m[2])
		switch m[4] {
		case "exist", "not.be.undefined":
			return []model.Assertion{{Type: "jsonpath", Expr: path, Op: "exists"}}
		case "not.exist", "be.undefined":
			return []model.Assertion{{Type: "jsonpath", Expr: path, Op: "not_exists"}}
		}
		v, ok := jsLiteral(m[3])
		if !ok {
			return nil
		}
		return []model.Assertion{{Type: "jsonpath", Expr: path, Op: "eq", Value: v}}
	}
	return nil
}

func matchTransfer(line string, aliases map[string]bool) (model.TransferSpec, bool) {
	m := reSetVar.FindStringSubmatch(line)
	if m == nil {
		return model.TransferSpec{}, false
	}
	name, src := m[1], strings.TrimSpace(m[2])
	switch src {
	case "pm.response.code", "responseCode.code":
		return model.TransferSpec{Name: name, From: "status"}, true
	case "pm.response.text()", "responseBody":
		return model.TransferSpec{Name: name, From: "body"}, true
	}
	if h := reSrcHeader.FindStringSubmatch(src); h != nil {
		return model.TransferSpec{Name: name, From: "header", Expr: h[1]}, true
	}
	if j := reSrcJSON.FindStringSubmatch(src); j != nil && j[2] != "" && isResponseJSON(j[1], aliases) {
		return model.TransferSpec{Name: name, From: "jsonpath", Expr: chainToJSONPath(j[2])}, true
	}
	return model.TransferSpec{}, false
}

func isResponseJSON(root string, aliases map[string]bool) bool {
	return root == "pm.response.json()" || aliases[root]
}

// chainToJSONPath turns a JS accessor chain (.a[0]["b c"]) into JSONPath.
func chainToJSONPath(chain string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, m := range reChainPart.FindAllStringSubmatch(chain, -1) {
		switch {
		case m[1] != "":
			b.WriteString("." + m[1])
		case m[2] != "":
			b.WriteString("[" + m[2] + "]")
		default:
			b.WriteString("['" + m[3] + "']")
		}
	}
	return b.String()
}

// jsLiteral parses a JS scalar literal (number, quoted string, boolean).
func jsLiteral(s string) (any, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '`') && s[len(s)-1] == s[0] {
		inner := s[1 : len(s)-1]
		if strings.ContainsAny(inner, "'`\\") || strings.Contains(inner, "${") {
			return nil, false
		}
		return inner, true
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	switch x := v.(type) {
	case string, bool:
		return x, true
	case float64:
		return jsonNumber(s), true
	}
	return nil, false
}

// jsonNumber keeps whole numbers as ints so generated YAML reads naturally.
func jsonNumber(s string) any {
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func atoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

// stripLineComment removes a trailing // comment that is not inside a string.
func stripLineComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			return s[:i]
		}
	}
	return s
}

// scriptLines returns the non-blank, non-comment lines of a script, used to
// decide whether a script (e.g. a pre-request script) has any content.
func scriptLines(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.TrimSpace(stripLineComment(l)) != "" {
			n++
		}
	}
	return n
}
