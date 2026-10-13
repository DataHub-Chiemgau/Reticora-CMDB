// Package rules is the binding evaluation of the JSON rule language of the
// metamodel (MET-45, CH18, REP-01): the AST of api/rules.schema.json.
// Phase 1 covers required, pattern, range and enum, combined with all.
// frontend/src/rules evaluates the same AST for previews; both pass the
// cases of api/rules.conformance.json.
package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Operators of the rule AST.
const (
	OpAll      = "all"
	OpRequired = "required"
	OpPattern  = "pattern"
	OpRange    = "range"
	OpEnum     = "enum"
)

// Ops lists the operators of phase 1.
var Ops = []string{OpAll, OpRequired, OpPattern, OpRange, OpEnum}

// What a range measures.
const (
	OfValue  = "value"
	OfLength = "length"
)

// Violation codes.
const (
	CodeRequired    = "required"
	CodePattern     = "pattern"
	CodePatternType = "pattern.type"
	CodeRangeMin    = "range.min"
	CodeRangeMax    = "range.max"
	CodeRangeType   = "range.type"
	CodeEnum        = "enum"
)

// Rule is one node of the AST.
type Rule struct {
	Op      string   `json:"op"`
	Rules   []Rule   `json:"rules,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Of      string   `json:"of,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Values  []any    `json:"values,omitempty"`
}

// Violation is one failed rule.
type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Parse decodes and validates a rule strictly: unknown properties and
// invalid rules are errors.
func Parse(raw []byte) (Rule, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r Rule
	if err := dec.Decode(&r); err != nil {
		return Rule{}, fmt.Errorf("rule: %w", err)
	}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// unsupported are the constructs outside the common subset of RE2 and
// ECMAScript.
var unsupported = regexp.MustCompile(`\(\?<?[=!]|\\[1-9]`)

// Validate checks a rule structurally.
func (r *Rule) Validate() error {
	switch r.Op {
	case OpAll:
		for i := range r.Rules {
			if err := r.Rules[i].Validate(); err != nil {
				return fmt.Errorf("rules[%d]: %w", i, err)
			}
		}
	case OpRequired:
	case OpPattern:
		if r.Pattern == "" {
			return errors.New("pattern: the pattern is required")
		}
		if unsupported.MatchString(r.Pattern) {
			return errors.New("pattern: lookaround and backreferences are not supported")
		}
		if _, err := regexp.Compile(r.Pattern); err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
	case OpRange:
		if r.Of != "" && r.Of != OfValue && r.Of != OfLength {
			return fmt.Errorf("range: of must be %s or %s", OfValue, OfLength)
		}
		if r.Min == nil && r.Max == nil {
			return errors.New("range: min or max is required")
		}
		if r.Min != nil && r.Max != nil && *r.Min > *r.Max {
			return errors.New("range: min is above max")
		}
	case OpEnum:
		if len(r.Values) == 0 {
			return errors.New("enum: values are required")
		}
		for _, v := range r.Values {
			switch v.(type) {
			case string, bool:
			default:
				if _, ok := number(v); !ok {
					return fmt.Errorf("enum: %v is no string, number or boolean", v)
				}
			}
		}
	default:
		return fmt.Errorf("unknown operator %q", r.Op)
	}
	if r.Op != OpAll && len(r.Rules) > 0 || r.Op != OpPattern && r.Pattern != "" ||
		r.Op != OpRange && (r.Of != "" || r.Min != nil || r.Max != nil) || r.Op != OpEnum && r.Values != nil {
		return fmt.Errorf("%s: properties of another operator", r.Op)
	}
	return nil
}

// Empty reports whether a value counts as empty: absent (nil), a string of
// only whitespace or an empty array.
func Empty(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	case []string:
		return len(v) == 0
	}
	return false
}

// Evaluate returns the violations of value; present is false when the
// value is absent. The rule is assumed valid (Validate).
func Evaluate(r *Rule, value any, present bool) []Violation {
	if !present {
		value = nil
	}
	return evaluate(r, value)
}

func evaluate(r *Rule, value any) []Violation {
	if r.Op == OpAll {
		var out []Violation
		for i := range r.Rules {
			out = append(out, evaluate(&r.Rules[i], value)...)
		}
		return out
	}
	if r.Op == OpRequired {
		if Empty(value) {
			return []Violation{{CodeRequired, "a value is required"}}
		}
		return nil
	}
	if Empty(value) {
		return nil
	}
	for _, item := range elements(value) {
		if v := check(r, item); v != nil {
			return []Violation{*v}
		}
	}
	return nil
}

// elements are the values a rule applies to: each element of an array.
func elements(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out
	}
	return []any{value}
}

func check(r *Rule, value any) *Violation {
	switch r.Op {
	case OpPattern:
		s, ok := value.(string)
		if !ok {
			return &Violation{CodePatternType, "a text value is expected"}
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil || !re.MatchString(s) {
			return &Violation{CodePattern, fmt.Sprintf("value does not match pattern %q", r.Pattern)}
		}
	case OpRange:
		var n float64
		if r.Of == OfLength {
			s, ok := value.(string)
			if !ok {
				return &Violation{CodeRangeType, "a text value is expected"}
			}
			n = float64(utf8.RuneCountInString(s))
		} else {
			f, ok := number(value)
			if !ok {
				return &Violation{CodeRangeType, "a number is expected"}
			}
			n = f
		}
		if r.Min != nil && n < *r.Min {
			return &Violation{CodeRangeMin, fmt.Sprintf("%s is below the minimum %v", measure(r), *r.Min)}
		}
		if r.Max != nil && n > *r.Max {
			return &Violation{CodeRangeMax, fmt.Sprintf("%s is above the maximum %v", measure(r), *r.Max)}
		}
	case OpEnum:
		for _, allowed := range r.Values {
			if equal(allowed, value) {
				return nil
			}
		}
		return &Violation{CodeEnum, fmt.Sprintf("value %v is not one of the allowed values", value)}
	}
	return nil
}

func measure(r *Rule) string {
	if r.Of == OfLength {
		return "length"
	}
	return "value"
}

// number returns the value of a JSON or Go number; booleans and strings
// are no numbers.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n)
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// equal compares strings, numbers and booleans by type and value.
func equal(a, b any) bool {
	if fa, ok := number(a); ok {
		fb, ok := number(b)
		return ok && fa == fb
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}
