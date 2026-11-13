package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// JSON project edits work on the ordered tree FormatJSON uses, so keys keep
// their order, keys the model does not know survive, number text is kept,
// and nothing is HTML-escaped. Output is normalized like verity fmt.

func parseOrderedJSON(data []byte) (*orderedJSON, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := decodeOrderedJSON(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("multiple JSON values")
	}
	return n, nil
}

func orderedOf(v any) (*orderedJSON, error) {
	b, err := MarshalJSONIndent(v)
	if err != nil {
		return nil, err
	}
	return parseOrderedJSON(b)
}

func (n *orderedJSON) bytes() []byte {
	var buf bytes.Buffer
	n.encode(&buf, 0)
	return buf.Bytes()
}

func (n *orderedJSON) field(key string) *orderedJSON {
	if n == nil || n.kind != '{' {
		return nil
	}
	for _, p := range n.pairs {
		if p.key == key {
			return p.value
		}
	}
	return nil
}

func (n *orderedJSON) set(key string, v *orderedJSON) {
	for i, p := range n.pairs {
		if p.key == key {
			n.pairs[i].value = v
			return
		}
	}
	n.pairs = append(n.pairs, orderedJSONPair{key: key, value: v})
}

func (n *orderedJSON) del(key string) {
	for i, p := range n.pairs {
		if p.key == key {
			n.pairs = append(n.pairs[:i], n.pairs[i+1:]...)
			return
		}
	}
}

func (n *orderedJSON) str() string {
	if n == nil || n.kind != 's' {
		return ""
	}
	var s string
	_ = json.Unmarshal([]byte(n.value), &s)
	return s
}

// mergeJSON is MergeYAMLNode for the ordered JSON tree.
func mergeJSON(cur, next, known *orderedJSON) *orderedJSON {
	if cur == nil {
		return next
	}
	if next == nil || cur.kind != next.kind {
		return next
	}
	switch cur.kind {
	case '{':
		seen := map[string]bool{}
		var out []orderedJSONPair
		for _, p := range cur.pairs {
			seen[p.key] = true
			nv := next.field(p.key)
			if nv == nil {
				if known == nil || known.field(p.key) != nil {
					continue
				}
				out = append(out, p)
				continue
			}
			var kv *orderedJSON
			if known != nil {
				kv = known.field(p.key)
			}
			out = append(out, orderedJSONPair{key: p.key, value: mergeJSON(p.value, nv, kv)})
		}
		for _, p := range next.pairs {
			if !seen[p.key] {
				out = append(out, p)
			}
		}
		cur.pairs = out
		return cur
	case '[':
		out := make([]*orderedJSON, 0, len(next.values))
		for i, nv := range next.values {
			if i < len(cur.values) {
				var kv *orderedJSON
				if known != nil && known.kind == '[' && i < len(known.values) {
					kv = known.values[i]
				}
				out = append(out, mergeJSON(cur.values[i], nv, kv))
			} else {
				out = append(out, nv)
			}
		}
		cur.values = out
		return cur
	}
	if cur.value == next.value {
		return cur
	}
	return next
}

// knownStep is old re-encoded through the model: the keys the model knows.
func knownStep(old *orderedJSON) *orderedJSON {
	dec := json.NewDecoder(bytes.NewReader(old.bytes()))
	dec.UseNumber()
	var st Step
	if dec.Decode(&st) != nil {
		return nil
	}
	k, err := orderedOf(st)
	if err != nil {
		return nil
	}
	return k
}

// ReplaceJSONStep merges step into suites[suite].cases[kase].steps[index] of
// a JSON project file. found is false when that case is not in the file.
func ReplaceJSONStep(data []byte, suite, kase string, index int, step Step) (out []byte, found bool, err error) {
	root, err := parseOrderedJSON(data)
	if err != nil {
		return nil, false, err
	}
	for _, s := range root.field("suites").listValues() {
		if s.field("name").str() != suite {
			continue
		}
		for _, c := range s.field("cases").listValues() {
			if c.field("name").str() != kase {
				continue
			}
			steps := c.field("steps")
			if steps == nil || steps.kind != '[' {
				continue
			}
			if index < 0 || index >= len(steps.values) {
				return nil, false, fmt.Errorf("step index %d out of range (case has %d steps)", index, len(steps.values))
			}
			next, err := orderedOf(step)
			if err != nil {
				return nil, false, err
			}
			old := steps.values[index]
			steps.values[index] = mergeJSON(old, next, knownStep(old))
			return append(root.bytes(), '\n'), true, nil
		}
	}
	return nil, false, nil
}

// EditJSONEnvironments applies an environment save to a JSON project file:
// it replaces the variables of the environment called name (keeping the
// order of variables that stay), appends it when create is set, and when
// def is set makes it the only default (true) or not default (false). out is
// nil when the file needs no change; found is true when the environment is
// in this file.
func EditJSONEnvironments(data []byte, name string, vars map[string]string, def *bool, create bool) (out []byte, found bool, err error) {
	root, err := parseOrderedJSON(data)
	if err != nil {
		return nil, false, err
	}
	if root.kind != '{' {
		return nil, false, nil
	}
	envs := root.field("environments")
	changed := false
	if create {
		if envs == nil || envs.kind != '[' {
			envs = &orderedJSON{kind: '['}
			root.set("environments", envs)
		}
		n, err := orderedOf(Environment{Name: name, Variables: vars})
		if err != nil {
			return nil, false, err
		}
		envs.values = append(envs.values, n)
		changed, found = true, true
	}
	for i, e := range envs.listValues() {
		envName := e.field("name").str()
		if envName == name {
			found = true
			if !create || i < len(envs.values)-1 {
				next, err := orderedOf(vars) // map keys encode sorted
				if err != nil {
					return nil, false, err
				}
				if len(vars) == 0 {
					e.del("variables") // omitted when empty, as the model writes it
				} else {
					e.set("variables", mergeJSON(e.field("variables"), next, nil))
				}
				changed = true
			}
		}
		if def != nil {
			want := envName == name && *def
			current := e.field("default") != nil && e.field("default").value == "true"
			if current != want {
				if want {
					e.set("default", &orderedJSON{kind: 'n', value: "true"})
				} else {
					e.del("default")
				}
				changed = true
			}
		}
	}
	if !changed {
		return nil, found, nil
	}
	return append(root.bytes(), '\n'), found, nil
}

func (n *orderedJSON) listValues() []*orderedJSON {
	if n == nil || n.kind != '[' {
		return nil
	}
	return n.values
}
