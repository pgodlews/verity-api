package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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
	return os.WriteFile(path, data, 0o644)
}

// Marshal serializes the project, choosing JSON for .json paths and YAML
// otherwise.
func (p *Project) Marshal(ext string) ([]byte, error) {
	if strings.HasSuffix(strings.ToLower(ext), ".json") {
		return json.MarshalIndent(p, "", "  ")
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
