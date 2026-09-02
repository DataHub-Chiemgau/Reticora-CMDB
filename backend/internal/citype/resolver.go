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

// ResolveFields returns the effective field definitions for a CI, merging the
// three attribute scopes in increasing precedence: organization-global
// attributes, the attributes of the CI type, and the attributes defined on the
// single CI instance (spec §3). A definition in a narrower scope replaces the
// wider-scoped definition with the same name, so an instance can tighten — but
// never silently drop — a type-level rule.
//
// It implements ci.FieldResolver, which the CI service uses to validate
// attribute payloads server-side.
func ResolveFields(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	order := make([]string, 0, 16)
	byName := make(map[string]fieldmeta.FieldDefinition, 16)
	add := func(def fieldmeta.FieldDefinition) {
		if _, seen := byName[def.Name]; !seen {
			order = append(order, def.Name)
		}
		byName[def.Name] = def
	}

	globals, err := r.ListGlobalFields(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, f := range globals {
		add(f.Definition())
	}

	if ciTypeID != "" {
		typeFields, err := r.ListFields(ctx, orgID, ciTypeID)
		if err != nil {
			return nil, err
		}
		for _, f := range typeFields {
			add(f.Definition())
		}
	}

	if ciID != "" {
		instanceFields, err := r.ListInstanceFields(ctx, orgID, ciID)
		if err != nil {
			return nil, err
		}
		for _, f := range instanceFields {
			add(f.Definition())
		}
	}

	out := make([]fieldmeta.FieldDefinition, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}

// ResolveFields implements ci.FieldResolver for the PostgreSQL repository.
func (r *PGRepository) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return ResolveFields(ctx, r, orgID, ciTypeID, ciID)
}

// ResolveFields implements ci.FieldResolver for the in-memory repository.
func (r *MemoryRepository) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return ResolveFields(ctx, r, orgID, ciTypeID, ciID)
}
