package model

import (
	"bytes"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MergeYAMLNode merges next into cur, field by field, and returns the node
// to keep in cur's place (cur itself unless the kinds differ). Untouched
// keys keep their order, comments, and scalar style; keys next adds are
// appended in next's order. A key of cur that next lacks is removed only
// when known (cur's value as the model encodes it) has it, so keys the
// model does not know survive an edit. known nil means every key is known
// (a free-form map such as a request).
func MergeYAMLNode(cur, next, known *yaml.Node) *yaml.Node {
	if cur == nil {
		return next
	}
	if next == nil {
		return cur
	}
	if cur.Kind != next.Kind || cur.Kind == yaml.AliasNode {
		return replaceKeepingComments(cur, next)
	}
	switch cur.Kind {
	case yaml.ScalarNode:
		if cur.Value == next.Value && cur.ShortTag() == next.ShortTag() {
			return cur
		}
		if cur.ShortTag() == "!!str" && next.ShortTag() == "!!str" {
			switch {
			case cur.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0:
				next.Style = cur.Style
			case cur.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 && strings.Contains(next.Value, "\n"):
				// Folded style can drift on re-encoding; literal holds the
				// same multi-line text exactly.
				next.Style = yaml.LiteralStyle
			}
		}
		return replaceKeepingComments(cur, next)
	case yaml.MappingNode:
		nextVals := map[string]*yaml.Node{}
		var nextKeys []*yaml.Node
		for i := 0; i+1 < len(next.Content); i += 2 {
			nextVals[next.Content[i].Value] = next.Content[i+1]
			nextKeys = append(nextKeys, next.Content[i])
		}
		knownVals := map[string]*yaml.Node{}
		if known != nil && known.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(known.Content); i += 2 {
				knownVals[known.Content[i].Value] = known.Content[i+1]
			}
		}
		seen := map[string]bool{}
		out := make([]*yaml.Node, 0, len(cur.Content))
		for i := 0; i+1 < len(cur.Content); i += 2 {
			k, v := cur.Content[i], cur.Content[i+1]
			seen[k.Value] = true
			nv, inNext := nextVals[k.Value]
			if !inNext {
				_, isKnown := knownVals[k.Value]
				if known == nil || isKnown {
					continue // removed by the edit
				}
				out = append(out, k, v) // a key the model does not know
				continue
			}
			var kv *yaml.Node
			if known != nil {
				kv = knownVals[k.Value]
			}
			out = append(out, k, MergeYAMLNode(v, nv, kv))
		}
		for _, k := range nextKeys {
			if !seen[k.Value] {
				out = append(out, k, nextVals[k.Value])
			}
		}
		cur.Content = out
		return cur
	case yaml.SequenceNode:
		out := make([]*yaml.Node, 0, len(next.Content))
		for i, nv := range next.Content {
			if i < len(cur.Content) {
				var kv *yaml.Node
				if known != nil && known.Kind == yaml.SequenceNode && i < len(known.Content) {
					kv = known.Content[i]
				}
				out = append(out, MergeYAMLNode(cur.Content[i], nv, kv))
			} else {
				out = append(out, nv)
			}
		}
		cur.Content = out
		return cur
	}
	return replaceKeepingComments(cur, next)
}

func replaceKeepingComments(cur, next *yaml.Node) *yaml.Node {
	if next.HeadComment == "" {
		next.HeadComment = cur.HeadComment
	}
	if next.LineComment == "" {
		next.LineComment = cur.LineComment
	}
	if next.FootComment == "" {
		next.FootComment = cur.FootComment
	}
	return next
}

// SpliceYAMLItems writes an edited YAML tree by re-encoding only items
// (block-sequence entries that still carry their original position) and
// splicing that text into original, so every other byte of the file stays
// as it was: quotes, blank lines, folded scalars, and comments. When an
// item cannot be located safely, or the spliced text would not decode to
// the same document (values and comments) as a full re-encoding, it falls
// back to FormatYAMLNode for the whole file. spliced reports which way was
// taken.
func SpliceYAMLItems(original []byte, doc *yaml.Node, items []*yaml.Node) (out []byte, spliced bool, err error) {
	candidate, ok := spliceItems(original, doc, items)
	full, err := FormatYAMLNode(original, deepCopyYAML(doc))
	if err != nil {
		return nil, false, err
	}
	if ok && sameYAMLDocument(candidate, full) {
		return candidate, true, nil
	}
	return full, false, nil
}

func spliceItems(original []byte, doc *yaml.Node, items []*yaml.Node) ([]byte, bool) {
	if len(items) == 0 || bytes.Contains(original, []byte("\r")) {
		return nil, false
	}
	text := string(original)
	trailingNL := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	sorted := append([]*yaml.Node(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Line > sorted[j].Line })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Line == sorted[i-1].Line {
			return nil, false
		}
	}
	for _, item := range sorted {
		if item.Line < 1 || item.Line > len(lines) || item.Kind != yaml.MappingNode || item.Style&yaml.FlowStyle != 0 {
			return nil, false
		}
		// The item must start on its dash line: "  - key: value".
		line := lines[item.Line-1]
		col := item.Column - 1
		if col < 2 || col > len(line) {
			return nil, false
		}
		prefix := strings.TrimRight(line[:col], " ")
		if !strings.HasSuffix(prefix, "-") || strings.TrimSpace(prefix[:len(prefix)-1]) != "" {
			return nil, false
		}
		dash := len(prefix) - 1

		// The item ends before the next node outside it, less trailing
		// blank and comment lines (they stay where they are).
		inside := map[*yaml.Node]bool{}
		markYAML(item, inside)
		end := len(lines) // exclusive, 0-based
		walkYAML(doc, func(n *yaml.Node) {
			if !inside[n] && n.Line > item.Line && n.Line-1 < end {
				end = n.Line - 1
			}
		})
		for i := item.Line; i < end; i++ { // document markers end the item too
			if l := strings.TrimRight(lines[i], " "); l == "..." || l == "---" || strings.HasPrefix(l, "... ") || strings.HasPrefix(l, "--- ") {
				end = i
				break
			}
		}
		for end > item.Line {
			t := strings.TrimSpace(lines[end-1])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			end--
		}

		enc, ok := encodeItem(item)
		if !ok {
			return nil, false
		}
		pad := strings.Repeat(" ", dash)
		var repl []string
		for _, l := range strings.Split(strings.TrimSuffix(enc, "\n"), "\n") {
			if l == "" {
				repl = append(repl, l)
			} else {
				repl = append(repl, pad+l)
			}
		}
		next := append([]string{}, lines[:item.Line-1]...)
		next = append(next, repl...)
		lines = append(next, lines[end:]...)
	}
	out := strings.Join(lines, "\n")
	if trailingNL {
		out += "\n"
	}
	return []byte(out), true
}

// encodeItem encodes one sequence entry as "- key: ..." text. Comments
// above the item and after it stay in the original text, so they are not
// emitted again. A folded scalar that would not survive re-encoding is
// written in literal style.
func encodeItem(item *yaml.Node) (string, bool) {
	cp := deepCopyYAML(item)
	cp.HeadComment, cp.FootComment = "", ""
	enc := func(n *yaml.Node) (string, error) {
		var buf bytes.Buffer
		e := yaml.NewEncoder(&buf)
		e.SetIndent(2)
		if err := e.Encode(&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{n}}); err != nil {
			return "", err
		}
		if err := e.Close(); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
	s, err := enc(cp)
	if err != nil {
		return "", false
	}
	var want, got any
	if item.Decode(&want) != nil {
		return "", false
	}
	var back []any
	if yaml.Unmarshal([]byte(s), &back) != nil || len(back) != 1 {
		return "", false
	}
	got = back[0]
	if reflect.DeepEqual(want, got) {
		return s, true
	}
	normalizeFoldedScalars(cp)
	if s, err = enc(cp); err != nil {
		return "", false
	}
	return s, true
}

func walkYAML(n *yaml.Node, fn func(*yaml.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Content {
		walkYAML(c, fn)
	}
}

func markYAML(n *yaml.Node, set map[*yaml.Node]bool) {
	walkYAML(n, func(c *yaml.Node) { set[c] = true })
}

func deepCopyYAML(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.Content = make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		cp.Content[i] = deepCopyYAML(c)
	}
	return &cp
}

// sameYAMLDocument reports whether a and b decode to the same values and
// carry the same comments (as a multiset of comment lines).
func sameYAMLDocument(a, b []byte) bool {
	var va, vb any
	if yaml.Unmarshal(a, &va) != nil || yaml.Unmarshal(b, &vb) != nil {
		return false
	}
	if !reflect.DeepEqual(va, vb) {
		return false
	}
	var na, nb yaml.Node
	if yaml.Unmarshal(a, &na) != nil || yaml.Unmarshal(b, &nb) != nil {
		return false
	}
	return reflect.DeepEqual(yamlComments(&na), yamlComments(&nb))
}

func yamlComments(doc *yaml.Node) map[string]int {
	out := map[string]int{}
	walkYAML(doc, func(n *yaml.Node) {
		for _, c := range []string{n.HeadComment, n.LineComment, n.FootComment} {
			for _, l := range strings.Split(c, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					out[l]++
				}
			}
		}
	})
	return out
}
