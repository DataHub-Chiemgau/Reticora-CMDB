package citype

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// CompositionError reports definitions of one key that cannot be composed
// (MET-16): publishing them is rejected.
type CompositionError struct {
	Key    string
	Reason string
}

func (e *CompositionError) Error() string {
	return fmt.Sprintf("attribute %s: %s (MET-16)", e.Key, e.Reason)
}

// CompositionKey names the attribute in conflict; ci reports it as the
// violated field without importing this package.
func (e *CompositionError) CompositionKey() string { return e.Key }

// Compose composes the effective schema from layers, the base first
// (MET-16): system base → type chain → (phase 2: sets, templates) →
// instance. Definitions of the same key are merged: required is OR-ed,
// the allowed values are intersected (empty → error), min/max and the
// length bounds take the narrowest range (empty → error), all patterns
// apply (allOf), the default and the presentation of the last layer that
// sets them win; a different data type is an error. Keys keep the order
// of their first appearance.
func Compose(layers ...[]fieldmeta.FieldDefinition) ([]fieldmeta.FieldDefinition, error) {
	order := make([]string, 0, 16)
	byName := make(map[string]fieldmeta.FieldDefinition, 16)
	for _, layer := range layers {
		for i := range layer {
			def := layer[i]
			prev, seen := byName[def.Name]
			if !seen {
				order = append(order, def.Name)
				byName[def.Name] = def
				continue
			}
			merged, err := composeField(&prev, &def)
			if err != nil {
				return nil, err
			}
			byName[def.Name] = merged
		}
	}
	out := make([]fieldmeta.FieldDefinition, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}

// referenceTarget is the target of a reference-typed definition; the
// specific type names (user, ci_ref, …) name their target.
func referenceTarget(def *fieldmeta.FieldDefinition) string {
	if fieldmeta.CanonicalType(def.DataType) != fieldmeta.TypeReference {
		return ""
	}
	if def.DataType != fieldmeta.TypeReference {
		return strings.TrimSuffix(def.DataType, "_ref")
	}
	if def.ReferenceTarget == "" {
		return "ci"
	}
	return def.ReferenceTarget
}

// allowedValues are the values a definition permits; nil means any. The
// options of the validation take precedence, as in fieldmeta.Rule.
func allowedValues(def *fieldmeta.FieldDefinition) []string {
	if def.Validation != nil && len(def.Validation.Options) > 0 {
		return def.Validation.Options
	}
	if len(def.EnumValues) > 0 {
		return def.EnumValues
	}
	return nil
}

func composeField(base, top *fieldmeta.FieldDefinition) (fieldmeta.FieldDefinition, error) {
	conflict := func(format string, args ...any) error {
		return &CompositionError{Key: top.Name, Reason: fmt.Sprintf(format, args...)}
	}
	if fieldmeta.CanonicalType(base.DataType) != fieldmeta.CanonicalType(top.DataType) ||
		referenceTarget(base) != referenceTarget(top) {
		return fieldmeta.FieldDefinition{}, conflict("data type %s conflicts with %s", top.DataType, base.DataType)
	}
	out := *top
	out.Required = base.Required || top.Required
	if out.DefaultValue == "" {
		out.DefaultValue = base.DefaultValue
	}
	if out.Label == "" {
		out.Label = base.Label
	}
	if out.Description == "" {
		out.Description = base.Description
	}
	if out.UIGroup == "" {
		out.UIGroup = base.UIGroup
	}
	if out.Conditional == nil {
		out.Conditional = base.Conditional
	}

	allowed := allowedValues(base)
	if more := allowedValues(top); more != nil {
		if allowed == nil {
			allowed = more
		} else {
			var both []string
			for _, v := range allowed {
				if slices.Contains(more, v) {
					both = append(both, v)
				}
			}
			if len(both) == 0 {
				return fieldmeta.FieldDefinition{}, conflict("no allowed value is left")
			}
			allowed = both
		}
	}

	var v fieldmeta.ValidationRules
	b, t := base.Validation, top.Validation
	if b == nil {
		b = &fieldmeta.ValidationRules{}
	}
	if t == nil {
		t = &fieldmeta.ValidationRules{}
	}
	v.Min, v.Max = narrowest(b.Min, t.Min, maxOf), narrowest(b.Max, t.Max, minOf)
	if v.Min != nil && v.Max != nil && *v.Min > *v.Max {
		return fieldmeta.FieldDefinition{}, conflict("the value range is empty")
	}
	v.MinLength, v.MaxLength = narrowest(b.MinLength, t.MinLength, maxOf), narrowest(b.MaxLength, t.MaxLength, minOf)
	if v.MinLength != nil && v.MaxLength != nil && *v.MinLength > *v.MaxLength {
		return fieldmeta.FieldDefinition{}, conflict("the length range is empty")
	}
	for _, p := range append(b.AllPatterns(), t.AllPatterns()...) {
		switch {
		case p == v.Pattern || slices.Contains(v.Patterns, p):
		case v.Pattern == "":
			v.Pattern = p
		default:
			v.Patterns = append(v.Patterns, p)
		}
	}
	out.EnumValues = nil
	if allowed != nil {
		switch fieldmeta.CanonicalType(out.DataType) {
		case fieldmeta.TypeEnum, fieldmeta.TypeMultiselect:
			out.EnumValues = allowed
		default:
			v.Options = allowed
		}
	}
	out.Validation = nil
	if v.Min != nil || v.Max != nil || v.MinLength != nil || v.MaxLength != nil || v.Pattern != "" || len(v.Options) > 0 {
		out.Validation = &v
	}
	return out, nil
}

type number interface{ ~int | ~float64 }

func maxOf[T number](a, b T) T { return max(a, b) }
func minOf[T number](a, b T) T { return min(a, b) }

// narrowest combines two optional bounds: the one that is set, or the
// stricter of both.
func narrowest[T number](a, b *T, pick func(T, T) T) *T {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	r := pick(*a, *b)
	return &r
}
