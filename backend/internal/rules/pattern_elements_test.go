package rules

import (
	"reflect"
	"testing"
)

// TestPatternOverArrayElements pins the pattern violations of array values:
// each element in turn, the type check before the pattern, the first
// violating element wins, an invalid pattern fails every text element.
func TestPatternOverArrayElements(t *testing.T) {
	typeV := []Violation{{CodePatternType, "a text value is expected"}}
	patternV := func(p string) []Violation {
		return []Violation{{CodePattern, `value does not match pattern "` + p + `"`}}
	}
	for _, c := range []struct {
		name    string
		pattern string
		value   any
		want    []Violation
	}{
		{"all match", "^a", []any{"ab", "ac"}, nil},
		{"all match strings", "^a", []string{"ab", "ac", "a"}, nil},
		{"second does not match", "^a", []any{"ab", "b", 1.0}, patternV("^a")},
		{"second is no text", "^a", []any{"ab", 1.0, "b"}, typeV},
		{"first is no text", "^a", []any{true, "b"}, typeV},
		{"invalid pattern, text first", "(", []any{"x", 1.0}, patternV("(")},
		{"invalid pattern, no text first", "(", []any{1.0, "x"}, typeV},
		{"invalid pattern, only no text", "(", []any{1.0}, typeV},
		{"invalid pattern, strings", "(", []string{"x"}, patternV("(")},
		{"invalid pattern, scalar", "(", "x", patternV("(")},
		{"scalar no text", "^a", 3.0, typeV},
		{"scalar match", "^a", "abc", nil},
		{"empty array", "(", []any{}, nil},
		{"blank text", "(", "  ", nil},
	} {
		r := &Rule{Op: OpPattern, Pattern: c.pattern}
		if got := Evaluate(r, c.value, true); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: violations = %#v, want %#v", c.name, got, c.want)
		}
	}
}
