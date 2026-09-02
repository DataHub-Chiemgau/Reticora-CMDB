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

// mergeAttributes returns the effective attribute map after applying patch on
// top of base, mirroring the `attributes || $n` merge the repository performs.
func mergeAttributes(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		out[k] = v
	}
	return out
}
