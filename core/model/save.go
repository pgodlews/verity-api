package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pgodlews/verity-api/core/fsutil"
)

// Save writes the project to a file. The extension selects the format: .json
// uses JSON, anything else uses YAML. Parent directories are created.
func (p *Project) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	data, err := p.Marshal(path)
	if err != nil {
		return err
	}
	return fsutil.WriteKeepingMode(path, data)
}

// MarshalJSONIndent is json.MarshalIndent with two-space indentation but
// without HTML escaping, so "<", ">", and "&" in SOAP/XML bodies and URLs
// are written as themselves rather than \u003c, \u003e, and \u0026.
func MarshalJSONIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// marshalJSONString encodes one JSON string without HTML escaping.
func marshalJSONString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// Marshal serializes the project, choosing JSON for .json paths and YAML
// otherwise.
func (p *Project) Marshal(ext string) ([]byte, error) {
	if strings.HasSuffix(strings.ToLower(ext), ".json") {
		return MarshalJSONIndent(p)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	ordered, err := decodeOrderedJSON(dec)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(orderedYAMLNode(ordered))
}

func orderedYAMLNode(n *orderedJSON) *yaml.Node {
	switch n.kind {
	case '{':
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, pair := range n.pairs {
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pair.key},
				orderedYAMLNode(pair.value))
		}
		return node
	case '[':
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, value := range n.values {
			node.Content = append(node.Content, orderedYAMLNode(value))
		}
		return node
	case 's':
		var value string
		_ = json.Unmarshal([]byte(n.value), &value)
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	default:
		switch n.value {
		case "true", "false":
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: n.value}
		case "null":
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: n.value}
		}
		return yamlNumberNode(n.value)
	}
}
