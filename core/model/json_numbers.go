package model

import (
	"encoding/json"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// JSONNumber retains a JSON numeric token through JSON and YAML serialization.
type JSONNumber string

func (n JSONNumber) MarshalJSON() ([]byte, error) { return []byte(n), nil }
func (n JSONNumber) MarshalYAML() (any, error) {
	return yamlNumberNode(string(n)), nil
}

func yamlNumberNode(value string) *yaml.Node {
	tag := "!!int"
	if strings.ContainsAny(value, ".eE") {
		tag = "!!float"
	}
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	if tag == "!!float" {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			node.Style = yaml.TaggedStyle
		}
	}
	return node
}

// PreserveJSONNumbers replaces generic JSON numbers with their lossless YAML-
// and JSON-serializable representation, recursively through maps and slices.
func PreserveJSONNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		return JSONNumber(v.String())
	case map[string]any:
		for key, child := range v {
			v[key] = PreserveJSONNumbers(child)
		}
	case []any:
		for i, child := range v {
			v[i] = PreserveJSONNumbers(child)
		}
	}
	return value
}
