package citype

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// FieldLister is the subset of Repository needed to resolve the three
// attribute scopes.
type FieldLister interface {
	ListGlobalFields(ctx context.Context, orgID string) ([]Field, error)
	ListFields(ctx context.Context, orgID, typeID string) ([]Field, error)
	ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error)
}

// ResolveFields returns the effective field definitions for a CI: the
// layers composed by the rules of MET-16 (Compose), the base first — the
// system's global attributes (the system base, materialized with the type
// schema), the organization's global attributes (MET-13), the attributes
// of the CI type and those of the single CI instance (MET-14). A narrower
// layer tightens a wider one and cannot drop its rules; conflicting
// definitions are a *CompositionError. The system, organization and type
// layers come from attribute_definition only (DB-03); the instance layer
// stays in ci_instance_field_definition (decision E-92, transitional rule
// option 2: MET-14 is [P2], WP-256).
//
// It implements ci.FieldResolver, which the CI service uses to validate
// attribute payloads server-side.
func ResolveFields(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	globals, err := r.ListGlobalFields(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var system, org []fieldmeta.FieldDefinition
	for _, f := range globals {
		if f.IsSystem {
			system = append(system, f.Definition())
		} else {
			org = append(org, f.Definition())
		}
	}

	var typed []fieldmeta.FieldDefinition
	if ciTypeID != "" {
		typeFields, err := r.ListFields(ctx, orgID, ciTypeID)
		if err != nil {
			return nil, err
		}
		for _, f := range typeFields {
			typed = append(typed, f.Definition())
		}
	}

	var instance []fieldmeta.FieldDefinition
	if ciID != "" {
		instanceFields, err := r.ListInstanceFields(ctx, orgID, ciID)
		if err != nil {
			return nil, err
		}
		for _, f := range instanceFields {
			instance = append(instance, f.Definition())
		}
	}
	return Compose(system, org, typed, instance)
}

// ResolveFields implements ci.FieldResolver for the PostgreSQL repository.
func (r *PGRepository) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return ResolveFields(ctx, r, orgID, ciTypeID, ciID)
}

// ResolveFields implements ci.FieldResolver for the in-memory repository.
func (r *MemoryRepository) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return ResolveFields(ctx, r, orgID, ciTypeID, ciID)
}
