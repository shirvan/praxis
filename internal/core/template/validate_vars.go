package template

import (
	"fmt"
	"strings"

	"github.com/praxiscloud/praxis/pkg/types"
)

// ValidateVariables checks user-provided variables against a stored schema.
// Returns nil if valid, or a descriptive error listing all violations.
func ValidateVariables(schema types.VariableSchema, vars map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	var errs []string

	// Check required variables are present.
	for name, field := range schema {
		if !field.Required {
			continue
		}
		if _, ok := vars[name]; !ok {
			errs = append(errs, fmt.Sprintf("missing required variable %q (type: %s)", name, field.Type))
		}
	}

	// Check type and constraints for provided variables.
	for name, val := range vars {
		field, known := schema[name]
		if !known {
			continue // unknown variables are allowed (may be consumed elsewhere)
		}

		if err := validateType(name, field, val); err != nil {
			errs = append(errs, err.Error())
		}

		if len(field.Enum) > 0 {
			if err := validateEnum(name, field.Enum, val); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("variable validation failed:\n  - %s", strings.Join(errs, "\n  - "))
}

func validateType(name string, field types.VariableField, val any) error {
	switch field.Type {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("variable %q: expected string, got %T", name, val)
		}
	case "bool":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("variable %q: expected bool, got %T", name, val)
		}
	case "int":
		switch val.(type) {
		case int, int64, float64:
			// float64 is accepted because JSON unmarshals numbers as float64
		default:
			return fmt.Errorf("variable %q: expected int, got %T", name, val)
		}
	case "float":
		switch val.(type) {
		case float64, int, int64:
		default:
			return fmt.Errorf("variable %q: expected float, got %T", name, val)
		}
	case "list":
		items, ok := val.([]any)
		if !ok {
			return fmt.Errorf("variable %q: expected list, got %T", name, val)
		}
		if field.Items != "" && field.Items != "any" {
			for i, elem := range items {
				if err := validateListElement(name, field.Items, i, elem); err != nil {
					return err
				}
			}
		}
	case "struct":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("variable %q: expected struct (object), got %T", name, val)
		}
	}
	return nil
}

func validateListElement(varName, itemType string, index int, val any) error {
	switch itemType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("variable %q[%d]: expected string element, got %T", varName, index, val)
		}
	case "bool":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("variable %q[%d]: expected bool element, got %T", varName, index, val)
		}
	case "int":
		switch val.(type) {
		case int, int64, float64:
		default:
			return fmt.Errorf("variable %q[%d]: expected int element, got %T", varName, index, val)
		}
	case "float":
		switch val.(type) {
		case float64, int, int64:
		default:
			return fmt.Errorf("variable %q[%d]: expected float element, got %T", varName, index, val)
		}
	case "struct":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("variable %q[%d]: expected struct element, got %T", varName, index, val)
		}
	}
	return nil
}

func validateEnum(name string, allowed []string, val any) error {
	s, ok := val.(string)
	if !ok {
		return nil // type validation handles the mismatch
	}
	for _, a := range allowed {
		if s == a {
			return nil
		}
	}
	return fmt.Errorf("variable %q: value %q not in allowed set [%s]", name, s, strings.Join(allowed, ", "))
}
