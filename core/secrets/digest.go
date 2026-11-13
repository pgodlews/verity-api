package secrets

import "strings"

type digestMaskSpan struct{ start, end int }

// maskDigestResponses parses Digest auth parameters rather than searching for
// response= inside arbitrary text. JSON-escaped quotes are normalized with an
// index map so only the original hash bytes are replaced.
func maskDigestResponses(s string) string {
	// nil positions means identity: no index map is allocated unless the
	// JSON-escaped form has to be normalized.
	spans := digestResponseSpans(s, nil)
	// Only JSON-escaped text (\") needs the normalized, index-mapped pass.
	if len(spans) == 0 && strings.Contains(s, `\"`) {
		normalized, mapped := digestNormalizedQuotes(s)
		spans = digestResponseSpans(normalized, mapped)
	}
	if len(spans) == 0 {
		return s
	}
	var out strings.Builder
	last := 0
	for _, span := range spans {
		if span.start < last {
			continue
		}
		out.WriteString(s[last:span.start])
		out.WriteString(Mask)
		last = span.end
	}
	out.WriteString(s[last:])
	return out.String()
}

func digestResponseSpans(s string, positions []int) []digestMaskSpan {
	lower := strings.ToLower(s)
	var spans []digestMaskSpan
	for at := 0; at < len(s); {
		i := strings.Index(lower[at:], "digest")
		if i < 0 {
			break
		}
		i += at
		at = i + len("digest")
		if (i > 0 && digestTokenByte(s[i-1])) || (at < len(s) && digestTokenByte(s[at])) {
			continue
		}
		start, end, ok := digestResponseValue(s, at)
		if ok {
			if positions == nil {
				spans = append(spans, digestMaskSpan{start, end})
			} else {
				spans = append(spans, digestMaskSpan{positions[start], positions[end]})
			}
		}
	}
	return spans
}

func digestTokenByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func digestNormalizedQuotes(s string) (string, []int) {
	var out strings.Builder
	out.Grow(len(s))
	positions := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '"' {
			out.WriteByte('"')
			positions = append(positions, i+1)
			i++
			continue
		}
		out.WriteByte(s[i])
		positions = append(positions, i)
	}
	positions = append(positions, len(s))
	return out.String(), positions
}

func digestResponseValue(s string, at int) (int, int, bool) {
	for at < len(s) && s[at] != '\r' && s[at] != '\n' {
		for at < len(s) && (s[at] == ' ' || s[at] == '\t' || s[at] == ',') {
			at++
		}
		keyStart := at
		for at < len(s) && digestTokenByte(s[at]) {
			at++
		}
		if keyStart == at {
			return 0, 0, false
		}
		key := s[keyStart:at]
		for at < len(s) && (s[at] == ' ' || s[at] == '\t') {
			at++
		}
		if at >= len(s) || s[at] != '=' {
			return 0, 0, false
		}
		at++
		for at < len(s) && (s[at] == ' ' || s[at] == '\t') {
			at++
		}
		if at >= len(s) || s[at] != '"' {
			return 0, 0, false
		}
		at++
		valueStart := at
		for at < len(s) {
			if s[at] == '\\' && at+1 < len(s) {
				at += 2
				continue
			}
			if s[at] == '"' {
				break
			}
			at++
		}
		if at >= len(s) {
			return 0, 0, false
		}
		valueEnd := at
		at++
		if strings.EqualFold(key, "response") && digestResponseHash(s[valueStart:valueEnd]) {
			return valueStart, valueEnd, true
		}
		for at < len(s) && (s[at] == ' ' || s[at] == '\t') {
			at++
		}
		if at < len(s) && s[at] != ',' {
			return 0, 0, false
		}
	}
	return 0, 0, false
}

func digestResponseHash(s string) bool {
	if len(s) != 32 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune("0123456789abcdefABCDEF", rune(s[i])) {
			return false
		}
	}
	return true
}
