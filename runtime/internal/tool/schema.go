package tool

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"
)

type argumentSchema struct {
	Type                 string                    `json:"type"`
	Required             []string                  `json:"required"`
	Properties           map[string]propertySchema `json:"properties"`
	AdditionalProperties json.RawMessage           `json:"additionalProperties"`
}

type propertySchema struct {
	Type      string `json:"type"`
	Enum      []any  `json:"enum"`
	MinLength *int   `json:"minLength"`
	MaxLength *int   `json:"maxLength"`
}

func ValidateArguments(inputSchema string, arguments map[string]any) error {
	var schema argumentSchema
	if err := json.Unmarshal([]byte(inputSchema), &schema); err != nil {
		return fmt.Errorf("tool input schema is invalid: %w", err)
	}
	if schema.Type != "" && schema.Type != "object" {
		return fmt.Errorf("tool input schema type %q is not supported", schema.Type)
	}

	for _, name := range schema.Required {
		if _, ok := arguments[name]; !ok {
			return fmt.Errorf("missing required argument %q", name)
		}
	}

	if disallowsAdditionalProperties(schema.AdditionalProperties) {
		for name := range arguments {
			if _, ok := schema.Properties[name]; !ok {
				return fmt.Errorf("unexpected argument %q", name)
			}
		}
	}

	for name, property := range schema.Properties {
		value, ok := arguments[name]
		if !ok {
			continue
		}
		if len(property.Enum) > 0 && !enumContains(property.Enum, value) {
			return fmt.Errorf("argument %q must match enum", name)
		}
		if property.Type != "" && !valueMatchesJSONSchemaType(value, property.Type) {
			return fmt.Errorf("argument %q must be %s", name, property.Type)
		}
		if text, ok := value.(string); ok {
			length := utf8.RuneCountInString(text)
			if property.MinLength != nil && length < *property.MinLength {
				return fmt.Errorf("argument %q must contain at least %d characters", name, *property.MinLength)
			}
			if property.MaxLength != nil && length > *property.MaxLength {
				return fmt.Errorf("argument %q must contain at most %d characters", name, *property.MaxLength)
			}
		}
	}
	return nil
}

func disallowsAdditionalProperties(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "false"
}

func enumContains(values []any, candidate any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, candidate) || numericEqual(value, candidate) {
			return true
		}
	}
	return false
}

func valueMatchesJSONSchemaType(value any, want string) bool {
	switch want {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := numericFloat(value)
		return ok
	case "integer":
		number, ok := numericFloat(value)
		return ok && math.Trunc(number) == number
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func numericEqual(a any, b any) bool {
	left, leftOK := numericFloat(a)
	right, rightOK := numericFloat(b)
	return leftOK && rightOK && left == right
}

func numericFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return 0, false
		}
		return float64(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	default:
		return 0, false
	}
}
