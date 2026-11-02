// Package assertions provides Verity's built-in assertion plugins. Every one is
// a reference implementation of the spi.Assertion interface — the host gives
// them no special treatment. They are registered via Register.
package assertions

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// compare evaluates op against actual/expected and returns pass + message.
// Supported ops: eq, ne, lt, lte, gt, gte, contains, matches, exists,
// not_exists. Numeric comparisons are attempted when both sides parse as
// numbers; otherwise string comparison is used.
func compare(op string, actual, expected any) (bool, string) {
	if op == "" {
		op = "eq"
	}
	switch op {
	case "exists":
		return actual != nil, fmt.Sprintf("expected value to exist, got %v", actual)
	case "not_exists":
		return actual == nil, fmt.Sprintf("expected value to be absent, got %v", actual)
	}

	as := toString(actual)
	es := toString(expected)

	switch op {
	case "eq":
		if af, ef, ok := toFloats(actual, expected); ok {
			return af == ef, fmt.Sprintf("expected %v == %v", actual, expected)
		}
		return as == es, fmt.Sprintf("expected %q == %q", as, es)
	case "ne":
		if af, ef, ok := toFloats(actual, expected); ok {
			return af != ef, fmt.Sprintf("expected %v != %v", actual, expected)
		}
		return as != es, fmt.Sprintf("expected %q != %q", as, es)
	case "lt", "lte", "gt", "gte":
		af, ef, ok := toFloats(actual, expected)
		if !ok {
			return false, fmt.Sprintf("op %q requires numeric values, got %q and %q", op, as, es)
		}
		switch op {
		case "lt":
			return af < ef, fmt.Sprintf("expected %v < %v", af, ef)
		case "lte":
			return af <= ef, fmt.Sprintf("expected %v <= %v", af, ef)
		case "gt":
			return af > ef, fmt.Sprintf("expected %v > %v", af, ef)
		case "gte":
			return af >= ef, fmt.Sprintf("expected %v >= %v", af, ef)
		}
	case "contains":
		return strings.Contains(as, es), fmt.Sprintf("expected %q to contain %q", as, es)
	case "matches":
		re, err := regexp.Compile(es)
		if err != nil {
			return false, fmt.Sprintf("invalid regex %q: %v", es, err)
		}
		return re.MatchString(as), fmt.Sprintf("expected %q to match /%s/", as, es)
	}
	return false, fmt.Sprintf("unknown operator %q", op)
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func toFloats(a, b any) (float64, float64, bool) {
	af, ok1 := toFloat(a)
	bf, ok2 := toFloat(b)
	return af, bf, ok1 && ok2
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		// JSON projects decode numbers with UseNumber to keep them exact.
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
