package relationship

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// The attributes of an edge (the metadata of REL-02) against the attribute
// schema of its relationship type (REL-07, WP-295): POST /relationships
// checks the attributes of the request, PATCH /relationships/{id} the
// attributes after the merge, in the transaction and under the lock of the
// write (UpdateRequest.Check). The repositories do not check, so discovery
// edges stay as discovery reports them.

// ErrReservedAttribute refuses an attribute named like a field of the edge
// (relationshiptype.ReservedAttributeKeys); provenance is refused with
// ErrProvenanceReserved (F-270).
var ErrReservedAttribute = errors.New("the key is a field of the relationship and cannot be an attribute")

// reservedViolations returns a violation on attributes.<key> per reserved
// key of attrs (422, as attributes.provenance since WP-583), sorted.
func reservedViolations(attrs map[string]any) []violation {
	var out []violation
	for key := range attrs {
		if !slices.Contains(relationshiptype.ReservedAttributeKeys, key) {
			continue
		}
		detail := ErrReservedAttribute.Error()
		if key == ProvenanceAttribute {
			detail = ErrProvenanceReserved.Error()
		}
		out = append(out, violation{Field: "attributes." + key, Detail: detail})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// schemaError refuses attributes that violate the attribute schema of the
// relationship type: 422 with a violation per attribute key, sorted.
type schemaError struct {
	violations []violation
}

func (e *schemaError) Error() string {
	return "the attributes violate the metadata schema of the relationship type"
}

// writeSchemaError answers a schemaError with 422 (ViolationProblem) and
// reports whether err was one.
func writeSchemaError(w http.ResponseWriter, err error) bool {
	var se *schemaError
	if !errors.As(err, &se) {
		return false
	}
	writeViolations(w, se.violations...)
	return true
}

// hasSchemaSource reports whether the handler reads attribute schemas: its
// TypeChecker resolves types (TypeResolver, relationshiptype.Repository).
// Without one every type has the schema [].
func (h *Handler) hasSchemaSource() bool {
	_, ok := h.types.(TypeResolver)
	return ok
}

// metadataSchema returns the attribute schema of the relationship type key
// as the organization resolves it (its row before the system row,
// GetByKey). A type without a catalogue row has none. A read error is
// returned: there is no check without the schema (fail-closed, 500).
func (h *Handler) metadataSchema(ctx context.Context, orgID, key string) ([]fieldmeta.FieldDefinition, error) {
	resolver, ok := h.types.(TypeResolver)
	if !ok {
		return nil, nil
	}
	typ, err := resolver.GetByKey(ctx, orgID, key)
	switch {
	case errors.Is(err, relationshiptype.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read metadata schema of %s: %w", key, err)
	case typ == nil:
		return nil, nil
	}
	return relationshiptype.Fields(typ.MetadataSchema), nil
}

// checkMetadata checks values (the attributes after the write) against the
// schema of the relationship type relType; incoming are the attributes of
// the request, existing the stored ones (nil for a new edge). A violation
// is a *schemaError.
func (h *Handler) checkMetadata(ctx context.Context, orgID, relType string, values, incoming, existing map[string]any) error {
	defs, err := h.metadataSchema(ctx, orgID, relType)
	if err != nil {
		return err
	}
	if violations := metadataViolations(defs, values, incoming, existing); len(violations) > 0 {
		return &schemaError{violations: violations}
	}
	return nil
}

// mergedAttributes returns the attributes of the edge after the PATCH: the
// stored ones with the keys of the request set (as the repositories merge
// them, a null value stays as null).
func mergedAttributes(stored, patch map[string]any) map[string]any {
	out := maps.Clone(stored)
	if out == nil {
		out = make(map[string]any, len(patch))
	}
	maps.Copy(out, patch)
	return out
}

// metadataViolations checks the attributes as ci.validateAttributes checks
// CI attributes: per definition the state of its conditional rules
// (fieldmeta.Evaluate), then visible, read_only against the stored value,
// required, the type and the rule (fieldmeta.ValidateValue) and the allowed
// values. A key without definition is allowed. The violations name the
// attribute key and are sorted by it.
func metadataViolations(defs []fieldmeta.FieldDefinition, values, incoming, existing map[string]any) []violation {
	if values == nil {
		values = map[string]any{}
	}
	var out []violation
	for i := range defs {
		def := &defs[i]
		state := fieldmeta.Evaluate(def, values)
		if !state.Visible {
			// A hidden attribute carries no requirement.
			continue
		}
		if in, changed := incoming[def.Name]; changed && state.ReadOnly && !sameValue(existing[def.Name], in) {
			out = append(out, violation{Field: def.Name, Detail: "attribute is read-only in the current state"})
			continue
		}
		value, present := values[def.Name]
		if !present || isBlank(value) {
			if state.Required {
				out = append(out, violation{Field: def.Name, Detail: "attribute is required"})
			}
			continue
		}
		if err := fieldmeta.ValidateValue(def, value); err != nil {
			out = append(out, violation{Field: def.Name, Detail: strings.TrimPrefix(err.Error(), "field "+def.Name+": ")})
			continue
		}
		if len(state.AllowedValues) > 0 && !allowedValue(state.AllowedValues, value) {
			out = append(out, violation{Field: def.Name,
				Detail: "value is not allowed here, expected one of: " + strings.Join(state.AllowedValues, ", ")})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// isBlank reports an empty value: null, a blank string, an empty list or
// object (as ci.isBlank).
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
	}
	return false
}

// sameValue compares two attribute values by their text (as ci.sameValue).
func sameValue(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// allowedValue reports whether the value, or every element of a list, is
// one of allowed (as ci.allowedValue).
func allowedValue(allowed []string, value any) bool {
	check := func(v any) bool { return slices.Contains(allowed, fmt.Sprintf("%v", v)) }
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
