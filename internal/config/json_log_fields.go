package config

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	logRequestJSON  = "request_json"
	logResponseJSON = "response_json"
	yamlStringTag   = "!!str"
)

// decodeJSONLogFields preserves alias spelling before Viper folds map keys.
func decodeJSONLogFields(raw []byte, cfg *LogFieldsConfig) error {
	var root map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("decode JSON log fields: %w", err)
	}

	seen := false

	for name, node := range root {
		if !strings.EqualFold(name, "log_fields") {
			continue
		}

		if seen {
			return fmt.Errorf("duplicate log_fields group")
		}

		seen = true

		if err := decodeJSONLogGroups(&node, cfg); err != nil {
			return err
		}
	}

	return nil
}

func decodeJSONLogGroups(node *yaml.Node, cfg *LogFieldsConfig) error {
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return fmt.Errorf("decode log_fields: %w", err)
	}

	seen := make(map[string]bool)

	for name, value := range fields {
		name = strings.ToLower(name)

		var target *map[string]string

		switch name {
		case logRequestJSON:
			target = &cfg.RequestJSON
		case logResponseJSON:
			target = &cfg.ResponseJSON
		default:
			continue
		}

		if seen[name] {
			return fmt.Errorf("duplicate log_fields.%s", name)
		}

		seen[name] = true
		if err := validateJSONAliasYAML(&value, make(map[*yaml.Node]bool)); err != nil {
			return fmt.Errorf("log_fields.%s: %w", name, err)
		}

		if err := value.Decode(target); err != nil {
			return fmt.Errorf("log_fields.%s: %w", name, err)
		}
	}

	return nil
}

func validateJSONAliasYAML(node *yaml.Node, visiting map[*yaml.Node]bool) error {
	if node == nil || visiting[node] {
		return fmt.Errorf("cyclic JSON field aliases")
	}

	visiting[node] = true
	defer delete(visiting, node)

	switch node.Kind {
	case yaml.AliasNode:
		return validateJSONAliasYAML(node.Alias, visiting)
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateJSONAliasYAML(child, visiting); err != nil {
				return err
			}
		}

		return nil
	case yaml.MappingNode:
		return validateJSONMappingYAML(node, visiting)
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return nil
		}
	}

	return fmt.Errorf("expected an alias-to-selector mapping")
}

func validateJSONMappingYAML(node *yaml.Node, visiting map[*yaml.Node]bool) error {
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			if err := validateJSONAliasYAML(value, visiting); err != nil {
				return err
			}

			continue
		}

		if err := validateYAMLString(key, visiting); err != nil {
			return fmt.Errorf("JSON field alias: %w", err)
		}

		if err := validateYAMLString(value, visiting); err != nil {
			return fmt.Errorf("JSON selector: %w", err)
		}
	}

	return nil
}

func validateYAMLString(node *yaml.Node, visiting map[*yaml.Node]bool) error {
	if node == nil || visiting[node] {
		return fmt.Errorf("cyclic scalar alias")
	}

	if node.Kind == yaml.AliasNode {
		visiting[node] = true
		defer delete(visiting, node)

		return validateYAMLString(node.Alias, visiting)
	}

	if node.Kind != yaml.ScalarNode || node.Tag != yamlStringTag {
		return fmt.Errorf("expected a string")
	}

	return nil
}
