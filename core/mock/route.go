package mock

import "strings"

// seg is one segment of a path template. A parameter matches exactly one
// non-empty segment.
type seg struct {
	lit   string
	name  string
	param bool
}

func parseTemplate(p string) []seg {
	if p == "" {
		p = "/"
	}
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	segs := make([]seg, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && len(part) > 2 {
			segs = append(segs, seg{param: true, name: part[1 : len(part)-1]})
			continue
		}
		segs = append(segs, seg{lit: part})
	}
	return segs
}

func matchPath(segs []seg, reqPath string) (map[string]string, bool) {
	if reqPath == "" {
		reqPath = "/"
	}
	if !strings.HasPrefix(reqPath, "/") {
		return nil, false
	}
	trimmed := strings.TrimPrefix(reqPath, "/")
	var parts []string
	if trimmed != "" {
		parts = strings.Split(trimmed, "/")
	}
	// Drop empty pieces from a trailing slash so "/widgets/" is not a
	// different route from "/widgets".
	clean := parts[:0]
	for _, part := range parts {
		if part != "" {
			clean = append(clean, part)
		}
	}
	parts = clean
	if len(parts) != len(segs) {
		return nil, false
	}
	params := make(map[string]string, len(segs))
	for i, s := range segs {
		if s.param {
			params[s.name] = parts[i]
			continue
		}
		if parts[i] != s.lit {
			return nil, false
		}
	}
	return params, true
}

// specificity ranks a template. A static segment outranks a parameter, and
// a longer match outranks a shorter one when the static counts are equal.
func specificity(segs []seg) int {
	static := 0
	for _, s := range segs {
		if !s.param {
			static++
		}
	}
	return static*1000 + len(segs)
}
