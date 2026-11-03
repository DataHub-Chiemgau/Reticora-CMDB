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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
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
	defs, _, err := resolveFields(ctx, r, orgID, ciTypeID, ciID, nil)
	return defs, err
}

// templateSource returns the template layer of a CI in place of the stored
// one (ResolveFieldsWithTemplates) and the rule scope of its versions.
type templateSource func(ctx context.Context) ([][]templateItem, ruleScope, error)

// resolveFields is ResolveFields; templates, when set, returns the template
// layer in place of the stored one (ResolveFieldsWithTemplates). It also
// returns the rule scope of the CI (WP-257), read with the layers.
func resolveFields(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string,
	templates templateSource) ([]fieldmeta.FieldDefinition, ruleScope, error) {
	layers, rs, err := compositionLayers(ctx, r, orgID, ciTypeID, ciID, layerSource{templates: templates})
	if err != nil {
		return nil, rs, err
	}
	defs, err := Compose(layers...)
	return defs, rs, err
}

// layerSource adjusts where compositionLayers takes two layers from.
type layerSource struct {
	// templates, when set, returns the template layer in place of the
	// stored ci_template rows of the CI.
	templates templateSource
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
// without a type or a CI are empty. It also returns the rule scope of the
// CI (WP-257), read with the chain and the template layer.
func compositionLayers(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string,
	src layerSource) ([][]fieldmeta.FieldDefinition, ruleScope, error) {
	templates := src.templates
	var rs ruleScope
	globals, err := r.ListGlobalFields(ctx, orgID)
	if err != nil {
		return nil, rs, err
	}
	var chain []Type
	var setLayers [][]Field
	scope := typeScope{}
	if ciTypeID != "" {
		if chain, setLayers, rs, err = chainAndSets(ctx, r, orgID, ciTypeID); err != nil {
			return nil, rs, err
		}
		scope = scopeOfChain(chain)
		if slices.ContainsFunc(globals, func(f Field) bool { return !f.IsSystem && f.AppliesTo == AppliesToCategories }) {
			if scope.category, err = r.TypeCategory(ctx, orgID, ciTypeID); err != nil {
				return nil, rs, err
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
		versions       ruleScope
	)
	cl, combined := r.(ciLayerLister)
	switch {
	case templates != nil:
		if assigned, versions, err = templates(ctx); err != nil {
			return nil, rs, err
		}
		if ciID != "" {
			instanceFields, err = r.ListInstanceFields(ctx, orgID, ciID)
		}
	case ciID != "" && combined:
		instanceFields, assigned, versions, err = cl.ciLayers(ctx, orgID, ciID)
	case ciID != "":
		instanceFields, err = r.ListInstanceFields(ctx, orgID, ciID)
	}
	if err != nil {
		return nil, rs, err
	}
	rs.versions, rs.active = versions.versions, rs.active || versions.active
	var instance []fieldmeta.FieldDefinition
	for i := range instanceFields {
		instance = append(instance, instanceFields[i].Definition())
	}
	layers := append([][]fieldmeta.FieldDefinition{system, org, typed}, sets...)
	templateLayer, err := templateLayers(layers, assigned)
	if err != nil {
		return nil, rs, err
	}
	layers = append(layers, templateLayer...)
	return append(layers, instance), rs, nil
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
		src.templates = func(ctx context.Context) ([][]templateItem, ruleScope, error) {
			layers, err := templateVersionLayersTx(ctx, l.tx, ids)
			return layers, ruleScope{}, err
		}
	}
	layers, _, err := compositionLayers(ctx, l, orgID, typeID, scope.CIID, src)
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

// typeChainLayers implements chainSetLister.
func (l *txLister) typeChainLayers(ctx context.Context, _, typeID string) ([]Type, [][]Field, ruleScope, error) {
	return typeChainSetsTx(ctx, l.tx, typeID, l.override)
}

// TypeChain implements FieldLister.
func (l *txLister) TypeChain(ctx context.Context, orgID, typeID string) ([]Type, error) {
	chain, _, _, err := l.typeChainLayers(ctx, orgID, typeID)
	return chain, err
}

// ListSetFields implements FieldLister.
func (l *txLister) ListSetFields(ctx context.Context, orgID, typeID string) ([][]Field, error) {
	_, sets, _, err := l.typeChainLayers(ctx, orgID, typeID)
	return sets, err
}

// TypeCategory implements FieldLister.
func (l *txLister) TypeCategory(ctx context.Context, _, typeID string) (string, error) {
	return typeCategoryTx(ctx, l.tx, typeID)
}

// ListInstanceFields implements FieldLister.
func (l *txLister) ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error) {
	fields, _, _, err := ciLayersTx(ctx, l.tx, orgID, ciID)
	return fields, err
}

// ciLayers implements ciLayerLister.
func (l *txLister) ciLayers(ctx context.Context, orgID, ciID string) ([]InstanceField, [][]templateItem, ruleScope, error) {
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

// chainSetLister is a FieldLister that reads the type chain, its set
// layers and its rule scope in one statement (PGRepository.TypeChainSets).
type chainSetLister interface {
	typeChainLayers(ctx context.Context, orgID, typeID string) ([]Type, [][]Field, ruleScope, error)
}

// chainAndSets returns the type chain, the set layers and the rule scope
// of typeID, in one read where the lister offers it. Without it the scope
// names the chain only and no rules (MemoryRepository keeps none).
func chainAndSets(ctx context.Context, r FieldLister, orgID, typeID string) ([]Type, [][]Field, ruleScope, error) {
	if cs, ok := r.(chainSetLister); ok {
		return cs.typeChainLayers(ctx, orgID, typeID)
	}
	var rs ruleScope
	chain, err := r.TypeChain(ctx, orgID, typeID)
	if err != nil {
		return nil, nil, rs, err
	}
	for i := range chain {
		rs.types = append(rs.types, chain[i].ID)
	}
	sets, err := r.ListSetFields(ctx, orgID, typeID)
	return chain, sets, rs, err
}

// ruleScope is what the catalog rules of a CI target (WP-257, E-111): the
// types of its chain root → type, the active sets of each type in chain
// and position order, the template versions of the CI in position order,
// and whether an active rule the organization sees targets one of them.
// It is read with the layers, so a resolution without rules reads none.
type ruleScope struct {
	types, sets, versions []string
	active                bool
}

// ResolveRules returns the active catalog rules (metamodel_rule, WP-716)
// that apply to a CI of ciTypeID (WP-257, MET-10) in a fixed order: the
// rules of the type chain from the root to the type (WP-248), then those
// of the sets of each type of the chain by position (WP-250), then those
// of the CI's template versions by position (ci_template, WP-710); within
// one target by key. They are read through RuleStore.ActiveRules, so a
// lister without a RuleStore (MemoryRepository) has no rules. Unlike the
// field definitions they are not composed: every rule applies, AND-ed with
// the attribute rules (ci.Service).
func ResolveRules(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string) ([]fieldmeta.CatalogRule, error) {
	var rs ruleScope
	if ciTypeID != "" {
		var err error
		if _, _, rs, err = chainAndSets(ctx, r, orgID, ciTypeID); err != nil {
			return nil, err
		}
	}
	if cl, ok := r.(ciLayerLister); ok && ciID != "" {
		_, _, versions, err := cl.ciLayers(ctx, orgID, ciID)
		if err != nil {
			return nil, err
		}
		rs.versions, rs.active = versions.versions, rs.active || versions.active
	}
	return catalogRules(ctx, r, orgID, &rs)
}

// ResolveFieldsAndRules returns what ResolveFields and ResolveRules return
// in one pass over the layers: the rule scope is read with the chain and
// the template layer, the rules with one statement more only when an
// active rule targets the CI (NFR-03, WP-333). templates, when set,
// replaces the stored template list as in ResolveFieldsWithTemplates.
func ResolveFieldsAndRules(ctx context.Context, r FieldLister, orgID, ciTypeID, ciID string,
	templates templateSource) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	defs, rs, err := resolveFields(ctx, r, orgID, ciTypeID, ciID, templates)
	if err != nil {
		return nil, nil, err
	}
	catalog, err := catalogRules(ctx, r, orgID, &rs)
	if err != nil {
		return nil, nil, err
	}
	return defs, catalog, nil
}

// catalogRules reads the active rules of the scope through the RuleStore
// of r and orders them by target: type chain, sets, template versions.
func catalogRules(ctx context.Context, r FieldLister, orgID string, rs *ruleScope) ([]fieldmeta.CatalogRule, error) {
	store, ok := r.(RuleStore)
	if !ok || !rs.active {
		return nil, nil
	}
	rank := map[string]int{}
	for _, id := range slices.Concat(rs.types, rs.sets, rs.versions) {
		if _, seen := rank[id]; !seen {
			rank[id] = len(rank)
		}
	}
	found, err := store.ActiveRules(ctx, orgID, RuleTargets{CITypeIDs: rs.types, AttributeSetIDs: rs.sets, TemplateVersionIDs: rs.versions})
	if err != nil {
		return nil, err
	}
	target := func(m *MetamodelRule) int {
		for _, id := range []string{m.CITypeID, m.AttributeSetID, m.TemplateVersionID} {
			if pos, known := rank[id]; known && id != "" {
				return pos
			}
		}
		return len(rank)
	}
	slices.SortStableFunc(found, func(a, b MetamodelRule) int { return target(&a) - target(&b) })
	out := make([]fieldmeta.CatalogRule, 0, len(found))
	for i := range found {
		rule, cerr := found[i].catalogRule()
		if cerr != nil {
			return nil, cerr
		}
		out = append(out, rule)
	}
	return out, nil
}

// catalogRule is the neutral form of a stored rule (fieldmeta.CatalogRule)
// with its condition and value rule parsed.
func (m *MetamodelRule) catalogRule() (fieldmeta.CatalogRule, error) {
	out := fieldmeta.CatalogRule{Key: m.Key, AttributeKey: m.AttributeKey, Kind: m.Kind}
	if len(m.Message) > 0 {
		out.Message = map[string]string(m.Message)
	}
	if !isJSONNull(m.Condition) {
		cond, err := rules.ParseCondition(m.Condition)
		if err != nil {
			return out, fmt.Errorf("rule %s: condition: %w", m.Key, err)
		}
		out.Condition = &cond
	}
	if !isJSONNull(m.AST) {
		ast, err := rules.Parse(m.AST)
		if err != nil {
			return out, fmt.Errorf("rule %s: ast: %w", m.Key, err)
		}
		out.AST = &ast
	}
	return out, nil
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

// ResolveRules implements ci.RuleResolver for the PostgreSQL repository
// (WP-257).
func (r *PGRepository) ResolveRules(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.CatalogRule, error) {
	return ResolveRules(ctx, r, orgID, ciTypeID, ciID)
}

// ResolveFieldsAndRules implements ci.RuleResolver for the PostgreSQL
// repository; templates, when set, is the template list a write leaves
// the CI with (as in ResolveFieldsWithTemplates).
func (r *PGRepository) ResolveFieldsAndRules(ctx context.Context, orgID, ciTypeID, ciID string,
	templates *[]CITemplate) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	var source templateSource
	if templates != nil {
		source = r.templateList(orgID, ciTypeID, *templates)
	}
	return ResolveFieldsAndRules(ctx, r, orgID, ciTypeID, ciID, source)
}

// ResolveRules implements ci.RuleResolver for the in-memory repository:
// without a database there is no rule catalog, so there are no rules.
func (r *MemoryRepository) ResolveRules(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.CatalogRule, error) {
	return ResolveRules(ctx, r, orgID, ciTypeID, ciID)
}

// ErrTemplatesNeedDatabase: the in-memory repository keeps no template
// assignments (WP-710).
var ErrTemplatesNeedDatabase = errors.New("template assignments need the database")

// ResolveFieldsAndRules implements ci.RuleResolver for the in-memory
// repository: the field definitions and no rules; a template list is
// refused (ErrTemplatesNeedDatabase).
func (r *MemoryRepository) ResolveFieldsAndRules(ctx context.Context, orgID, ciTypeID, ciID string,
	templates *[]CITemplate) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	if templates != nil {
		return nil, nil, ErrTemplatesNeedDatabase
	}
	return ResolveFieldsAndRules(ctx, r, orgID, ciTypeID, ciID, nil)
}
