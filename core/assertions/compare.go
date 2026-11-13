// Package assertions provides Verity's built-in assertion plugins. Every one is
// a reference implementation of the spi.Assertion interface — the host gives
// them no special treatment. They are registered via Register.
package assertions

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/pgodlews/verity-api/core/internal/jsonnum"
)

// compare evaluates op against actual/expected and returns pass + message.
// Supported ops: eq, ne, lt, lte, gt, gte, contains, not_contains, matches,
// not_matches, exists, not_exists.
//
// eq/ne compare numerically only when at least one side is a real number
// (a JSON or YAML number, not a string) and the other side parses as one;
// two strings compare as text, so "1.10" ne "1.1" and "007" ne "7". The
// ordering ops parse numeric strings too. Numeric comparison is exact (no
// float64 rounding), so 9007199254740993 ne 9007199254740992.
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
		if ar, er, ok := equalityRats(actual, expected); ok {
			return ar.Cmp(er) == 0, fmt.Sprintf("expected %s == %s", as, es)
		}
		return as == es, fmt.Sprintf("expected %q == %q", as, es)
	case "ne":
		if ar, er, ok := equalityRats(actual, expected); ok {
			return ar.Cmp(er) != 0, fmt.Sprintf("expected %s != %s", as, es)
		}
		return as != es, fmt.Sprintf("expected %q != %q", as, es)
	case "lt", "lte", "gt", "gte":
		ar, ok1 := jsonnum.Rat(actual)
		er, ok2 := jsonnum.Rat(expected)
		if !ok1 || !ok2 {
			return false, fmt.Sprintf("op %q requires numeric values, got %q and %q", op, as, es)
		}
		c := ar.Cmp(er)
		switch op {
		case "lt":
			return c < 0, fmt.Sprintf("expected %s < %s", as, es)
		case "lte":
			return c <= 0, fmt.Sprintf("expected %s <= %s", as, es)
		case "gt":
			return c > 0, fmt.Sprintf("expected %s > %s", as, es)
		case "gte":
			return c >= 0, fmt.Sprintf("expected %s >= %s", as, es)
		}
	case "contains":
		return strings.Contains(as, es), fmt.Sprintf("expected %q to contain %q", as, es)
	case "not_contains":
		return !strings.Contains(as, es), fmt.Sprintf("expected %q not to contain %q", as, es)
	case "matches", "not_matches":
		re, err := regexp.Compile(es)
		if err != nil {
			return false, fmt.Sprintf("invalid regex %q: %v", es, err)
		}
		if op == "not_matches" {
			return !re.MatchString(as), fmt.Sprintf("expected %q not to match /%s/", as, es)
		}
		return re.MatchString(as), fmt.Sprintf("expected %q to match /%s/", as, es)
	}
	return false, fmt.Sprintf("unknown operator %q", op)
}

// equalityRats returns both sides as exact numbers when eq/ne should compare
// numerically: at least one side is a number and both convert.
func equalityRats(a, b any) (*big.Rat, *big.Rat, bool) {
	if !jsonnum.IsNumber(a) && !jsonnum.IsNumber(b) {
		return nil, nil, false
	}
	ar, ok1 := jsonnum.Rat(a)
	br, ok2 := jsonnum.Rat(b)
	return ar, br, ok1 && ok2
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
