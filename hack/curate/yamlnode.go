package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Helpers over yaml.v3 nodes. The generator works on nodes, not on Go maps,
// so the fleet comments survive into the generated values.yaml.

func loadYAMLFile(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseYAML(raw, path)
}

func parseYAML(raw []byte, source string) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if document.Kind == 0 {
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{newMapping()}}, nil
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level must be a mapping", source)
	}
	return &document, nil
}

func rootMapping(document *yaml.Node) *yaml.Node { return document.Content[0] }

func newMapping() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }

func newScalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func newBool(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", value)}
}

func mappingKeys(mapping *yaml.Node) []string {
	keys := make([]string, 0, len(mapping.Content)/2)
	for i := 0; i < len(mapping.Content); i += 2 {
		keys = append(keys, mapping.Content[i].Value)
	}
	return keys
}

// mappingGet returns the key and value nodes for key, or nil when absent.
func mappingGet(mapping *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}
	return nil, nil
}

func mappingSet(mapping *yaml.Node, key *yaml.Node, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key.Value {
			mapping.Content[i] = key
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, key, value)
}

// mappingDelete removes key and returns its key and value nodes.
func mappingDelete(mapping *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			keyNode, valueNode := mapping.Content[i], mapping.Content[i+1]
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return keyNode, valueNode
		}
	}
	return nil, nil
}

// pathGet resolves a two-level dotted path such as "kagent.enabled".
func pathGet(mapping *yaml.Node, path string) (*yaml.Node, error) {
	first, rest, found := strings.Cut(path, ".")
	_, value := mappingGet(mapping, first)
	if value == nil {
		return nil, fmt.Errorf("path %q: key %q not found", path, first)
	}
	if !found {
		return value, nil
	}
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("path %q: %q is not a mapping", path, first)
	}
	return pathGet(value, rest)
}

func scalarBool(node *yaml.Node, what string) (bool, error) {
	if node.Kind != yaml.ScalarNode {
		return false, fmt.Errorf("%s is not a scalar", what)
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return false, fmt.Errorf("%s is not a bool: %w", what, err)
	}
	return value, nil
}

func scalarString(node *yaml.Node, what string) (string, error) {
	if node == nil {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s is not a scalar", what)
	}
	return node.Value, nil
}

// deepMerge merges overlay into base. Mappings merge recursively, any other
// node from the overlay replaces the base node.
func deepMerge(base *yaml.Node, overlay *yaml.Node) {
	for i := 0; i+1 < len(overlay.Content); i += 2 {
		key, value := overlay.Content[i], overlay.Content[i+1]
		_, existing := mappingGet(base, key.Value)
		if existing != nil && existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			deepMerge(existing, value)
			continue
		}
		mappingSet(base, key, value)
	}
}

func encodeYAML(document *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return spaceInlineComments(out.Bytes()), nil
}
