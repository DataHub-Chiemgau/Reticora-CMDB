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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Field types of MET-11. Phase 1: text, multiline, integer, decimal,
// boolean, date, datetime, enum, multiselect, ip, mac, url, email, uuid;
// phase 2: file, json and the references.
const (
	TypeText        = "text"
	TypeMultiline   = "multiline"
	TypeMultiselect = "multiselect"
	TypeUUID        = "uuid"
	TypeReference   = "reference"
	// TypeTextarea and TypeMultiEnum are the former names of multiline and
	// multiselect; stored definitions keep validating.
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
// from the original data_type CHECK constraint (migration 000002) are
// accepted as aliases so existing metadata keeps validating.
var ValidFieldTypes = map[string]bool{
	TypeMultiline: true, TypeMultiselect: true, TypeUUID: true, TypeReference: true,
	TypeText: true, TypeTextarea: true, TypeInteger: true, TypeDecimal: true,
	TypeBoolean: true, TypeDate: true, TypeDatetime: true, TypeEnum: true,
	TypeMultiEnum: true, TypeIP: true, TypeMAC: true, TypeURL: true,
	TypeEmail: true, TypeJSON: true, TypeFile: true, TypeUser: true,
	TypeTeam: true, TypeLocation: true, TypeCIRef: true, TypeAssetRef: true,
	TypeContractRef: true,
	// Legacy aliases
	"string": true, "number": true,
}

// FilterKind maps a field type onto the kind of a list filter (API-02):
// numbers compare numerically, dates and datetimes as instants, booleans
// and uuids by value, everything else as text.
func FilterKind(dataType string) string {
	switch dataType {
	case TypeInteger, TypeDecimal, "number":
		return api.KindNumber
	case TypeDate, TypeDatetime:
		return api.KindTime
	case TypeBoolean:
		return api.KindBool
	case TypeUUID:
		return api.KindUUID
	}
	return api.KindText
}

// Phase1Types are the data types of MET-11 phase 1, exactly.
var Phase1Types = []string{
	TypeText, TypeMultiline, TypeInteger, TypeDecimal, TypeBoolean, TypeDate, TypeDatetime,
	TypeEnum, TypeMultiselect, TypeIP, TypeMAC, TypeURL, TypeEmail, TypeUUID,
}

// Phase2Types are the data types of MET-11 phase 2 (file in the document
// module, json, references to CI, asset, user, team, location, contract).
var Phase2Types = []string{TypeFile, TypeJSON, TypeReference}

// aliases map former and specific type names onto the MET-11 type.
var aliases = map[string]string{
	TypeTextarea: TypeMultiline, TypeMultiEnum: TypeMultiselect, "string": TypeText, "number": TypeDecimal,
	TypeUser: TypeReference, TypeTeam: TypeReference, TypeLocation: TypeReference,
	TypeCIRef: TypeReference, TypeAssetRef: TypeReference, TypeContractRef: TypeReference,
}

// CanonicalType returns the MET-11 data type of a (possibly former) type
// name.
func CanonicalType(dataType string) string {
	if c, ok := aliases[dataType]; ok {
		return c
	}
	return dataType
}

// IsMultiValue reports whether a type holds a list of values.
func IsMultiValue(dataType string) bool { return CanonicalType(dataType) == TypeMultiselect }

// ReferenceTargets lists the valid reference_target values for reference-typed
// fields.
var ReferenceTargets = map[string]bool{
	"user": true, "team": true, "location": true,
	"ci": true, "asset": true, "contract": true,
}

// FieldDefinition is the canonical shape of a field definition across all
// three attribute scopes. Storage rows (attribute_definition,
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
	// Patterns are further patterns that apply as well (allOf); composing
	// layers that each set a pattern produces them (MET-16).
	Patterns []string `json:"patterns,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// AllPatterns returns Pattern and Patterns, the empty ones left out.
func (v *ValidationRules) AllPatterns() []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, p := range append([]string{v.Pattern}, v.Patterns...) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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

// ReservedNames are the attribute namespaces of CI-10; no field may be
// called like one (GLO-13).
var ReservedNames = map[string]bool{"_overrides": true, "_observed": true, "_provenance": true, "_instance": true}

// ValidateDefinition checks structural validity of a field definition.
func ValidateDefinition(def FieldDefinition) error {
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("field name is required")
	}
	if ReservedNames[def.Name] {
		return fmt.Errorf("field name %q is a reserved namespace (GLO-13)", def.Name)
	}
	if !ValidFieldTypes[def.DataType] {
		return fmt.Errorf("unsupported field type %q", def.DataType)
	}
	if def.ReferenceTarget != "" && !ReferenceTargets[def.ReferenceTarget] {
		return fmt.Errorf("unsupported reference target %q", def.ReferenceTarget)
	}
	for _, p := range def.Validation.AllPatterns() {
		if _, err := regexp.Compile(p); err != nil {
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

// jsonNumber accepts numbers only: a numeric string is text (MET-11, the
// rule AST of MET-45 agrees).
func jsonNumber(v any) (float64, bool) {
	if _, isString := v.(string); isString {
		return 0, false
	}
	return toFloat(v)
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
	rule := def.Rule(false)
	for _, v := range rules.Evaluate(&rule, value, true) {
		return fmt.Errorf("field %s: %s", def.Name, v.Message)
	}
	return nil
}

// Rule is the rule AST (MET-45, internal/rules) of a definition: required
// (when withRequired), the allowed values (enum values or validation
// options), the pattern and the numeric and length ranges. internal/rules
// evaluates it bindingly; frontend/src/rules builds the same AST.
func (def *FieldDefinition) Rule(withRequired bool) rules.Rule {
	var list []rules.Rule
	if withRequired && def.Required {
		list = append(list, rules.Rule{Op: rules.OpRequired})
	}
	allowed := def.EnumValues
	v := def.Validation
	if v != nil && len(v.Options) > 0 {
		allowed = v.Options
	}
	if len(allowed) > 0 {
		values := make([]any, len(allowed))
		for i, a := range allowed {
			values[i] = a
		}
		list = append(list, rules.Rule{Op: rules.OpEnum, Values: values})
	}
	if v != nil {
		for _, p := range v.AllPatterns() {
			list = append(list, rules.Rule{Op: rules.OpPattern, Pattern: p})
		}
		if v.Min != nil || v.Max != nil {
			list = append(list, rules.Rule{Op: rules.OpRange, Min: v.Min, Max: v.Max})
		}
		if v.MinLength != nil || v.MaxLength != nil {
			r := rules.Rule{Op: rules.OpRange, Of: rules.OfLength}
			if v.MinLength != nil {
				f := float64(*v.MinLength)
				r.Min = &f
			}
			if v.MaxLength != nil {
				f := float64(*v.MaxLength)
				r.Max = &f
			}
			list = append(list, r)
		}
	}
	return rules.Rule{Op: rules.OpAll, Rules: list}
}

// uuidPattern is the textual form of a UUID (any version).
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// checkType checks a value against its MET-11 data type.
func checkType(dataType string, value any) error {
	switch CanonicalType(dataType) {
	case TypeText:
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		if strings.ContainsAny(str, "\r\n") {
			return fmt.Errorf("expected a single line (use multiline for line breaks)")
		}
	case TypeMultiline:
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string")
		}
	case TypeInteger:
		f, ok := jsonNumber(value)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("expected integer")
		}
	case TypeDecimal:
		if _, ok := jsonNumber(value); !ok {
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
	case TypeMultiselect:
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array of enum values")
		}
		seen := map[string]bool{}
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("expected array of enum strings")
			}
			if seen[s] {
				return fmt.Errorf("value %q is selected twice", s)
			}
			seen[s] = true
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
		if hw, err := net.ParseMAC(str); err != nil || (len(hw) != 6 && len(hw) != 8) {
			return fmt.Errorf("expected valid MAC address (EUI-48 or EUI-64)")
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
		if addr, err := mail.ParseAddress(str); err != nil || addr.Address != str {
			return fmt.Errorf("expected valid email address")
		}
	case TypeUUID:
		str, ok := value.(string)
		if !ok || !uuidPattern.MatchString(str) {
			return fmt.Errorf("expected UUID")
		}
	case TypeJSON:
		// Any JSON value is valid.
	case TypeFile, TypeReference:
		// Files and references store the referenced entity's id.
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected reference id string")
		}
	default:
		return fmt.Errorf("unknown field type %q", dataType)
	}
	return nil
}
