// Package rules is the binding evaluation of the JSON rule language of the
// metamodel (MET-45, CH18, REP-01): the AST of api/rules.schema.json.
//
// A value rule (Parse, Evaluate) checks one value with required, pattern,
// range and enum, combined with all. A condition (ParseCondition,
// EvaluateCondition, WP-715) is true or false for a record: comparisons of
// a field (eq, ne, in, not_in, gt, gte, lt, lte, contains, empty,
// not_empty with the semantics of fieldmeta.Predicate) combined with all,
// any and not. The condition grammar is the common form of metamodel
// conditions, guards and filters (CH18, MET-45).
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
	"slices"
	"sort"
	"strconv"
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

	// Logic nodes of conditions; all also combines value rules.
	OpAny = "any"
	OpNot = "not"

	// Comparisons of conditions (fieldmeta.Predicate).
	OpEq       = "eq"
	OpNe       = "ne"
	OpIn       = "in"
	OpNotIn    = "not_in"
	OpGt       = "gt"
	OpGte      = "gte"
	OpLt       = "lt"
	OpLte      = "lte"
	OpContains = "contains"
	OpEmpty    = "empty"
	OpNotEmpty = "not_empty"
)

// ValueOps are the operators only value rules use.
var ValueOps = []string{OpRequired, OpPattern, OpRange, OpEnum}

// LogicOps combine rules: all in value rules and conditions, any and not
// only in conditions.
var LogicOps = []string{OpAll, OpAny, OpNot}

// CompareOps are the comparisons only conditions use.
var CompareOps = []string{
	OpEq, OpNe, OpIn, OpNotIn, OpGt, OpGte, OpLt, OpLte, OpContains, OpEmpty, OpNotEmpty,
}

// Ops lists every operator of the AST.
var Ops = slices.Concat(LogicOps, ValueOps, CompareOps)

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
	// Field and Value belong to the comparisons of a condition.
	Field string `json:"field,omitempty"`
	Value any    `json:"value,omitempty"`
}

// MarshalJSON writes the rule in the form of api/rules.schema.json: all,
// any and not always carry their rules (also none) and nothing else. The
// value receiver makes json.Marshal use it for values as well as pointers.
func (r Rule) MarshalJSON() ([]byte, error) { //nolint:gocritic // see above
	type plain Rule
	if !slices.Contains(LogicOps, r.Op) {
		return json.Marshal(plain(r))
	}
	all := struct {
		Op    string `json:"op"`
		Rules []Rule `json:"rules"`
	}{r.Op, r.Rules}
	if all.Rules == nil {
		all.Rules = []Rule{}
	}
	return json.Marshal(all)
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
// validation: fixed texts naming the operator and the property that fail.
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
	case OpAny, OpNot:
		return fmt.Errorf("%s: a logic node of conditions, not of value rules", r.Op)
	default:
		if slices.Contains(CompareOps, r.Op) {
			return fmt.Errorf("%s: a comparison of conditions, not of value rules", r.Op)
		}
		return fmt.Errorf("unknown operator %q", r.Op)
	}
	if r.Op != OpAll && len(r.Rules) > 0 || r.Op != OpPattern && r.Pattern != "" ||
		r.Op != OpRange && (r.Of != "" || r.Min != nil || r.Max != nil) || r.Op != OpEnum && r.Values != nil ||
		r.Field != "" || r.Value != nil {
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
			if Equal(allowed, value) {
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

// Equal compares strings, numbers and booleans by type and value, as enum
// does.
func Equal(a, b any) bool {
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

// ─── Conditions (WP-715) ────────────────────────────────────────────────────

// conditionKeys are the properties each operator of a condition allows.
var conditionKeys = func() map[string][]string {
	keys := map[string][]string{}
	for _, op := range LogicOps {
		keys[op] = []string{"op", "rules"}
	}
	for _, op := range CompareOps {
		keys[op] = []string{"op", "field", "value"}
	}
	return keys
}()

// ParseCondition decodes and validates a condition strictly: unknown
// properties, unknown operators and value operators are errors; the
// message starts with the path of the failing property, e.g.
// rules[1].field.
func ParseCondition(raw []byte) (Rule, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return Rule{}, fmt.Errorf("condition: %w", err)
	}
	return parseCondition(v, "")
}

// at joins a path and a property.
func at(path, prop string) string {
	if path == "" {
		return prop
	}
	return path + "." + prop
}

// parseCondition checks one node of a condition below path.
// validation: fixed texts naming the path and the operator that fail.
func parseCondition(v any, path string) (Rule, error) {
	node, ok := v.(map[string]any)
	if !ok {
		where := path
		if where == "" {
			where = "condition"
		}
		return Rule{}, fmt.Errorf("%s: a condition is an object", where)
	}
	op, _ := node["op"].(string)
	keys, known := conditionKeys[op]
	if !known {
		if slices.Contains(ValueOps, op) {
			return Rule{}, fmt.Errorf("%s: %s is a value rule, not a condition", at(path, "op"), op)
		}
		return Rule{}, fmt.Errorf("%s: unknown operator %q", at(path, "op"), fmt.Sprint(node["op"]))
	}
	props := make([]string, 0, len(node))
	for k := range node {
		props = append(props, k)
	}
	sort.Strings(props)
	for _, k := range props {
		if !slices.Contains(keys, k) {
			return Rule{}, fmt.Errorf("%s: unknown property of %s", at(path, k), op)
		}
	}
	if slices.Contains(LogicOps, op) {
		return parseLogic(node, op, path)
	}
	field, ok := node["field"].(string)
	if !ok || strings.TrimSpace(field) == "" {
		return Rule{}, fmt.Errorf("%s: %s requires a field", at(path, "field"), op)
	}
	value, has := node["value"]
	switch op {
	case OpEmpty, OpNotEmpty:
		if has {
			return Rule{}, fmt.Errorf("%s: %s takes no value", at(path, "value"), op)
		}
		return Rule{Op: op, Field: field}, nil
	case OpIn, OpNotIn:
		list, ok := value.([]any)
		if !ok || len(list) == 0 {
			return Rule{}, fmt.Errorf("%s: %s requires a non-empty array", at(path, "value"), op)
		}
		for i, item := range list {
			if !scalar(item) {
				return Rule{}, fmt.Errorf("%s[%d]: a string, number or boolean is expected", at(path, "value"), i)
			}
		}
	case OpGt, OpGte, OpLt, OpLte:
		if _, ok := number(value); !ok {
			return Rule{}, fmt.Errorf("%s: %s requires a number", at(path, "value"), op)
		}
	default:
		if !has || !scalar(value) {
			return Rule{}, fmt.Errorf("%s: %s requires a string, number or boolean", at(path, "value"), op)
		}
	}
	return Rule{Op: op, Field: field, Value: value}, nil
}

// parseLogic checks all, any and not with their rules.
func parseLogic(node map[string]any, op, path string) (Rule, error) {
	list, ok := node["rules"].([]any)
	if !ok {
		return Rule{}, fmt.Errorf("%s: %s requires an array of rules", at(path, "rules"), op)
	}
	if op == OpNot && len(list) != 1 {
		return Rule{}, fmt.Errorf("%s: not takes exactly one rule", at(path, "rules"))
	}
	r := Rule{Op: op, Rules: make([]Rule, 0, len(list))}
	for i, child := range list {
		c, err := parseCondition(child, at(path, fmt.Sprintf("rules[%d]", i)))
		if err != nil {
			return Rule{}, err
		}
		r.Rules = append(r.Rules, c)
	}
	return r, nil
}

// scalar reports whether v is a string, number or boolean.
func scalar(v any) bool {
	switch v.(type) {
	case string, bool:
		return true
	}
	_, ok := number(v)
	return ok
}

// EvaluateCondition reports whether the condition holds for the values of
// a record; an absent field has the value nil. all of no rules holds, any
// of no rules does not. The condition is assumed valid (ParseCondition).
func EvaluateCondition(cond *Rule, values map[string]any) bool {
	switch cond.Op {
	case OpAll:
		for i := range cond.Rules {
			if !EvaluateCondition(&cond.Rules[i], values) {
				return false
			}
		}
		return true
	case OpAny:
		for i := range cond.Rules {
			if EvaluateCondition(&cond.Rules[i], values) {
				return true
			}
		}
		return false
	case OpNot:
		return len(cond.Rules) == 1 && !EvaluateCondition(&cond.Rules[0], values)
	}
	return compare(cond, values[cond.Field])
}

// compare evaluates one comparison like fieldmeta.Predicate: tolerant of
// numbers sent as text, so the same condition works on API payloads and
// stored JSONB attributes.
func compare(r *Rule, actual any) bool {
	switch r.Op {
	case OpEmpty:
		return blank(actual)
	case OpNotEmpty:
		return !blank(actual)
	case OpEq:
		return looseEqual(actual, r.Value)
	case OpNe:
		return !looseEqual(actual, r.Value)
	case OpIn:
		return inList(actual, r.Value)
	case OpNotIn:
		return !inList(actual, r.Value)
	case OpGt, OpGte, OpLt, OpLte:
		return compareNumbers(r.Op, actual, r.Value)
	case OpContains:
		if s, ok := actual.(string); ok {
			needle, ok := text(r.Value)
			return ok && strings.Contains(s, needle)
		}
		if list, ok := array(actual); ok {
			for _, item := range list {
				if looseEqual(item, r.Value) {
					return true
				}
			}
		}
	}
	return false
}

func compareNumbers(op string, actual, expected any) bool {
	a, ok := looseNumber(actual)
	if !ok {
		return false
	}
	b, ok := looseNumber(expected)
	if !ok {
		return false
	}
	switch op {
	case OpGt:
		return a > b
	case OpGte:
		return a >= b
	case OpLt:
		return a < b
	}
	return a <= b
}

// blank is Empty plus the empty object, as in fieldmeta.
func blank(v any) bool {
	if m, ok := v.(map[string]any); ok {
		return len(m) == 0
	}
	return Empty(v)
}

// array returns the elements of an array value.
func array(v any) ([]any, bool) {
	switch v.(type) {
	case []any, []string:
		return elements(v), true
	}
	return nil, false
}

// numeric is a decimal number as text; other notations (hex, Infinity)
// are text in Go and TypeScript alike.
var numeric = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// looseNumber accepts numbers and decimal numbers as text.
func looseNumber(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return number(v)
	}
	s = strings.TrimSpace(s)
	if !numeric.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// text is the text form of a string, number or boolean; numbers are
// written like JavaScript's String().
func text(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	}
	f, ok := number(v)
	if !ok {
		return "", false
	}
	if abs := math.Abs(f); f == 0 {
		return "0", true
	} else if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64), true
	}
	mantissa, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	n, _ := strconv.Atoi(exp)
	return fmt.Sprintf("%se%+d", mantissa, n), true
}

// looseEqual: two empty values are equal, numbers compare by value (also
// as text), strings and booleans by their text; a non-empty array or
// object equals nothing.
func looseEqual(a, b any) bool {
	if blank(a) && blank(b) {
		return true
	}
	if fa, ok := looseNumber(a); ok {
		fb, ok := looseNumber(b)
		return ok && fa == fb
	}
	ta, ok := text(a)
	if !ok {
		return false
	}
	tb, ok := text(b)
	return ok && ta == tb
}

func inList(actual, list any) bool {
	items, ok := array(list)
	if !ok {
		return false
	}
	for _, item := range items {
		if looseEqual(actual, item) {
			return true
		}
	}
	return false
}
