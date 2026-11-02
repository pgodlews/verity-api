package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/verr"
)

// FormatYAML normalizes a YAML document with two-space indentation while
// preserving mapping order, scalar values, comments, and document markers.
func FormatYAML(data []byte) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "parse YAML")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, verr.New(verr.CodeConfig, "format YAML: multiple documents are not supported")
		}
		return nil, verr.Wrap(verr.CodeConfig, err, "parse YAML")
	}

	normalizeFoldedScalars(&doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "format YAML")
	}
	if err := enc.Close(); err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "format YAML")
	}
	out := buf.Bytes()
	if hasYAMLMarker(data, "---") && !bytes.HasPrefix(out, []byte("---\n")) {
		out = append([]byte("---\n"), out...)
	}
	if hasYAMLMarker(data, "...") && !bytes.HasSuffix(out, []byte("...\n")) {
		out = append(out, []byte("...\n")...)
	}
	return out, nil
}

// yaml.v3 can add a blank line each time a folded scalar is decoded and
// encoded. Literal style represents the same string without that drift.
func normalizeFoldedScalars(n *yaml.Node) {
	if n.Style&yaml.FoldedStyle != 0 {
		n.Style = n.Style&^yaml.FoldedStyle | yaml.LiteralStyle
	}
	for _, child := range n.Content {
		normalizeFoldedScalars(child)
	}
}

func hasYAMLMarker(data []byte, marker string) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == marker || strings.HasPrefix(line, marker+" #") {
			return true
		}
	}
	return false
}

// FormatJSON normalizes JSON with two-space indentation and a trailing
// newline. Its ordered representation preserves mapping order and number text.
func FormatJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := decodeOrderedJSON(dec)
	if err != nil {
		return nil, verr.Wrap(verr.CodeConfig, err, "parse JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, verr.New(verr.CodeConfig, "parse JSON: multiple values")
		}
		return nil, verr.Wrap(verr.CodeConfig, err, "parse JSON")
	}
	var buf bytes.Buffer
	n.encode(&buf, 0)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

type orderedJSON struct {
	kind   byte
	value  string
	pairs  []orderedJSONPair
	values []*orderedJSON
}

type orderedJSONPair struct {
	key   string
	value *orderedJSON
}

func decodeOrderedJSON(dec *json.Decoder) (*orderedJSON, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			n := &orderedJSON{kind: '{'}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is %T, want string", keyToken)
				}
				value, err := decodeOrderedJSON(dec)
				if err != nil {
					return nil, err
				}
				n.pairs = append(n.pairs, orderedJSONPair{key: key, value: value})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		case '[':
			n := &orderedJSON{kind: '['}
			for dec.More() {
				value, err := decodeOrderedJSON(dec)
				if err != nil {
					return nil, err
				}
				n.values = append(n.values, value)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return n, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter %q", v)
		}
	case string:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return &orderedJSON{kind: 's', value: string(encoded)}, nil
	case json.Number:
		return &orderedJSON{kind: 'n', value: v.String()}, nil
	case bool:
		return &orderedJSON{kind: 'n', value: fmt.Sprint(v)}, nil
	case nil:
		return &orderedJSON{kind: 'n', value: "null"}, nil
	default:
		return nil, fmt.Errorf("unexpected JSON token %T", tok)
	}
}

func (n *orderedJSON) encode(buf *bytes.Buffer, depth int) {
	switch n.kind {
	case '{':
		if len(n.pairs) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteString("{\n")
		for i, pair := range n.pairs {
			writeJSONIndent(buf, depth+1)
			key, _ := json.Marshal(pair.key)
			buf.Write(key)
			buf.WriteString(": ")
			pair.value.encode(buf, depth+1)
			if i+1 < len(n.pairs) {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		writeJSONIndent(buf, depth)
		buf.WriteByte('}')
	case '[':
		if len(n.values) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteString("[\n")
		for i, value := range n.values {
			writeJSONIndent(buf, depth+1)
			value.encode(buf, depth+1)
			if i+1 < len(n.values) {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		writeJSONIndent(buf, depth)
		buf.WriteByte(']')
	default:
		buf.WriteString(n.value)
	}
}

func writeJSONIndent(buf *bytes.Buffer, depth int) {
	buf.WriteString(strings.Repeat("  ", depth))
}
