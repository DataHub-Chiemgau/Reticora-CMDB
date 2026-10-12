package ci

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// FieldResolver returns the effective field definitions that apply to a CI:
// global attributes, the attributes of its CI type and — when ciID is
// non-empty — the instance-specific attributes of that single CI (spec §3).
// Later scopes override earlier ones by field name.
type FieldResolver interface {
	ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error)
}

// ValidationError reports one or more attribute values that violate the field
// metadata of the CI type. Handlers map it onto HTTP 422.
type ValidationError struct {
	Violations []Violation
}

// Violation is a single rejected field.
type Violation struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Violations))
	for _, v := range e.Violations {
		parts = append(parts, fmt.Sprintf("%s: %s", v.Field, v.Detail))
	}
	return strings.Join(parts, "; ")
}

// AsValidationError reports whether err is (or wraps) a *ValidationError.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

// validateAttributes enforces the field metadata server-side. UI-side
// evaluation of the same rules is a convenience only: every rule is re-checked
// here so a direct API call cannot bypass required fields, value domains,
// conditional requirements or read-only fields.
//
// values is the effective attribute map after the write (existing attributes
// merged with the incoming patch); incoming lists the attribute names the
// request actually carries, which is what read-only enforcement keys on.
// Attributes without a definition are left untouched: discovery and
// integrations legitimately store attributes the metadata does not describe.
func validateAttributes(defs []fieldmeta.FieldDefinition, values map[string]any, incoming map[string]any, existing map[string]any) error {
	if len(defs) == 0 {
		return nil
	}
	if values == nil {
		values = map[string]any{}
	}
	var violations []Violation
	for _, def := range defs {
		state := fieldmeta.Evaluate(def, values)
		value, present := values[def.Name]

		if !state.Visible {
			// A hidden field carries no requirement and its stored value is
			// not asserted against the rules of the branch it belongs to.
			continue
		}

		if state.ReadOnly {
			if _, changed := incoming[def.Name]; changed && !sameValue(existing[def.Name], incoming[def.Name]) {
				violations = append(violations, Violation{def.Name, "field is read-only in the current state"})
				continue
			}
		}

		if state.Required && (!present || isBlank(value)) {
			violations = append(violations, Violation{def.Name, "field is required"})
			continue
		}
		if !present || isBlank(value) {
			continue
		}
		if err := fieldmeta.ValidateValue(def, value); err != nil {
			violations = append(violations, Violation{def.Name, trimFieldPrefix(def.Name, err.Error())})
			continue
		}
		if len(state.AllowedValues) > 0 && !allowedValue(state.AllowedValues, value) {
			violations = append(violations, Violation{
				def.Name,
				"value is not allowed here, expected one of: " + strings.Join(state.AllowedValues, ", "),
			})
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Field < violations[j].Field })
	return &ValidationError{Violations: violations}
}

// trimFieldPrefix removes the "field <name>: " prefix fieldmeta adds, because
// the field name is already carried by the violation.
func trimFieldPrefix(name, msg string) string {
	return strings.TrimPrefix(msg, "field "+name+": ")
}

func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

func sameValue(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func allowedValue(allowed []string, value any) bool {
	check := func(v any) bool {
		s := fmt.Sprintf("%v", v)
		for _, a := range allowed {
			if a == s {
				return true
			}
		}
		return false
	}
	if list, ok := value.([]any); ok {
		for _, v := range list {
			if !check(v) {
				return false
			}
		}
		return true
	}
	return check(value)
}

// Reserved namespaces in attributes (CI-10, GLO-13). attributes holds the
// business keys without prefix and these four namespaces: _overrides,
// _observed and _provenance are projected from the field provenance by the
// database, _instance holds the values of the CI's instance attributes
// (MET-14). Only reconciliation and the override code write them; as a key
// of a write they are rejected.
const (
	NamespaceOverrides  = "_overrides"
	NamespaceObserved   = "_observed"
	NamespaceProvenance = "_provenance"
	NamespaceInstance   = "_instance"
)

// ReservedNamespaces lists the four reserved attribute keys.
var ReservedNamespaces = []string{NamespaceOverrides, NamespaceObserved, NamespaceProvenance, NamespaceInstance}

// IsReservedKey reports whether key is a reserved namespace (GLO-13).
func IsReservedKey(key string) bool {
	return fieldmeta.ReservedNames[key]
}

// ReservedKeyError reports a write that uses reserved attribute keys; the
// handlers answer 400 with the field.
type ReservedKeyError struct {
	Fields []string
}

func (e *ReservedKeyError) Error() string {
	return strings.Join(e.Fields, ", ") + ": reserved namespace, not allowed as an attribute key (GLO-13)"
}

// CheckReservedKeys rejects attributes that use a reserved namespace as a
// key; prefix names the attribute map in the error ("attributes").
func CheckReservedKeys(prefix string, attributes map[string]any) error {
	var fields []string
	for key := range attributes {
		if IsReservedKey(key) {
			fields = append(fields, prefix+"."+key)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	sort.Strings(fields)
	return &ReservedKeyError{Fields: fields}
}

// presentAttributes is the read view of stored attributes: the projected
// namespaces are left out (the resolved per-field view is the effective
// field view, REC-10) and the instance attributes appear under their names.
func presentAttributes(stored map[string]any) map[string]any {
	out := make(map[string]any, len(stored))
	for k, v := range stored {
		if !IsReservedKey(k) {
			out[k] = v
		}
	}
	if instance, ok := stored[NamespaceInstance].(map[string]any); ok {
		for k, v := range instance {
			out[k] = v
		}
	}
	return out
}

// storedAttributes is the write form of attributes: the values of the
// instance attributes named in instance move into _instance. The projected
// namespaces are kept by the statement that writes the row.
func storedAttributes(attributes map[string]any, instance []string) map[string]any {
	out := make(map[string]any, len(attributes))
	nested := map[string]any{}
	isInstance := make(map[string]bool, len(instance))
	for _, name := range instance {
		isInstance[name] = true
	}
	for k, v := range attributes {
		switch {
		case IsReservedKey(k):
		case isInstance[k]:
			nested[k] = v
		default:
			out[k] = v
		}
	}
	if len(nested) > 0 {
		out[NamespaceInstance] = nested
	}
	return out
}
