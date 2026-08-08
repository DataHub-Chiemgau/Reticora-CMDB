package form

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FieldError describes one JSON-Schema validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError aggregates invalid fields for RFC 7807 responses.
type ValidationError struct {
	Fields []FieldError `json:"fields"`
}

func (e ValidationError) Error() string { return "form submission failed schema validation" }

// Validate checks a deliberately focused subset of JSON Schema used by forms.
func Validate(schema JSONMap, value any) error {
	var fields []FieldError
	validateValue(schema, value, "$", &fields)
	if len(fields) > 0 {
		return ValidationError{Fields: fields}
	}
	return nil
}

func validateValue(schema JSONMap, value any, path string, fields *[]FieldError) {
	typ, _ := schema["type"].(string)
	if typ != "" && !matchesType(typ, value) {
		add(fields, path, "expected "+typ)
		return
	}
	if enums, ok := schema["enum"].([]any); ok && len(enums) > 0 {
		matched := false
		for _, enum := range enums {
			if fmt.Sprint(enum) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			add(fields, path, "must be one of the allowed values")
		}
	}
	switch v := value.(type) {
	case JSONMap:
		validateObject(schema, map[string]any(v), path, fields)
	case map[string]any:
		validateObject(schema, v, path, fields)
	case []any:
		validateArray(schema, v, path, fields)
	case string:
		if min, ok := number(schema["minLength"]); ok && float64(len(v)) < min {
			add(fields, path, "is shorter than minimum length")
		}
		if max, ok := number(schema["maxLength"]); ok && float64(len(v)) > max {
			add(fields, path, "is longer than maximum length")
		}
		if pattern, ok := schema["pattern"].(string); ok && pattern != "" {
			re, err := regexp.Compile(pattern)
			if err != nil {
				add(fields, path, "schema pattern is invalid")
			} else if !re.MatchString(v) {
				add(fields, path, "does not match required pattern")
			}
		}
	case float64, float32, int, int64, int32, jsonNumber:
		val, ok := number(v)
		if !ok {
			return
		}
		if min, ok := number(schema["minimum"]); ok && val < min {
			add(fields, path, "is below minimum")
		}
		if max, ok := number(schema["maximum"]); ok && val > max {
			add(fields, path, "is above maximum")
		}
	}
}

type jsonNumber interface{ String() string }

func validateObject(schema JSONMap, obj map[string]any, path string, fields *[]FieldError) {
	if raw, exists := schema["required"]; exists && raw != nil {
		req, ok := raw.([]any)
		if !ok {
			add(fields, path, "schema 'required' must be an array")
		} else {
			for _, item := range req {
				name, ok := item.(string)
				if !ok || name == "" {
					add(fields, path, "schema 'required' entries must be non-empty strings")
					continue
				}
				if _, exists := obj[name]; !exists {
					add(fields, join(path, name), "is required")
				}
			}
		}
	}
	rawProps, exists := schema["properties"]
	if !exists || rawProps == nil {
		return
	}
	props, ok := rawProps.(map[string]any)
	if !ok {
		add(fields, path, "schema 'properties' must be an object")
		return
	}
	for name, raw := range props {
		child, ok := raw.(map[string]any)
		if !ok {
			add(fields, join(path, name), "schema property must be an object")
			continue
		}
		if val, exists := obj[name]; exists {
			validateValue(child, val, join(path, name), fields)
		}
	}
}

func validateArray(schema JSONMap, arr []any, path string, fields *[]FieldError) {
	if min, ok := number(schema["minItems"]); ok && float64(len(arr)) < min {
		add(fields, path, "has too few items")
	}
	if max, ok := number(schema["maxItems"]); ok && float64(len(arr)) > max {
		add(fields, path, "has too many items")
	}
	rawItems, exists := schema["items"]
	if !exists || rawItems == nil {
		return
	}
	itemSchema, ok := rawItems.(map[string]any)
	if !ok {
		add(fields, path, "schema 'items' must be an object")
		return
	}
	for i, item := range arr {
		validateValue(itemSchema, item, path+"["+strconv.Itoa(i)+"]", fields)
	}
}

func matchesType(typ string, value any) bool {
	switch typ {
	case "object":
		if _, ok := value.(map[string]any); ok {
			return true
		}
		_, ok := value.(JSONMap)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := number(value)
		return ok
	case "integer":
		n, ok := number(value)
		return ok && n == float64(int64(n))
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case jsonNumber:
		f, err := strconv.ParseFloat(n.String(), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func add(fields *[]FieldError, field, msg string) {
	*fields = append(*fields, FieldError{Field: field, Message: msg})
}
func join(path, name string) string {
	if strings.HasSuffix(path, ".") {
		return path + name
	}
	return path + "." + name
}
