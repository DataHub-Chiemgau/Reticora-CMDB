package ci

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

func f64(v float64) *float64 { return &v }

func serverKindDefs() []fieldmeta.FieldDefinition {
	return []fieldmeta.FieldDefinition{
		{
			Name:       "server_kind",
			DataType:   fieldmeta.TypeEnum,
			Required:   true,
			EnumValues: []string{"physical", "virtual"},
		},
		{
			Name:     "rack_unit",
			DataType: fieldmeta.TypeInteger,
			Conditional: &fieldmeta.ConditionalRules{
				VisibleWhen:  []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
				RequiredWhen: []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "physical"}},
			},
			Validation: &fieldmeta.ValidationRules{Min: f64(1), Max: f64(48)},
		},
		{
			Name:     "hypervisor",
			DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{
				VisibleWhen:  []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "virtual"}},
				RequiredWhen: []fieldmeta.Predicate{{Field: "server_kind", Op: "eq", Value: "virtual"}},
			},
		},
	}
}

func fieldsOf(err error) []string {
	ve, ok := AsValidationError(err)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(ve.Violations))
	for _, v := range ve.Violations {
		out = append(out, v.Field)
	}
	return out
}

func TestValidateAttributesRequiredField(t *testing.T) {
	err := validateAttributes(serverKindDefs(), map[string]any{}, map[string]any{}, nil)
	got := fieldsOf(err)
	if len(got) != 1 || got[0] != "server_kind" {
		t.Fatalf("expected server_kind required violation, got %v (err=%v)", got, err)
	}
}

func TestValidateAttributesConditionalRequired(t *testing.T) {
	values := map[string]any{"server_kind": "physical"}
	err := validateAttributes(serverKindDefs(), values, values, nil)
	if got := fieldsOf(err); len(got) != 1 || got[0] != "rack_unit" {
		t.Fatalf("expected rack_unit required for physical servers, got %v", got)
	}

	values = map[string]any{"server_kind": "virtual"}
	err = validateAttributes(serverKindDefs(), values, values, nil)
	if got := fieldsOf(err); len(got) != 1 || got[0] != "hypervisor" {
		t.Fatalf("expected hypervisor required for virtual servers, got %v", got)
	}
}

func TestValidateAttributesHiddenFieldIsNotRequired(t *testing.T) {
	// A virtual server must not be asked for rack_unit.
	values := map[string]any{"server_kind": "virtual", "hypervisor": "esx-01"}
	if err := validateAttributes(serverKindDefs(), values, values, nil); err != nil {
		t.Fatalf("expected no violations, got %v", err)
	}
}

func TestValidateAttributesRangeAndEnum(t *testing.T) {
	values := map[string]any{"server_kind": "physical", "rack_unit": 99}
	if got := fieldsOf(validateAttributes(serverKindDefs(), values, values, nil)); len(got) != 1 || got[0] != "rack_unit" {
		t.Fatalf("expected rack_unit range violation, got %v", got)
	}

	values = map[string]any{"server_kind": "container"}
	if got := fieldsOf(validateAttributes(serverKindDefs(), values, values, nil)); len(got) == 0 || got[0] != "server_kind" {
		t.Fatalf("expected server_kind enum violation, got %v", got)
	}
}

func TestValidateAttributesConditionalAllowedValues(t *testing.T) {
	defs := []fieldmeta.FieldDefinition{
		{Name: "env", DataType: fieldmeta.TypeText},
		{
			Name:     "tier",
			DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{
				AllowedValuesWhen: &fieldmeta.ConditionalValues{
					When:   []fieldmeta.Predicate{{Field: "env", Op: "eq", Value: "prod"}},
					Values: []any{"gold", "silver"},
				},
			},
		},
	}
	values := map[string]any{"env": "prod", "tier": "bronze"}
	if got := fieldsOf(validateAttributes(defs, values, values, nil)); len(got) != 1 || got[0] != "tier" {
		t.Fatalf("expected tier allowed-values violation, got %v", got)
	}
	values = map[string]any{"env": "dev", "tier": "bronze"}
	if err := validateAttributes(defs, values, values, nil); err != nil {
		t.Fatalf("tier is unconstrained outside prod, got %v", err)
	}
}

func TestValidateAttributesReadOnly(t *testing.T) {
	defs := []fieldmeta.FieldDefinition{
		{Name: "state", DataType: fieldmeta.TypeText},
		{
			Name:     "asset_tag",
			DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{
				ReadOnlyWhen: []fieldmeta.Predicate{{Field: "state", Op: "eq", Value: "disposed"}},
			},
		},
	}
	existing := map[string]any{"state": "disposed", "asset_tag": "A-1"}
	patch := map[string]any{"asset_tag": "A-2"}
	values := MergePatch(existing, patch)
	if got := fieldsOf(validateAttributes(defs, values, patch, existing)); len(got) != 1 || got[0] != "asset_tag" {
		t.Fatalf("expected asset_tag read-only violation, got %v", got)
	}
	// Re-sending the identical value is not a change and must be accepted.
	patch = map[string]any{"asset_tag": "A-1"}
	if err := validateAttributes(defs, MergePatch(existing, patch), patch, existing); err != nil {
		t.Fatalf("unchanged read-only value must pass, got %v", err)
	}
}

func TestValidateAttributesIgnoresUndefinedAttributes(t *testing.T) {
	values := map[string]any{"server_kind": "virtual", "hypervisor": "esx-01", "discovered_only": 42}
	if err := validateAttributes(serverKindDefs(), values, values, nil); err != nil {
		t.Fatalf("undefined attributes must not be rejected, got %v", err)
	}
}

func TestValidateAttributesNoDefinitionsIsNoop(t *testing.T) {
	if err := validateAttributes(nil, map[string]any{"anything": 1}, nil, nil); err != nil {
		t.Fatalf("expected no-op, got %v", err)
	}
}

// stubResolver returns fixed definitions regardless of scope.
type stubResolver struct{ defs []fieldmeta.FieldDefinition }

func (s stubResolver) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return s.defs, nil
}

func TestServiceCreateRejectsInvalidAttributes(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo).WithFieldResolver(stubResolver{defs: serverKindDefs()})

	item := &Item{OrganizationID: "org-1", CITypeID: "type-1", Name: "srv-01", Attributes: map[string]any{}}
	err := svc.Create(context.Background(), item)
	if _, ok := AsValidationError(err); !ok {
		t.Fatalf("expected validation error, got %v", err)
	}

	item.Attributes = map[string]any{"server_kind": "virtual", "hypervisor": "esx-01"}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("valid payload must be accepted, got %v", err)
	}
}
