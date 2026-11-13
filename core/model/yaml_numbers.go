package model

import (
	"encoding/json"
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

var jsonNumberToken = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

func decodeYAMLProject(data []byte, p *Project) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	normalizeYAMLFloatTags(&doc)
	if len(doc.Content) > 0 {
		if err := doc.Content[0].Decode(p); err != nil {
			return err
		}
		// Reject keys the model does not define (see checkKnownFields).
		var generic any
		if err := doc.Content[0].Decode(&generic); err == nil {
			if err := checkKnownFields(generic, "yaml"); err != nil {
				return err
			}
		}
	}
	return preserveYAMLRequestNumbers(&doc, p)
}

func normalizeYAMLFloatTags(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!float" && jsonNumberToken.MatchString(n.Value) {
		if _, err := strconv.ParseFloat(n.Value, 64); err != nil {
			n.Tag = "!verity/json-number"
		}
	}
	for _, child := range n.Content {
		normalizeYAMLFloatTags(child)
	}
}

// YAML otherwise decodes interface numbers to machine numeric types. Preserve
// their source tokens wherever a project holds arbitrary JSON: the request
// json/variables of case, setup, and teardown steps, and mock json bodies.
func preserveYAMLRequestNumbers(doc *yaml.Node, p *Project) error {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	for mi, mock := range yamlSequence(yamlMapValue(root, "mocks")) {
		if mi >= len(p.Mocks) {
			break
		}
		if value := yamlMapValue(mock, "json"); value != nil {
			p.Mocks[mi].JSON = yamlJSONValue(value)
		}
	}
	for si, suite := range yamlSequence(yamlMapValue(root, "suites")) {
		if si >= len(p.Suites) {
			break
		}
		s := &p.Suites[si]
		if s.Setup != nil {
			preserveStepNumbers(yamlMapValue(yamlMapValue(suite, "setup"), "steps"), s.Setup.Steps)
		}
		if s.Teardown != nil {
			preserveStepNumbers(yamlMapValue(yamlMapValue(suite, "teardown"), "steps"), s.Teardown.Steps)
		}
		for ci, testCase := range yamlSequence(yamlMapValue(suite, "cases")) {
			if ci >= len(s.Cases) {
				break
			}
			preserveStepNumbers(yamlMapValue(testCase, "steps"), s.Cases[ci].Steps)
		}
	}
	return nil
}

// preserveStepNumbers re-reads each step's JSON-carrying request field from
// its YAML node with exact numbers.
func preserveStepNumbers(stepsNode *yaml.Node, steps []Step) {
	for sti, step := range yamlSequence(stepsNode) {
		if sti >= len(steps) {
			break
		}
		current := &steps[sti]
		if current.Request == nil {
			continue
		}
		field := ""
		switch current.Protocol {
		case "rest":
			field = "json"
		case "graphql":
			field = "variables"
		}
		if field == "" {
			continue
		}
		request := yamlMapValue(step, "request")
		if value := yamlMapValue(request, field); value != nil {
			current.Request[field] = yamlJSONValue(value)
		}
	}
}

func yamlJSONValue(n *yaml.Node) any {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		return yamlJSONValue(n.Alias)
	}
	switch n.Kind {
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		var merge func(any)
		merge = func(value any) {
			switch values := value.(type) {
			case map[string]any:
				for key, child := range values {
					if _, exists := out[key]; !exists {
						out[key] = child
					}
				}
			case []any:
				for _, child := range values {
					merge(child)
				}
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Value == "<<" && key.Tag == "!!merge" {
				merge(yamlJSONValue(n.Content[i+1]))
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Value != "<<" || key.Tag != "!!merge" {
				out[key.Value] = yamlJSONValue(n.Content[i+1])
			}
		}
		return out
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, child := range n.Content {
			out[i] = yamlJSONValue(child)
		}
		return out
	case yaml.ScalarNode:
		numericTag := n.Tag == "!!int" || n.Tag == "!!float" || n.Tag == "!verity/json-number" || n.Tag == "!!str" && n.Style == 0
		if numericTag && jsonNumberToken.MatchString(n.Value) {
			return json.Number(n.Value)
		}
	}
	var out any
	if err := n.Decode(&out); err != nil {
		return nil
	}
	return out
}

func yamlMapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func yamlSequence(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}
