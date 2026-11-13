// Package scalar converts decoded YAML/JSON configuration values to the text
// sent on the wire. It exists so a null value never becomes Go's "<nil>".
package scalar

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// String renders v as text: nil is "", strings are unchanged, numbers and
// booleans use their literal form (no exponent for whole floats), and a map
// or slice becomes compact JSON.
func String(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", t)
	case map[string]any, []any, map[string]string, []string:
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(t); err == nil {
			return strings.TrimSuffix(b.String(), "\n")
		}
	}
	return fmt.Sprintf("%v", v)
}
