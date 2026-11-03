package citype

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// FieldLister is the subset of Repository needed to resolve the attribute
// layers.
type FieldLister interface {
	ListGlobalFields(ctx context.Context, orgID string) ([]Field, error)
	// TypeChain returns the chain root → typeID (MET-06), each type with
	// its own field definitions, read in one query.
	TypeChain(ctx context.Context, orgID, typeID string) ([]Type, error)
	// TypeCategory returns the category of typeID ("" when unknown); with
	// the chain it decides which of the organization's global attributes
	// apply (MET-13, WP-255).
	TypeCategory(ctx context.Context, orgID, typeID string) (string, error)
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
// the inheritable set attributes), one layer per template of the CI in
// position order (ci_template with its versions, WP-710: Template 1 … n)
// and the attributes of the single CI instance (MET-14).
// A narrower layer tightens a wider one and cannot drop its rules;
// conflicting definitions are a *CompositionError. The system,
// organization, type and set layers come from attribute_definition only
// (DB-03); the instance layer stays in ci_instance_field_definition
// (decision E-92, transitional rule option 2: MET-14 is [P2], WP-256).
//
// It implements ci.FieldResolver, which the CI service uses to validate
// attribute payloads server-side.
//
// The system's global attributes apply to every type, an organization's
// only to the types it applies to (applies_to, MET-13, WP-255): the
// category and the chain of the CI type decide (appliesTo). The category
// is read only while a global of the organization is restricted to
// categories.
//
// The template layer of a stored CI comes from ci_template, read with the
// instance definitions in one statement where the lister offers it
// (ciLayerLister); without it (MemoryRepository) a CI has no templates.
func ResolveFields(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	return resolveFields(ctx, r, orgID, ciTypeID, ciID, nil)
}

// resolveFields is ResolveFields; templates, when set, returns the template
// layer in place of the stored one (ResolveFieldsWithTemplates).
func resolveFields(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string,
	templates func(ctx context.Context) ([][]templateItem, error)) ([]fieldmeta.FieldDefinition, error) {
	layers, err := compositionLayers(ctx, r, orgID, ciTypeID, ciID, layerSource{templates: templates})
	if err != nil {
		return nil, err
	}
	return Compose(layers...)
}

// layerSource adjusts where compositionLayers takes two layers from.
type layerSource struct {
	// templates, when set, returns the template layer in place of the
	// stored ci_template rows of the CI.
	templates func(ctx context.Context) ([][]templateItem, error)
	// everyGlobal: without a type the organization layer holds every
	// global attribute of the organization, whatever it applies to; the
	// check of the global layers (CheckComposition without a type). With
	// a type only the applicable ones count, as always.
	everyGlobal bool
}

// compositionLayers is the one layer order of MET-16 (WP-252), shared by
// ResolveFields and CheckComposition: system base → organization's global
// attributes that apply to the type (MET-13, WP-255) → type chain root →
// type (MET-06, WP-248) → one layer per active set of the chain in
// position order (WP-709) → one layer per template in position order
// (WP-710; the item's definition where no lower layer has the key, then
// its overrides) → the instance definitions of the CI (MET-14). Layers
// without a type or a CI are empty.
func compositionLayers(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string,
	src layerSource) ([][]fieldmeta.FieldDefinition, error) {
	templates := src.templates
	globals, err := r.ListGlobalFields(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var chain []Type
	var setLayers [][]Field
	scope := typeScope{}
	if ciTypeID != "" {
		if chain, setLayers, err = chainAndSets(ctx, r, orgID, ciTypeID); err != nil {
			return nil, err
		}
		scope = scopeOfChain(chain)
		if slices.ContainsFunc(globals, func(f Field) bool { return !f.IsSystem && f.AppliesTo == AppliesToCategories }) {
			if scope.category, err = r.TypeCategory(ctx, orgID, ciTypeID); err != nil {
				return nil, err
			}
		}
	}
	var system, org []fieldmeta.FieldDefinition
	for i := range globals {
		f := &globals[i]
		switch {
		case f.IsSystem:
			system = append(system, f.Definition())
		case f.appliesTo(scope) || (src.everyGlobal && ciTypeID == ""):
			org = append(org, f.Definition())
		}
	}

	var typed []fieldmeta.FieldDefinition
	var sets [][]fieldmeta.FieldDefinition
	if ciTypeID != "" {
		typed = chainDefinitions(chain)
		for _, layer := range setLayers {
			defs := make([]fieldmeta.FieldDefinition, 0, len(layer))
			for i := range layer {
				defs = append(defs, layer[i].Definition())
			}
			sets = append(sets, defs)
		}
	}

	var (
		instanceFields []InstanceField
		assigned       [][]templateItem
	)
	cl, combined := r.(ciLayerLister)
	switch {
	case templates != nil:
		if assigned, err = templates(ctx); err != nil {
			return nil, err
		}
		if ciID != "" {
			instanceFields, err = r.ListInstanceFields(ctx, orgID, ciID)
		}
	case ciID != "" && combined:
		instanceFields, assigned, err = cl.ciLayers(ctx, orgID, ciID)
	case ciID != "":
		instanceFields, err = r.ListInstanceFields(ctx, orgID, ciID)
	}
	if err != nil {
		return nil, err
	}
	var instance []fieldmeta.FieldDefinition
	for i := range instanceFields {
		instance = append(instance, instanceFields[i].Definition())
	}
	layers := append([][]fieldmeta.FieldDefinition{system, org, typed}, sets...)
	templateLayer, err := templateLayers(layers, assigned)
	if err != nil {
		return nil, err
	}
	layers = append(layers, templateLayer...)
	return append(layers, instance), nil
}

// CompositionScope is what CheckComposition composes (WP-252):
//   - TypeID alone: a type — system base → the organization's global
//     attributes that apply to it → type chain → sets;
//   - TypeID with TemplateVersionIDs: the type and these template versions
//     (template_version.id) in the given order, as a CI with them would
//     compose (any status: the caller checks a version before publishing
//     it);
//   - CIID: a stored CI — its type (TypeID "" takes ci.ci_type_id), its
//     templates from ci_template in position order (TemplateVersionIDs,
//     when not nil, replace them) and its instance definitions;
//   - neither: the global layers — the system base and every global
//     attribute of the organization.
type CompositionScope struct {
	TypeID             string
	TemplateVersionIDs []string
	CIID               string
}

// errScopeCINotFound: the CI of a CompositionScope is not visible.
var errScopeCINotFound = errors.New("composition scope: ci not found")

// CheckComposition composes scope within tx (MET-16, WP-252), so it sees
// what the transaction wrote and has not committed (a draft), under the
// row level security of the transaction (database.WithRequestTenant, in
// jobs WithTenant). The layers are those of ResolveFields
// (compositionLayers, the one layer order): system base → organization's
// global attributes (applies_to, WP-255) → type chain root → type → sets
// in position order → templates in position order → instance. It returns
// every conflict (ComposeAll): per key the first, as *CompositionError
// with Key and Code (CodeDatatypeConflict, CodeEnumEmpty, CodeRangeEmpty,
// CodeLengthRangeEmpty); none is nil. The error is for a failed read or an
// unknown CI only. It is the contract of the test run and the publication
// of drafts (WP-721, WP-261) and of the template migration (WP-253).
func CheckComposition(ctx context.Context, tx pgx.Tx, scope CompositionScope) ([]*CompositionError, error) {
	return newTxLister(tx, nil).check(ctx, scope)
}

// firstConflict returns the first of conflicts whose key matches (nil:
// none); the callers report it as a 422 with its code.
// validation: the *CompositionError texts of Compose (MET-16), naming the key.
func firstConflict(conflicts []*CompositionError, matches func(key string) bool) *CompositionError {
	for _, c := range conflicts {
		if matches(c.Key) {
			return c
		}
	}
	return nil
}

// txLister is the FieldLister of CheckComposition: it reads the layers in
// a transaction of the caller, the chain and set layers with an optional
// replaced set list of one type (setOverride, before it is written). The
// global attributes are read once per lister.
type txLister struct {
	tx       pgx.Tx
	override *setOverride
	globals  []Field
	read     bool
}

func newTxLister(tx pgx.Tx, override *setOverride) *txLister {
	return &txLister{tx: tx, override: override}
}

// check composes scope (CheckComposition).
func (l *txLister) check(ctx context.Context, scope CompositionScope) ([]*CompositionError, error) {
	orgID, typeID := "", scope.TypeID
	if scope.CIID != "" {
		if !api.IsUUID(scope.CIID) {
			return nil, errScopeCINotFound
		}
		var ciType string
		err := l.tx.QueryRow(ctx, `SELECT ci_type_id::text, organization_id::text FROM ci WHERE id = $1`,
			scope.CIID).Scan(&ciType, &orgID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, errScopeCINotFound
		case err != nil:
			return nil, fmt.Errorf("read ci of composition scope: %w", err)
		}
		if typeID == "" {
			typeID = ciType
		}
	}
	src := layerSource{everyGlobal: true}
	if scope.TemplateVersionIDs != nil {
		ids := scope.TemplateVersionIDs
		src.templates = func(ctx context.Context) ([][]templateItem, error) {
			return templateVersionLayersTx(ctx, l.tx, ids)
		}
	}
	layers, err := compositionLayers(ctx, l, orgID, typeID, scope.CIID, src)
	if err != nil {
		return nil, err
	}
	_, conflicts := ComposeAll(layers...)
	return conflicts, nil
}

// ListGlobalFields implements FieldLister: the system's and the
// organization's global attributes, read once.
func (l *txLister) ListGlobalFields(ctx context.Context, _ string) ([]Field, error) {
	if !l.read {
		globals, err := listFieldsScopeTx(ctx, l.tx, "", "global")
		if err != nil {
			return nil, err
		}
		l.globals, l.read = globals, true
	}
	return l.globals, nil
}

// TypeChainSets implements chainSetLister.
func (l *txLister) TypeChainSets(ctx context.Context, _, typeID string) ([]Type, [][]Field, error) {
	return typeChainSetsTx(ctx, l.tx, typeID, l.override)
}

// TypeChain implements FieldLister.
func (l *txLister) TypeChain(ctx context.Context, orgID, typeID string) ([]Type, error) {
	chain, _, err := l.TypeChainSets(ctx, orgID, typeID)
	return chain, err
}

// ListSetFields implements FieldLister.
func (l *txLister) ListSetFields(ctx context.Context, orgID, typeID string) ([][]Field, error) {
	_, sets, err := l.TypeChainSets(ctx, orgID, typeID)
	return sets, err
}

// TypeCategory implements FieldLister.
func (l *txLister) TypeCategory(ctx context.Context, _, typeID string) (string, error) {
	return typeCategoryTx(ctx, l.tx, typeID)
}

// ListInstanceFields implements FieldLister.
func (l *txLister) ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error) {
	fields, _, err := ciLayersTx(ctx, l.tx, orgID, ciID)
	return fields, err
}

// ciLayers implements ciLayerLister.
func (l *txLister) ciLayers(ctx context.Context, orgID, ciID string) ([]InstanceField, [][]templateItem, error) {
	return ciLayersTx(ctx, l.tx, orgID, ciID)
}

// typeScope is what decides whether an organization's global attribute
// applies to a CI type (MET-13, WP-255): the type's category and the IDs
// of its chain root → type (MET-06). The zero value is no type.
type typeScope struct {
	category string
	chain    []string
}

// scopeOfChain is the scope of the last type of a chain root → type, with
// the category the chain carries (MemoryRepository; the PostgreSQL chain
// carries none, ResolveFields reads it with TypeCategory).
func scopeOfChain(chain []Type) typeScope {
	var s typeScope
	if len(chain) == 0 {
		return s
	}
	s.category = chain[len(chain)-1].Category
	for i := range chain {
		s.chain = append(s.chain, chain[i].ID)
	}
	return s
}

// appliesToScope is the one rule of applicability (MET-13, WP-255): mode
// all (or empty) applies to every type; categories when the type's
// category is listed; types when the type or one of its ancestors is
// listed, so subtypes are included. IDs of types that no longer exist
// match nothing. Without a type only mode all applies.
func appliesToScope(mode string, categories, typeIDs []string, s typeScope) bool {
	switch mode {
	case "", AppliesToAll:
		return true
	case AppliesToCategories:
		return s.category != "" && slices.Contains(categories, s.category)
	case AppliesToTypes:
		for _, id := range s.chain {
			if slices.ContainsFunc(typeIDs, func(t string) bool { return strings.EqualFold(t, id) }) {
				return true
			}
		}
	}
	return false
}

// appliesTo reports whether the global field applies to the type of s.
func (f *Field) appliesTo(s typeScope) bool {
	return appliesToScope(f.AppliesTo, f.AppliesToCategories, f.AppliesToTypeIDs, s)
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
