package form

import "testing"

func TestValidateSubset(t *testing.T) {
	schema := JSONMap{"type": "object", "required": []any{"name", "tags"}, "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 2, "pattern": "^[A-Z]"}, "age": map[string]any{"type": "integer", "minimum": 18, "maximum": 99}, "tags": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"a", "b"}}}, "nested": map[string]any{"type": "object", "required": []any{"ok"}, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}}}
	if err := Validate(schema, JSONMap{"name": "Alice", "age": 30, "tags": []any{"a"}, "nested": map[string]any{"ok": true}}); err != nil {
		t.Fatalf("valid: %v", err)
	}
	err := Validate(schema, JSONMap{"name": "bob", "age": 12, "tags": []any{"c"}, "nested": map[string]any{}})
	if err == nil {
		t.Fatal("expected validation error")
	}
	ve := err.(ValidationError)
	if len(ve.Fields) < 4 {
		t.Fatalf("expected field errors, got %#v", ve.Fields)
	}
}
