package citype

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// FieldLister is the subset of Repository needed to resolve the attribute
// layers.
type FieldLister interface {
	ListGlobalFields(ctx context.Context, orgID string) ([]Field, error)
	// TypeChain returns the chain root → typeID (MET-06), each type with
	// its own field definitions, read in one query.
	TypeChain(ctx context.Context, orgID, typeID string) ([]Type, error)
	// ListSetFields returns one layer per active attribute set of the
	// chain root → typeID (MET-16, WP-709): the sets of each type in
	// position order, in the set by sort_order; of the sets of an ancestor
	// only the inheritable set attributes.
	ListSetFields(ctx context.Context, orgID, typeID string) ([][]Field, error)
	ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error)
}

// ResolveFields returns the effective field definitions for a CI: the
// layers composed by the rules of MET-16 (Compose), the base first — the
// system's global attributes (the system base, materialized with the type
// schema), the organization's global attributes (MET-13), the type chain
// root → type (MET-06: of the ancestors only the inheritable definitions,
// of the type all), the active attribute sets of that chain, one layer per
// set in chain and position order (WP-709; of the sets of an ancestor only
// the inheritable set attributes), and the attributes of the single CI
// instance (MET-14).
// A narrower layer tightens a wider one and cannot drop its rules;
// conflicting definitions are a *CompositionError. The system,
// organization, type and set layers come from attribute_definition only
// (DB-03); the instance layer stays in ci_instance_field_definition
// (decision E-92, transitional rule option 2: MET-14 is [P2], WP-256).
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
	var sets [][]fieldmeta.FieldDefinition
	if ciTypeID != "" {
		chain, setLayers, err := chainAndSets(ctx, r, orgID, ciTypeID)
		if err != nil {
			return nil, err
		}
		typed = chainDefinitions(chain)
		for _, layer := range setLayers {
			defs := make([]fieldmeta.FieldDefinition, 0, len(layer))
			for i := range layer {
				defs = append(defs, layer[i].Definition())
			}
			sets = append(sets, defs)
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
	layers := append([][]fieldmeta.FieldDefinition{system, org, typed}, sets...)
	return Compose(append(layers, instance)...)
}

// chainSetLister is a FieldLister that reads the type chain and its set
// layers in one statement (PGRepository.TypeChainSets).
type chainSetLister interface {
	TypeChainSets(ctx context.Context, orgID, typeID string) ([]Type, [][]Field, error)
}

// chainAndSets returns the type chain and the set layers of typeID, in one
// read where the lister offers it.
func chainAndSets(ctx context.Context, r FieldLister, orgID, typeID string) ([]Type, [][]Field, error) {
	if cs, ok := r.(chainSetLister); ok {
		return cs.TypeChainSets(ctx, orgID, typeID)
	}
	chain, err := r.TypeChain(ctx, orgID, typeID)
	if err != nil {
		return nil, nil, err
	}
	sets, err := r.ListSetFields(ctx, orgID, typeID)
	return chain, sets, err
}

// chainDefinitions lists the definitions of a chain root → type in layer
// order: of the ancestors the inheritable ones, of the last type all.
// Compose merges a key that several levels define in this order.
func chainDefinitions(chain []Type) []fieldmeta.FieldDefinition {
	var out []fieldmeta.FieldDefinition
	for i := range chain {
		own := i == len(chain)-1
		fields := chain[i].Fields
		for j := range fields {
			if own || fields[j].Inheritable {
				out = append(out, fields[j].Definition())
			}
		}
	}
	return out
}

// ResolveFields implements ci.FieldResolver for the PostgreSQL repository.
func (r *PGRepository) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return ResolveFields(ctx, r, orgID, ciTypeID, ciID)
}

// ResolveFields implements ci.FieldResolver for the in-memory repository.
func (r *MemoryRepository) ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return ResolveFields(ctx, r, orgID, ciTypeID, ciID)
}
