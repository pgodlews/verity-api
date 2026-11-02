package spi

import (
	"net/url"
	"strings"
)

// MergeQuery merges auth-added extra parameters into a raw request query.
// With no extra parameters it returns raw unchanged. Otherwise the query is
// re-encoded once, sorted by key, with spaces as %20 rather than '+'. Callers
// apply EscapeQuery to the result so a signer and the sender share the wire form.
func MergeQuery(raw string, extra map[string]string) string {
	if len(extra) == 0 {
		return raw
	}
	vals, _ := url.ParseQuery(raw) // like url.URL.Query: invalid pairs are dropped
	for k, v := range extra {
		vals.Set(k, v)
	}
	// Values.Encode escapes a literal '+' as %2B, so each '+' it emits is a space.
	return strings.ReplaceAll(vals.Encode(), "+", "%20")
}

// NormalizeTargetQuery percent-encodes the raw query in a request target before
// the sender or an auth signer parses it. Text after '#' is left untouched.
func NormalizeTargetQuery(target string) string {
	queryAt := strings.IndexByte(target, '?')
	fragmentAt := strings.IndexByte(target, '#')
	if queryAt < 0 || (fragmentAt >= 0 && queryAt > fragmentAt) {
		return target
	}
	end := len(target)
	if fragmentAt >= 0 {
		end = fragmentAt
	}
	return target[:queryAt+1] + EscapeQuery(target[queryAt+1:end]) + target[end:]
}

// EscapeQuery percent-encodes bytes that cannot appear literally in an HTTP
// request-target query. Existing %HH sequences and all other bytes stay as written.
func EscapeQuery(raw string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		escaped := c <= 0x20 || c >= 0x7f || strings.ContainsRune(`"<>\^`+"`"+`{|}`, rune(c))
		if !escaped {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
