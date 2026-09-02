// Package fieldmeta provides the shared field-definition registry used by the
// metadata-driven CI type system: the canonical field types, validation rule
// evaluation, and conditional-attribute rule evaluation.
//
// The same definitions drive three attribute scopes (global, CI-type,
// CI-instance) so instance-specific fields behave exactly like type-level
// attributes (spec §2, §3). The frontend carries a TypeScript mirror of the
// conditional evaluator so forms evaluate rules client-side without exposing
// the rule model to end users.
package fieldmeta

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Field types supported by the metadata-driven CI type system (spec §1).
const (
	TypeText        = "text"
	TypeTextarea    = "textarea"
	TypeInteger     = "integer"
	TypeDecimal     = "decimal"
	TypeBoolean     = "boolean"
	TypeDate        = "date"
	TypeDatetime    = "datetime"
	TypeEnum        = "enum"
	TypeMultiEnum   = "multi_enum"
	TypeIP          = "ip"
	TypeMAC         = "mac"
	TypeURL         = "url"
	TypeEmail       = "email"
	TypeJSON        = "json"
	TypeFile        = "file"
	TypeUser        = "user"
	TypeTeam        = "team"
	TypeLocation    = "location"
	TypeCIRef       = "ci_ref"
	TypeAssetRef    = "asset_ref"
	TypeContractRef = "contract_ref"
)

// ValidFieldTypes is the registry of supported field types. Legacy type names
// from the original ci_type_attribute CHECK constraint (migration 000002) are
// accepted as aliases so existing metadata keeps validating.
var ValidFieldTypes = map[string]bool{
	TypeText: true, TypeTextarea: true, TypeInteger: true, TypeDecimal: true,
	TypeBoolean: true, TypeDate: true, TypeDatetime: true, TypeEnum: true,
	TypeMultiEnum: true, TypeIP: true, TypeMAC: true, TypeURL: true,
	TypeEmail: true, TypeJSON: true, TypeFile: true, TypeUser: true,
	TypeTeam: true, TypeLocation: true, TypeCIRef: true, TypeAssetRef: true,
	TypeContractRef: true,
	// Legacy aliases
	"string": true, "number": true,
}

// ReferenceTargets lists the valid reference_target values for reference-typed
// fields.
var ReferenceTargets = map[string]bool{
	"user": true, "team": true, "location": true,
	"ci": true, "asset": true, "contract": true,
}

// FieldDefinition is the canonical shape of a field definition across all
// three attribute scopes. Storage rows (ci_type_attribute,
// ci_instance_field_definition) map onto this structure.
type FieldDefinition struct {
	Name            string            `json:"name"`
	Label           string            `json:"label,omitempty"`
	Description     string            `json:"description,omitempty"`
	DataType        string            `json:"data_type"`
	Required        bool              `json:"required"`
	DefaultValue    string            `json:"default_value,omitempty"`
	EnumValues      []string          `json:"enum_values,omitempty"`
	UIGroup         string            `json:"ui_group,omitempty"`
	SortOrder       int               `json:"sort_order"`
	Validation      *ValidationRules  `json:"validation,omitempty"`
	Conditional     *ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget string            `json:"reference_target,omitempty"`
}

// ValidationRules carries the configurable validation metadata of a field.
type ValidationRules struct {
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Options   []string `json:"options,omitempty"`
}

// ConditionalRules describe how a field reacts to the values of other fields
// (spec §3). All slices are AND-ed; each entry is one predicate.
type ConditionalRules struct {
	// VisibleWhen hides the field unless every predicate matches.
	VisibleWhen []Predicate `json:"visible_when,omitempty"`
	// RequiredWhen makes the field required when every predicate matches.
	RequiredWhen []Predicate `json:"required_when,omitempty"`
	// ReadOnlyWhen renders the field read-only when every predicate matches.
	ReadOnlyWhen []Predicate `json:"read_only_when,omitempty"`
	// AllowedValuesWhen restricts the accepted values when the predicates
	// match.
	AllowedValuesWhen *ConditionalValues `json:"allowed_values_when,omitempty"`
}

// ConditionalValues restricts the allowed values of a field while its
// predicates match.
type ConditionalValues struct {
	When   []Predicate `json:"when"`
	Values []any       `json:"values"`
}

// Predicate compares the current value of a field against a constant.
// Supported operators: eq, ne, in, not_in, gt, gte, lt, lte, contains,
// empty, not_empty.
type Predicate struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

// ValidateDefinition checks structural validity of a field definition.
func ValidateDefinition(def FieldDefinition) error {
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("field name is required")
	}
	if !ValidFieldTypes[def.DataType] {
		return fmt.Errorf("unsupported field type %q", def.DataType)
	}
	if def.ReferenceTarget != "" && !ReferenceTargets[def.ReferenceTarget] {
		return fmt.Errorf("unsupported reference target %q", def.ReferenceTarget)
	}
	if def.Validation != nil && def.Validation.Pattern != "" {
		if _, err := regexp.Compile(def.Validation.Pattern); err != nil {
			return fmt.Errorf("invalid validation pattern: %w", err)
		}
	}
	for _, p := range def.allPredicates() {
		if p.Field == "" {
			return fmt.Errorf("conditional predicate requires a field")
		}
		if !validOps[p.Op] {
			return fmt.Errorf("unsupported predicate operator %q", p.Op)
		}
	}
	return nil
}

func (def FieldDefinition) allPredicates() []Predicate {
	if def.Conditional == nil {
		return nil
	}
	var out []Predicate
	out = append(out, def.Conditional.VisibleWhen...)
	out = append(out, def.Conditional.RequiredWhen...)
	out = append(out, def.Conditional.ReadOnlyWhen...)
	if def.Conditional.AllowedValuesWhen != nil {
		out = append(out, def.Conditional.AllowedValuesWhen.When...)
	}
	return out
}

var validOps = map[string]bool{
	"eq": true, "ne": true, "in": true, "not_in": true,
	"gt": true, "gte": true, "lt": true, "lte": true,
	"contains": true, "empty": true, "not_empty": true,
}

// ─── Conditional rule evaluation ────────────────────────────────────────────

// FieldState is the effective presentation/validation state of a field after
// evaluating its conditional rules against the current values.
type FieldState struct {
	Visible       bool     `json:"visible"`
	Required      bool     `json:"required"`
	ReadOnly      bool     `json:"read_only"`
	AllowedValues []string `json:"allowed_values,omitempty"`
}

// Evaluate resolves the conditional state of a field against the current
// attribute values. Without conditional rules the field is visible and keeps
// its static required flag.
func Evaluate(def FieldDefinition, values map[string]any) FieldState {
	state := FieldState{Visible: true, Required: def.Required}
	c := def.Conditional
	if c == nil {
		if len(def.EnumValues) > 0 {
			state.AllowedValues = def.EnumValues
		}
		return state
	}
	if len(c.VisibleWhen) > 0 && !matchAll(c.VisibleWhen, values) {
		state.Visible = false
		state.Required = false
		return state
	}
	if len(c.RequiredWhen) > 0 && matchAll(c.RequiredWhen, values) {
		state.Required = true
	}
	if len(c.ReadOnlyWhen) > 0 && matchAll(c.ReadOnlyWhen, values) {
		state.ReadOnly = true
	}
	if c.AllowedValuesWhen != nil && matchAll(c.AllowedValuesWhen.When, values) {
		state.AllowedValues = stringifyAll(c.AllowedValuesWhen.Values)
	} else if len(def.EnumValues) > 0 {
		state.AllowedValues = def.EnumValues
	}
	return state
}

func matchAll(preds []Predicate, values map[string]any) bool {
	for _, p := range preds {
		if !match(p, values[p.Field]) {
			return false
		}
	}
	return true
}

// match evaluates a single predicate. It is deliberately tolerant of JSON
// number representations (float64 vs string) so the same rules work against
// API payloads and stored JSONB attributes.
func match(p Predicate, actual any) bool {
	switch p.Op {
	case "empty":
		return isEmpty(actual)
	case "not_empty":
		return !isEmpty(actual)
	case "eq":
		return looseEqual(actual, p.Value)
	case "ne":
		return !looseEqual(actual, p.Value)
	case "in":
		return inList(actual, p.Value)
	case "not_in":
		return !inList(actual, p.Value)
	case "gt", "gte", "lt", "lte":
		return compareNumeric(p.Op, actual, p.Value)
	case "contains":
		return containsValue(actual, p.Value)
	}
	return false
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func looseEqual(a, b any) bool {
	if isEmpty(a) && isEmpty(b) {
		return true
	}
	if af, ok := toFloat(a); ok {
		bf, ok := toFloat(b)
		return ok && af == bf
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func inList(actual, list any) bool {
	items, ok := list.([]any)
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

func containsValue(actual, needle any) bool {
	switch t := actual.(type) {
	case string:
		return strings.Contains(t, fmt.Sprintf("%v", needle))
	case []any:
		for _, item := range t {
			if looseEqual(item, needle) {
				return true
			}
		}
	}
	return false
}

func compareNumeric(op string, actual, expected any) bool {
	af, ok := toFloat(actual)
	if !ok {
		return false
	}
	bf, ok := toFloat(expected)
	if !ok {
		return false
	}
	switch op {
	case "gt":
		return af > bf
	case "gte":
		return af >= bf
	case "lt":
		return af < bf
	case "lte":
		return af <= bf
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func stringifyAll(values []any) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}

// ─── Value validation ────────────────────────────────────────────────────────

// ValidateValue validates a single field value against its definition,
// combining the type check with the validation rules. Empty values are valid
// unless the field is required (required checks happen at the form/transition
// level via FieldState).
func ValidateValue(def FieldDefinition, value any) error {
	if isEmpty(value) {
		return nil
	}
	if err := checkType(def.DataType, value); err != nil {
		return fmt.Errorf("field %s: %w", def.Name, err)
	}
	v := def.Validation
	if v == nil {
		if def.DataType == TypeEnum || def.DataType == TypeMultiEnum {
			return checkEnum(def, value)
		}
		return nil
	}
	if err := checkBounds(def, value, v); err != nil {
		return err
	}
	if v.Pattern != "" {
		str, ok := value.(string)
		if ok {
			matched, err := regexp.MatchString(v.Pattern, str)
			if err != nil {
				return fmt.Errorf("field %s: invalid validation pattern", def.Name)
			}
			if !matched {
				return fmt.Errorf("field %s: value does not match pattern %q", def.Name, v.Pattern)
			}
		}
	}
	if len(v.Options) > 0 {
		if err := checkOptions(def, value, v.Options); err != nil {
			return err
		}
		return nil
	}
	if def.DataType == TypeEnum || def.DataType == TypeMultiEnum {
		return checkEnum(def, value)
	}
	return nil
}

func checkType(dataType string, value any) error {
	switch dataType {
	case TypeText, TypeTextarea, TypeFile, "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string")
		}
	case TypeInteger:
		f, ok := toFloat(value)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("expected integer")
		}
	case TypeDecimal, "number":
		if _, ok := toFloat(value); !ok {
			return fmt.Errorf("expected number")
		}
	case TypeBoolean:
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	case TypeDate:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected date string")
		}
		if _, err := time.Parse("2006-01-02", str); err != nil {
			return fmt.Errorf("expected date in YYYY-MM-DD format")
		}
	case TypeDatetime:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected datetime string")
		}
		if _, err := time.Parse(time.RFC3339, str); err != nil {
			return fmt.Errorf("expected RFC3339 datetime")
		}
	case TypeEnum:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected enum string")
		}
	case TypeMultiEnum:
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("expected array of enum values")
		}
	case TypeIP:
		str, ok := value.(string)
		if !ok || net.ParseIP(str) == nil {
			return fmt.Errorf("expected valid IP address")
		}
	case TypeMAC:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected MAC string")
		}
		if _, err := net.ParseMAC(str); err != nil {
			return fmt.Errorf("expected valid MAC address")
		}
	case TypeURL:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected URL string")
		}
		u, err := url.Parse(str)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("expected valid URL")
		}
	case TypeEmail:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected email string")
		}
		if _, err := mail.ParseAddress(str); err != nil {
			return fmt.Errorf("expected valid email address")
		}
	case TypeJSON:
		// Any JSON value is valid.
	case TypeUser, TypeTeam, TypeLocation, TypeCIRef, TypeAssetRef, TypeContractRef:
		// Reference fields store the referenced entity's UUID.
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected reference id string")
		}
	default:
		return fmt.Errorf("unknown field type %q", dataType)
	}
	return nil
}

func checkBounds(def FieldDefinition, value any, v *ValidationRules) error {
	if v.Min != nil || v.Max != nil {
		if f, ok := toFloat(value); ok {
			if v.Min != nil && f < *v.Min {
				return fmt.Errorf("field %s: value below minimum %v", def.Name, *v.Min)
			}
			if v.Max != nil && f > *v.Max {
				return fmt.Errorf("field %s: value above maximum %v", def.Name, *v.Max)
			}
		}
	}
	if v.MinLength != nil || v.MaxLength != nil {
		if str, ok := value.(string); ok {
			if v.MinLength != nil && len(str) < *v.MinLength {
				return fmt.Errorf("field %s: value shorter than %d characters", def.Name, *v.MinLength)
			}
			if v.MaxLength != nil && len(str) > *v.MaxLength {
				return fmt.Errorf("field %s: value longer than %d characters", def.Name, *v.MaxLength)
			}
		}
	}
	return nil
}

func checkEnum(def FieldDefinition, value any) error {
	if len(def.EnumValues) == 0 {
		return nil
	}
	return checkOptions(def, value, def.EnumValues)
}

func checkOptions(def FieldDefinition, value any, options []string) error {
	allowed := make(map[string]bool, len(options))
	for _, o := range options {
		allowed[o] = true
	}
	if def.DataType == TypeMultiEnum {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("field %s: expected array", def.Name)
		}
		for _, item := range items {
			if !allowed[fmt.Sprintf("%v", item)] {
				return fmt.Errorf("field %s: value %v not in allowed options", def.Name, item)
			}
		}
		return nil
	}
	if !allowed[fmt.Sprintf("%v", value)] {
		return fmt.Errorf("field %s: value %v not in allowed options", def.Name, value)
	}
	return nil
}
