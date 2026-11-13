// Package jsonnum decodes JSON response bodies without losing number
// precision and compares numbers exactly.
//
// Decode keeps a number as float64 when float64 prints back as the same
// decimal value (so JSONPath filters such as [?(@.id == 1)] keep working),
// and as json.Number when float64 would change it (for example the 64-bit id
// 9007199254740993).
package jsonnum

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Decode parses one JSON document. Trailing non-whitespace is an error.
func Decode(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid character after top-level value")
	}
	return normalize(v), nil
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalize(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalize(e)
		}
		return t
	case json.Number:
		if f, ok := losslessFloat(t); ok {
			return f
		}
		return t
	}
	return v
}

// losslessFloat returns n as float64 when the float's shortest decimal form
// has exactly n's value.
func losslessFloat(n json.Number) (float64, bool) {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, false
	}
	want, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return 0, false
	}
	got, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	if !ok {
		return 0, false
	}
	return f, want.Cmp(got) == 0
}

// IsNumber reports whether v is a numeric Go or JSON type (not a string).
func IsNumber(v any) bool {
	switch v.(type) {
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

// Rat returns v as an exact rational. Numeric types and numeric strings
// (decimal or exponent form) convert; floats use their shortest decimal form,
// so 0.1 is exactly 1/10. NaN, infinities, and other values do not convert.
func Rat(v any) (*big.Rat, bool) {
	switch t := v.(type) {
	case json.Number:
		return parseRat(string(t))
	case string:
		return parseRat(strings.TrimSpace(t))
	case float64:
		return floatRat(t)
	case float32:
		return floatRat(float64(t))
	case int:
		return new(big.Rat).SetInt64(int64(t)), true
	case int8:
		return new(big.Rat).SetInt64(int64(t)), true
	case int16:
		return new(big.Rat).SetInt64(int64(t)), true
	case int32:
		return new(big.Rat).SetInt64(int64(t)), true
	case int64:
		return new(big.Rat).SetInt64(t), true
	case uint:
		return new(big.Rat).SetUint64(uint64(t)), true
	case uint8:
		return new(big.Rat).SetUint64(uint64(t)), true
	case uint16:
		return new(big.Rat).SetUint64(uint64(t)), true
	case uint32:
		return new(big.Rat).SetUint64(uint64(t)), true
	case uint64:
		return new(big.Rat).SetUint64(t), true
	}
	return nil, false
}

func floatRat(f float64) (*big.Rat, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	return new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
}

// parseRat accepts what strconv.ParseFloat accepts as a finite decimal number
// (not fractions such as "1/3", hex, NaN, or Inf).
func parseRat(s string) (*big.Rat, bool) {
	if s == "" || strings.ContainsAny(s, "/xXpP_") {
		return nil, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, false
	}
	if math.IsNaN(f) || (math.IsInf(f, 0) && err == nil) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// String renders a decoded JSON value as text: json.Number keeps its literal,
// floats use their shortest decimal form, and nil is "".
func String(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
