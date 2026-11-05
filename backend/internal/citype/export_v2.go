package citype

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// ExportFormatVersionV2 is the version of the complete metamodel export
// (MET-54, phase 2, WP-262). Version 1 (ExportFormatVersion, BuildExport)
// stays the default (API-01, E-25).
const ExportFormatVersionV2 = 2

// MetamodelExportV2 is the complete metamodel of an organization as one JSON
// document (format 2, WP-262): CI types, attribute definitions, attribute
// sets, templates with their versions, catalog rules, form layouts and
// lifecycle models. Every reference is a key instead of a surrogate id, so
// the document can be imported into another organization (WP-722); ids,
// organization ids, generated fields, version counters, user ids and time
// stamps of the rows are left out. System parts (organization_id NULL)
// appear as references only (key and is_system). FieldGrants and
// FieldSensitivity hold the field permissions (WP-726) with keys instead of
// ids; they appear only for a reader who manages the field permissions,
// otherwise they are left out and OmittedSections names them. Two exports
// of the same state are byte-identical except for exported_at.
type MetamodelExportV2 struct {
	FormatVersion        int                      `json:"format_version"`
	ExportedAt           time.Time                `json:"exported_at"`
	OrganizationID       string                   `json:"organization_id"`
	OmittedSections      []string                 `json:"omitted_sections"`
	CITypes              []ExportTypeV2           `json:"ci_types"`
	AttributeDefinitions []ExportAttributeV2      `json:"attribute_definitions"`
	AttributeSets        []ExportAttributeSetV2   `json:"attribute_sets"`
	Templates            []ExportTemplateV2       `json:"templates"`
	Rules                []ExportRuleV2           `json:"rules"`
	Layouts              []ExportLayoutV2         `json:"layouts"`
	LifecycleModels      []ExportLifecycleModelV2 `json:"lifecycle_models"`
	// FieldGrants and FieldSensitivity are nil when left out for the
	// reader (omitted_sections), an empty list when there is none.
	FieldGrants      *[]ExportFieldGrantV2     `json:"field_grants,omitempty"`
	FieldSensitivity *[]ExportSensitiveFieldV2 `json:"field_sensitivity,omitempty"`
}

// The sections of the field permissions in the export v2 (WP-726), as
// omitted_sections names them.
const (
	SectionFieldGrants      = "field_grants"
	SectionFieldSensitivity = "field_sensitivity"
)

// ExportFieldGrantV2 is a field grant of the export v2 (WP-726): the role
// by name and kind (role, custom_role), the CI type and the relationship
// type by key, the field key and the permission key of an action; empty
// values are left out. Object grants concern single objects and are not
// part of the metamodel.
type ExportFieldGrantV2 struct {
	Role             string `json:"role"`
	RoleKind         string `json:"role_kind"`
	ObjectKind       string `json:"object_kind"`
	CIType           string `json:"ci_type,omitempty"`
	FieldKey         string `json:"field_key,omitempty"`
	RelationshipType string `json:"relationship_type,omitempty"`
	Action           string `json:"action,omitempty"`
	Level            string `json:"level"`
}

// ExportSensitiveFieldV2 is a sensitive field of the export v2: the object
// kind, the CI type by key (empty: every type) and the field key.
type ExportSensitiveFieldV2 struct {
	ObjectKind string `json:"object_kind"`
	CIType     string `json:"ci_type,omitempty"`
	FieldKey   string `json:"field_key"`
}

// FieldPermissionsExportV2 is what a GrantExporter returns.
type FieldPermissionsExportV2 struct {
	FieldGrants []ExportFieldGrantV2
	Sensitivity []ExportSensitiveFieldV2
}

// GrantExporter reads the field grants and sensitive fields of the
// organization with keys instead of ids in the context's transaction, so it
// joins the snapshot of the export v2. The server adapts the field
// permission repository (citype does not import permission).
type GrantExporter interface {
	ExportFieldPermissions(ctx context.Context, orgID string) (*FieldPermissionsExportV2, error)
}

// ExportV2Grants selects the field permission sections of the export v2:
// Exporter reads them (nil: empty sections), Readable is whether the reader
// may see them (field_permission:manage for the whole organization).
// Without Readable an exporter is not called, the sections are left out and
// omitted_sections names them.
type ExportV2Grants struct {
	Exporter GrantExporter
	Readable bool
}

// ExportTypeV2 is a CI type of the export v2; a system type carries no body.
type ExportTypeV2 struct {
	Key      string `json:"key"`
	IsSystem bool   `json:"is_system"`
	*ExportTypeBody
}

// ExportTypeBody holds the content of an organization's CI type: the
// fields of Type without ids, generated fields, counters and time stamps,
// and its references as keys.
type ExportTypeBody struct {
	Name         string        `json:"name"`
	DisplayName  LocalizedText `json:"display_name"`
	Icon         string        `json:"icon,omitempty"`
	Description  string        `json:"description,omitempty"`
	Category     string        `json:"category,omitempty"`
	IsBuiltin    bool          `json:"is_builtin"`
	IsActive     bool          `json:"is_active"`
	IsLogical    bool          `json:"is_logical"`
	IsObservable bool          `json:"is_observable"`
	// ParentTypeKey and LifecycleModelKey name the parent (MET-06) and the
	// own lifecycle model (WP-263); TemplateKey is the type template of
	// the catalog (Templates).
	ParentTypeKey     string `json:"parent_type_key,omitempty"`
	LifecycleModelKey string `json:"lifecycle_model_key,omitempty"`
	TemplateKey       string `json:"template_key,omitempty"`
	// AttributeSetKeys are the assigned sets in position order (WP-250).
	AttributeSetKeys         []string       `json:"attribute_set_keys"`
	Capabilities             map[string]any `json:"capabilities"`
	AllowedRelationshipTypes []string       `json:"allowed_relationship_types"`
	UISchema                 map[string]any `json:"ui_schema"`
	ComplianceRules          []any          `json:"compliance_rules"`
	DiscoveryMappings        []any          `json:"discovery_mappings"`
}

// ExportAttributeV2 is an attribute definition of the export v2: global
// (no owner), of a type (CITypeKey) or of a set (AttributeSetKey). A system
// definition carries no body.
type ExportAttributeV2 struct {
	Key             string `json:"key"`
	Scope           string `json:"scope"`
	CITypeKey       string `json:"ci_type_key,omitempty"`
	AttributeSetKey string `json:"attribute_set_key,omitempty"`
	IsSystem        bool   `json:"is_system"`
	*ExportAttributeBody
}

// ExportAttributeBody is the full AttributeDefinition of an organization
// without ids and time stamps; applies_to_type_ids are type keys.
type ExportAttributeBody struct {
	DisplayName         LocalizedText               `json:"display_name"`
	Description         string                      `json:"description,omitempty"`
	DataType            string                      `json:"data_type"`
	Required            bool                        `json:"required"`
	Default             json.RawMessage             `json:"default,omitempty"`
	EnumValues          []string                    `json:"enum_values,omitempty"`
	MultiValue          bool                        `json:"multi_value"`
	Searchable          bool                        `json:"searchable"`
	Unique              bool                        `json:"unique"`
	UniqueScope         string                      `json:"unique_scope"`
	Inheritable         bool                        `json:"inheritable"`
	Validation          *rules.Rule                 `json:"validation,omitempty"`
	Computed            *rules.Expr                 `json:"computed,omitempty"`
	Conditional         *fieldmeta.ConditionalRules `json:"conditional,omitempty"`
	ReferenceTarget     string                      `json:"reference_target,omitempty"`
	Group               string                      `json:"group,omitempty"`
	Order               int                         `json:"order"`
	Unit                string                      `json:"unit,omitempty"`
	AppliesTo           string                      `json:"applies_to"`
	AppliesToCategories []string                    `json:"applies_to_categories"`
	AppliesToTypeKeys   []string                    `json:"applies_to_type_keys"`
}

// ExportAttributeSetV2 is an attribute set; its attributes are the
// definitions with its attribute_set_key. A system set carries no body.
type ExportAttributeSetV2 struct {
	Key      string `json:"key"`
	IsSystem bool   `json:"is_system"`
	*ExportAttributeSetBody
}

// ExportAttributeSetBody is the content of an organization's set.
type ExportAttributeSetBody struct {
	DisplayName LocalizedText `json:"display_name"`
	Description string        `json:"description,omitempty"`
	IsActive    bool          `json:"is_active"`
}

// ExportTemplateV2 is a template (WP-251) with all its versions. A system
// template carries no body.
type ExportTemplateV2 struct {
	Key      string `json:"key"`
	IsSystem bool   `json:"is_system"`
	*ExportTemplateBody
}

// ExportTemplateBody is the content of an organization's template.
type ExportTemplateBody struct {
	CITypeKey      string                    `json:"ci_type_key"`
	DisplayName    LocalizedText             `json:"display_name"`
	Description    string                    `json:"description,omitempty"`
	IsActive       bool                      `json:"is_active"`
	CurrentVersion *int                      `json:"current_version"`
	Versions       []ExportTemplateVersionV2 `json:"versions"`
}

// ExportTemplateVersionV2 is one version of a template with its items in
// position order; published_by is left out.
type ExportTemplateVersionV2 struct {
	Version     int                    `json:"version"`
	Status      string                 `json:"status"`
	PublishedAt *time.Time             `json:"published_at,omitempty"`
	Items       []ExportTemplateItemV2 `json:"items"`
}

// ExportTemplateItemV2 references an attribute definition by its key and
// owner (ci_type_key, attribute_set_key; none for a global definition).
type ExportTemplateItemV2 struct {
	AttributeKey       string          `json:"attribute_key"`
	CITypeKey          string          `json:"ci_type_key,omitempty"`
	AttributeSetKey    string          `json:"attribute_set_key,omitempty"`
	Position           int             `json:"position"`
	RequiredOverride   *bool           `json:"required_override,omitempty"`
	DefaultOverride    json.RawMessage `json:"default_override,omitempty"`
	ValidationOverride json.RawMessage `json:"validation_override,omitempty"`
}

// ExportRuleV2 is a catalog rule (WP-716). A system rule carries no body.
type ExportRuleV2 struct {
	Key      string `json:"key"`
	IsSystem bool   `json:"is_system"`
	*ExportRuleBody
}

// ExportRuleBody is the content of an organization's rule: its target as
// the key of a type, a set or a template with the version number.
type ExportRuleBody struct {
	AttributeKey    string          `json:"attribute_key"`
	Kind            string          `json:"kind"`
	CITypeKey       string          `json:"ci_type_key,omitempty"`
	AttributeSetKey string          `json:"attribute_set_key,omitempty"`
	TemplateKey     string          `json:"template_key,omitempty"`
	TemplateVersion *int            `json:"template_version,omitempty"`
	Condition       json.RawMessage `json:"condition"`
	AST             json.RawMessage `json:"ast"`
	Message         LocalizedText   `json:"message"`
	IsActive        bool            `json:"is_active"`
}

// ExportLayoutV2 is a form layout (WP-259). The role is its name and the
// table it lives in (RoleKind "role" or "custom_role"), because names are
// unique per table only. There are no system layouts.
type ExportLayoutV2 struct {
	Key         string         `json:"key"`
	CITypeKey   string         `json:"ci_type_key,omitempty"`
	TemplateKey string         `json:"template_key,omitempty"`
	RoleName    string         `json:"role_name,omitempty"`
	RoleKind    string         `json:"role_kind,omitempty"`
	Priority    int            `json:"priority"`
	IsActive    bool           `json:"is_active"`
	Layout      LayoutDocument `json:"layout"`
}

// ExportLifecycleModelV2 is a lifecycle model (WP-263); ID is only the
// lookup of the readers. A system model carries no body.
type ExportLifecycleModelV2 struct {
	ID       string `json:"-"`
	Key      string `json:"key"`
	IsSystem bool   `json:"is_system"`
	*ExportLifecycleModelBody
}

// ExportLifecycleModelBody is the content of a lifecycle model.
type ExportLifecycleModelBody struct {
	Name        string                        `json:"name"`
	AppliesTo   string                        `json:"applies_to"`
	Description string                        `json:"description,omitempty"`
	States      []ExportLifecycleStateV2      `json:"states"`
	Transitions []ExportLifecycleTransitionV2 `json:"transitions"`
}

// ExportLifecycleStateV2 is a state of a lifecycle model.
type ExportLifecycleStateV2 struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	IsInitial      bool     `json:"is_initial"`
	IsTerminal     bool     `json:"is_terminal"`
	SortOrder      int      `json:"sort_order"`
	RequiredFields []string `json:"required_fields"`
}

// ExportLifecycleTransitionV2 is a transition between two states by key
// (no from state: from every state).
type ExportLifecycleTransitionV2 struct {
	Key            string         `json:"key"`
	Label          string         `json:"label"`
	FromStateKey   string         `json:"from_state_key,omitempty"`
	ToStateKey     string         `json:"to_state_key"`
	RequiredFields []string       `json:"required_fields"`
	Validation     map[string]any `json:"validation,omitempty"`
}

// ExportRole is a role an organization's layout can be bound to: a
// standard role (Kind "role") or a custom role ("custom_role").
type ExportRole struct {
	ID   string
	Name string
	Kind string
}

// Kinds of an ExportRole.
const (
	RoleKindRole       = "role"
	RoleKindCustomRole = "custom_role"
)

// LifecycleModelReader reads the lifecycle models the organization sees
// (its own and the system models) with states and transitions; the server
// adapts the lifecycle repository (citype does not import lifecycle).
type LifecycleModelReader interface {
	ExportLifecycleModels(ctx context.Context, orgID string) ([]ExportLifecycleModelV2, error)
}

// RoleReader reads the roles and custom roles of the organization; the
// server adapts the user repository.
type RoleReader interface {
	ExportRoles(ctx context.Context, orgID string) ([]ExportRole, error)
}

// ExportV2Source is what BuildExportV2 reads from: the stores of the
// package and one reading transaction with a snapshot (RunInSnapshot) that
// every read joins. PGRepository implements it.
type ExportV2Source interface {
	Repository
	AttributeDefinitionStore
	AttributeSetStore
	TemplateStore
	RuleStore
	LayoutStore
	RunInSnapshot(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// typeSetLister reads the set assignments of all types in one statement.
type typeSetLister interface {
	typeAttributeSetIDs(ctx context.Context, orgID string) (map[string][]string, error)
}

// exportV2Rows are the rows the export v2 reads in its snapshot.
type exportV2Rows struct {
	types     []Type
	defs      []AttributeDefinition
	sets      []AttributeSet
	typeSets  map[string][]string
	templates []Template
	versions  map[string][]TemplateVersion
	rules     []MetamodelRule
	layouts   []FormLayout
	models    []ExportLifecycleModelV2
	roles     []ExportRole
	// grants are the field permissions, nil when not read.
	grants *FieldPermissionsExportV2
}

// readAllPages reads every page of a list with offset pages of limit rows.
// A page may hold one row more than the limit (newest first lists); it is
// read again with the next page.
func readAllPages[T any](limit int, list func(page api.PaginationParams) ([]T, int, error)) ([]T, error) {
	out := []T{}
	for offset := 0; ; offset += limit {
		rows, total, err := list(api.PaginationParams{Limit: limit, Offset: offset})
		if err != nil {
			return nil, err
		}
		if len(rows) > limit {
			rows = rows[:limit]
		}
		out = append(out, rows...)
		if len(rows) == 0 || offset+len(rows) >= total {
			return out, nil
		}
	}
}

// BuildExportV2 reads the complete metamodel of orgID in one snapshot
// (src.RunInSnapshot) and maps every reference onto keys. The field
// permission sections are empty (no exporter); BuildExportV2WithGrants
// fills them.
func BuildExportV2(ctx context.Context, src ExportV2Source, models LifecycleModelReader, roles RoleReader,
	orgID string, now time.Time) (*MetamodelExportV2, error) {
	return BuildExportV2WithGrants(ctx, src, models, roles, ExportV2Grants{}, orgID, now)
}

// BuildExportV2WithGrants is BuildExportV2 with the field permission
// sections of grants (WP-726): read in the same snapshot when the reader
// may see them, left out and named in omitted_sections otherwise.
func BuildExportV2WithGrants(ctx context.Context, src ExportV2Source, models LifecycleModelReader, roles RoleReader,
	grants ExportV2Grants, orgID string, now time.Time) (*MetamodelExportV2, error) {
	var rows exportV2Rows
	err := src.RunInSnapshot(ctx, orgID, func(ctx context.Context) error {
		if err := readExportV2(ctx, src, models, roles, orgID, &rows); err != nil {
			return err
		}
		if grants.Exporter == nil || !grants.Readable {
			return nil
		}
		g, err := grants.Exporter.ExportFieldPermissions(ctx, orgID)
		if err != nil {
			return fmt.Errorf("list field permissions: %w", err)
		}
		if g == nil {
			g = &FieldPermissionsExportV2{}
		}
		rows.grants = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	out, err := assembleExportV2(&rows, orgID, now)
	if err != nil {
		return nil, err
	}
	switch {
	case grants.Exporter != nil && !grants.Readable:
		out.OmittedSections = append(out.OmittedSections, SectionFieldGrants, SectionFieldSensitivity)
	case rows.grants != nil:
		fieldGrants, sensitivity := exportFieldPermissions(rows.grants)
		out.FieldGrants, out.FieldSensitivity = &fieldGrants, &sensitivity
	default:
		out.FieldGrants, out.FieldSensitivity = &[]ExportFieldGrantV2{}, &[]ExportSensitiveFieldV2{}
	}
	return out, nil
}

// exportFieldPermissions returns the field permissions in a stable order:
// grants by role kind, role, object kind, CI type and target, sensitive
// fields by object kind, CI type and field key.
func exportFieldPermissions(g *FieldPermissionsExportV2) ([]ExportFieldGrantV2, []ExportSensitiveFieldV2) {
	fieldGrants := append([]ExportFieldGrantV2{}, g.FieldGrants...)
	slices.SortStableFunc(fieldGrants, func(a, b ExportFieldGrantV2) int {
		return cmp.Or(cmp.Compare(a.RoleKind, b.RoleKind), cmp.Compare(a.Role, b.Role), cmp.Compare(a.ObjectKind, b.ObjectKind),
			cmp.Compare(a.CIType, b.CIType), cmp.Compare(a.FieldKey, b.FieldKey),
			cmp.Compare(a.RelationshipType, b.RelationshipType), cmp.Compare(a.Action, b.Action), cmp.Compare(a.Level, b.Level))
	})
	sensitivity := append([]ExportSensitiveFieldV2{}, g.Sensitivity...)
	slices.SortStableFunc(sensitivity, func(a, b ExportSensitiveFieldV2) int {
		return cmp.Or(cmp.Compare(a.ObjectKind, b.ObjectKind), cmp.Compare(a.CIType, b.CIType), cmp.Compare(a.FieldKey, b.FieldKey))
	})
	return fieldGrants, sensitivity
}

// readExportV2 reads the rows of the export in the context's transaction.
func readExportV2(ctx context.Context, src ExportV2Source, models LifecycleModelReader, roles RoleReader,
	orgID string, rows *exportV2Rows) error {
	var err error
	if rows.types, err = readAllPages(exportPage, func(page api.PaginationParams) ([]Type, int, error) {
		return src.List(ctx, orgID, FilterParams{IncludeInactive: true}, page)
	}); err != nil {
		return fmt.Errorf("list ci types: %w", err)
	}
	if rows.defs, err = src.ListAttributeDefinitions(ctx, orgID, AttributeFilter{}); err != nil {
		return fmt.Errorf("list attribute definitions: %w", err)
	}
	if rows.sets, err = readAllPages(api.MaxPageLimit, func(page api.PaginationParams) ([]AttributeSet, int, error) {
		return src.ListAttributeSetPage(ctx, orgID, page)
	}); err != nil {
		return fmt.Errorf("list attribute sets: %w", err)
	}
	if rows.typeSets, err = readTypeSets(ctx, src, orgID, rows.types); err != nil {
		return err
	}
	if rows.templates, err = readAllPages(api.MaxPageLimit, func(page api.PaginationParams) ([]Template, int, error) {
		return src.ListTemplatePage(ctx, orgID, TemplateFilter{}, page)
	}); err != nil {
		return fmt.Errorf("list templates: %w", err)
	}
	rows.versions = map[string][]TemplateVersion{}
	for i := range rows.templates {
		id := rows.templates[i].ID
		if rows.versions[id], err = readAllPages(api.MaxPageLimit, func(page api.PaginationParams) ([]TemplateVersion, int, error) {
			return src.ListTemplateVersionPage(ctx, orgID, id, page)
		}); err != nil {
			return fmt.Errorf("list versions of template %s: %w", rows.templates[i].Key, err)
		}
	}
	if rows.rules, err = readAllPages(api.MaxPageLimit, func(page api.PaginationParams) ([]MetamodelRule, int, error) {
		return src.ListRulePage(ctx, orgID, &RuleFilter{}, page)
	}); err != nil {
		return fmt.Errorf("list rules: %w", err)
	}
	if rows.layouts, err = readAllPages(api.MaxPageLimit, func(page api.PaginationParams) ([]FormLayout, int, error) {
		return src.ListLayoutPage(ctx, orgID, &LayoutFilter{}, page)
	}); err != nil {
		return fmt.Errorf("list layouts: %w", err)
	}
	if rows.models, err = models.ExportLifecycleModels(ctx, orgID); err != nil {
		return fmt.Errorf("list lifecycle models: %w", err)
	}
	if rows.roles, err = roles.ExportRoles(ctx, orgID); err != nil {
		return fmt.Errorf("list roles: %w", err)
	}
	return nil
}

// readTypeSets reads the set assignments of the organization's types, in
// one statement where the source offers it.
func readTypeSets(ctx context.Context, src ExportV2Source, orgID string, types []Type) (map[string][]string, error) {
	if l, ok := src.(typeSetLister); ok {
		out, err := l.typeAttributeSetIDs(ctx, orgID)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	out := map[string][]string{}
	for i := range types {
		if types[i].IsSystem {
			continue
		}
		doc, err := src.GetTypeAttributeSets(ctx, orgID, types[i].ID)
		if err != nil {
			return nil, fmt.Errorf("read attribute sets of ci type %s: %w", types[i].Key, err)
		}
		for _, a := range doc.AttributeSets {
			out[types[i].ID] = append(out[types[i].ID], a.AttributeSetID)
		}
	}
	return out, nil
}

// exportKeys maps the surrogate ids of the export onto keys.
type exportKeys struct {
	types     map[string]string
	sets      map[string]string
	templates map[string]string
	versions  map[string]templateVersionRef
	defs      map[string]*AttributeDefinition
	models    map[string]string
	roles     map[string]ExportRole
}

// templateVersionRef names a template version by template key and number.
type templateVersionRef struct {
	template string
	version  int
}

// exportKey returns the key of id in m; an id without one is an error, since
// every reference of the snapshot is visible to the organization.
func exportKey(m map[string]string, what, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	k, ok := m[id]
	if !ok {
		return "", fmt.Errorf("export: unknown %s %s", what, id)
	}
	return k, nil
}

func newExportKeys(rows *exportV2Rows) *exportKeys {
	k := &exportKeys{types: map[string]string{}, sets: map[string]string{}, templates: map[string]string{},
		versions: map[string]templateVersionRef{}, defs: map[string]*AttributeDefinition{}, models: map[string]string{},
		roles: map[string]ExportRole{}}
	for i := range rows.types {
		k.types[rows.types[i].ID] = rows.types[i].Key
	}
	for i := range rows.sets {
		k.sets[rows.sets[i].ID] = rows.sets[i].Key
	}
	for i := range rows.templates {
		k.templates[rows.templates[i].ID] = rows.templates[i].Key
		versions := rows.versions[rows.templates[i].ID]
		for j := range versions {
			k.versions[versions[j].ID] = templateVersionRef{template: rows.templates[i].Key, version: versions[j].Version}
		}
	}
	for i := range rows.defs {
		k.defs[rows.defs[i].ID] = &rows.defs[i]
	}
	for i := range rows.models {
		k.models[rows.models[i].ID] = rows.models[i].Key
	}
	for _, r := range rows.roles {
		k.roles[r.ID] = r
	}
	return k
}

// assembleExportV2 builds the document from the rows of the snapshot.
func assembleExportV2(rows *exportV2Rows, orgID string, now time.Time) (*MetamodelExportV2, error) {
	k := newExportKeys(rows)
	out := &MetamodelExportV2{FormatVersion: ExportFormatVersionV2, ExportedAt: now.UTC(), OrganizationID: orgID,
		OmittedSections: []string{}}
	var err error
	if out.CITypes, err = exportTypes(rows, k); err != nil {
		return nil, err
	}
	if out.AttributeDefinitions, err = exportAttributes(rows.defs, k); err != nil {
		return nil, err
	}
	out.AttributeSets = exportSets(rows.sets)
	if out.Templates, err = exportTemplates(rows, k); err != nil {
		return nil, err
	}
	if out.Rules, err = exportRules(rows.rules, k); err != nil {
		return nil, err
	}
	if out.Layouts, err = exportLayouts(rows.layouts, k); err != nil {
		return nil, err
	}
	out.LifecycleModels = exportLifecycleModels(rows.models)
	return out, nil
}

func exportTypes(rows *exportV2Rows, k *exportKeys) ([]ExportTypeV2, error) {
	out := make([]ExportTypeV2, 0, len(rows.types))
	for i := range rows.types {
		t := &rows.types[i]
		item := ExportTypeV2{Key: t.Key, IsSystem: t.IsSystem}
		if !t.IsSystem {
			body := &ExportTypeBody{Name: t.Name, DisplayName: t.DisplayName, Icon: t.Icon, Description: t.Description,
				Category: t.Category, IsBuiltin: t.IsBuiltin, IsActive: t.IsActive, IsLogical: t.IsLogical,
				IsObservable: t.IsObservable, TemplateKey: t.TemplateKey, AttributeSetKeys: []string{},
				Capabilities: t.Capabilities, AllowedRelationshipTypes: api.NonNilSlice(t.AllowedRelationshipTypes),
				UISchema: t.UISchema, ComplianceRules: nonNilAny(t.ComplianceRules), DiscoveryMappings: nonNilAny(t.DiscoveryMappings)}
			var err error
			if body.ParentTypeKey, err = exportKey(k.types, "parent ci type", t.ParentTypeID); err != nil {
				return nil, err
			}
			if body.LifecycleModelKey, err = exportKey(k.models, "lifecycle model", t.LifecycleModelID); err != nil {
				return nil, err
			}
			for _, setID := range rows.typeSets[t.ID] {
				setKey, setErr := exportKey(k.sets, "attribute set", setID)
				if setErr != nil {
					return nil, setErr
				}
				body.AttributeSetKeys = append(body.AttributeSetKeys, setKey)
			}
			item.ExportTypeBody = body
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b ExportTypeV2) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), compareBool(b.IsSystem, a.IsSystem))
	})
	return out, nil
}

func exportAttributes(defs []AttributeDefinition, k *exportKeys) ([]ExportAttributeV2, error) {
	out := make([]ExportAttributeV2, 0, len(defs))
	for i := range defs {
		d := &defs[i]
		item := ExportAttributeV2{Key: d.Key, Scope: d.Scope, IsSystem: d.IsSystem}
		var err error
		if item.CITypeKey, err = exportKey(k.types, "ci type", d.CITypeID); err != nil {
			return nil, err
		}
		if item.AttributeSetKey, err = exportKey(k.sets, "attribute set", d.AttributeSetID); err != nil {
			return nil, err
		}
		if !d.IsSystem {
			body := &ExportAttributeBody{DisplayName: d.DisplayName, Description: d.Description, DataType: d.DataType,
				Required: d.Required, Default: d.Default, EnumValues: d.EnumValues, MultiValue: d.MultiValue,
				Searchable: d.Searchable, Unique: d.Unique, UniqueScope: d.UniqueScope, Inheritable: d.Inheritable,
				Validation: d.Validation, Computed: d.Computed, Conditional: d.Conditional,
				ReferenceTarget: d.ReferenceTarget, Group: d.Group, Order: d.Order, Unit: d.Unit, AppliesTo: d.AppliesTo,
				AppliesToCategories: api.NonNilSlice(d.AppliesToCategories), AppliesToTypeKeys: []string{}}
			for _, typeID := range d.AppliesToTypeIDs {
				typeKey, typeErr := exportKey(k.types, "ci type", typeID)
				if typeErr != nil {
					return nil, typeErr
				}
				body.AppliesToTypeKeys = append(body.AppliesToTypeKeys, typeKey)
			}
			item.ExportAttributeBody = body
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b ExportAttributeV2) int {
		return cmp.Or(cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.CITypeKey, b.CITypeKey),
			cmp.Compare(a.AttributeSetKey, b.AttributeSetKey), cmp.Compare(a.Key, b.Key), compareBool(b.IsSystem, a.IsSystem))
	})
	return out, nil
}

func exportSets(sets []AttributeSet) []ExportAttributeSetV2 {
	out := make([]ExportAttributeSetV2, 0, len(sets))
	for i := range sets {
		s := &sets[i]
		item := ExportAttributeSetV2{Key: s.Key, IsSystem: s.IsSystem}
		if !s.IsSystem {
			item.ExportAttributeSetBody = &ExportAttributeSetBody{DisplayName: s.DisplayName, Description: s.Description,
				IsActive: s.IsActive}
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b ExportAttributeSetV2) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), compareBool(b.IsSystem, a.IsSystem))
	})
	return out
}

func exportTemplates(rows *exportV2Rows, k *exportKeys) ([]ExportTemplateV2, error) {
	out := make([]ExportTemplateV2, 0, len(rows.templates))
	for i := range rows.templates {
		t := &rows.templates[i]
		item := ExportTemplateV2{Key: t.Key, IsSystem: t.IsSystem}
		if !t.IsSystem {
			typeKey, err := exportKey(k.types, "ci type", t.CITypeID)
			if err != nil {
				return nil, err
			}
			body := &ExportTemplateBody{CITypeKey: typeKey, DisplayName: t.DisplayName, Description: t.Description,
				IsActive: t.IsActive, CurrentVersion: t.CurrentVersion, Versions: []ExportTemplateVersionV2{}}
			versions := rows.versions[t.ID]
			for j := range versions {
				version, versionErr := exportTemplateVersion(&versions[j], k)
				if versionErr != nil {
					return nil, versionErr
				}
				body.Versions = append(body.Versions, version)
			}
			slices.SortStableFunc(body.Versions, func(a, b ExportTemplateVersionV2) int { return cmp.Compare(a.Version, b.Version) })
			item.ExportTemplateBody = body
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b ExportTemplateV2) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), compareBool(b.IsSystem, a.IsSystem))
	})
	return out, nil
}

func exportTemplateVersion(v *TemplateVersion, k *exportKeys) (ExportTemplateVersionV2, error) {
	out := ExportTemplateVersionV2{Version: v.Version, Status: v.Status, Items: []ExportTemplateItemV2{}}
	if v.PublishedAt != nil {
		at := v.PublishedAt.UTC()
		out.PublishedAt = &at
	}
	for _, it := range v.Items {
		def, ok := k.defs[it.AttributeDefinitionID]
		if !ok {
			return out, fmt.Errorf("export: unknown attribute definition %s", it.AttributeDefinitionID)
		}
		item := ExportTemplateItemV2{AttributeKey: def.Key, Position: it.Position, RequiredOverride: it.RequiredOverride,
			DefaultOverride: it.DefaultOverride, ValidationOverride: it.ValidationOverride}
		var err error
		if item.CITypeKey, err = exportKey(k.types, "ci type", def.CITypeID); err != nil {
			return out, err
		}
		if item.AttributeSetKey, err = exportKey(k.sets, "attribute set", def.AttributeSetID); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	slices.SortStableFunc(out.Items, func(a, b ExportTemplateItemV2) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.AttributeKey, b.AttributeKey))
	})
	return out, nil
}

// ruleBinding is the sort key of a rule's target: types, then sets, then
// template versions, each by key (and version number).
type ruleBinding struct {
	rank    int
	target  string
	version int
}

func exportRules(list []MetamodelRule, k *exportKeys) ([]ExportRuleV2, error) {
	type sorted struct {
		item    ExportRuleV2
		binding ruleBinding
	}
	all := make([]sorted, 0, len(list))
	for i := range list {
		r := &list[i]
		var b ruleBinding
		var err error
		switch {
		case r.CITypeID != "":
			b.target, err = exportKey(k.types, "ci type", r.CITypeID)
		case r.AttributeSetID != "":
			b.rank = 1
			b.target, err = exportKey(k.sets, "attribute set", r.AttributeSetID)
		case r.TemplateVersionID != "":
			ref, ok := k.versions[r.TemplateVersionID]
			if !ok {
				err = fmt.Errorf("export: unknown template version %s", r.TemplateVersionID)
			}
			b = ruleBinding{rank: 2, target: ref.template, version: ref.version}
		}
		if err != nil {
			return nil, err
		}
		item := ExportRuleV2{Key: r.Key, IsSystem: r.IsSystem}
		if !r.IsSystem {
			body := &ExportRuleBody{AttributeKey: r.AttributeKey, Kind: r.Kind, Condition: jsonOrNull(r.Condition),
				AST: jsonOrNull(r.AST), Message: r.Message, IsActive: r.IsActive}
			switch b.rank {
			case 0:
				body.CITypeKey = b.target
			case 1:
				body.AttributeSetKey = b.target
			default:
				version := b.version
				body.TemplateKey, body.TemplateVersion = b.target, &version
			}
			item.ExportRuleBody = body
		}
		all = append(all, sorted{item: item, binding: b})
	}
	slices.SortStableFunc(all, func(a, b sorted) int {
		return cmp.Or(cmp.Compare(a.binding.rank, b.binding.rank), cmp.Compare(a.binding.target, b.binding.target),
			cmp.Compare(a.binding.version, b.binding.version), cmp.Compare(a.item.Key, b.item.Key),
			compareBool(b.item.IsSystem, a.item.IsSystem))
	})
	out := make([]ExportRuleV2, 0, len(all))
	for _, s := range all {
		out = append(out, s.item)
	}
	return out, nil
}

func exportLayouts(list []FormLayout, k *exportKeys) ([]ExportLayoutV2, error) {
	out := make([]ExportLayoutV2, 0, len(list))
	for i := range list {
		l := &list[i]
		item := ExportLayoutV2{Key: l.Key, Priority: l.Priority, IsActive: l.IsActive, Layout: l.Layout}
		var err error
		if item.CITypeKey, err = exportKey(k.types, "ci type", l.CITypeID); err != nil {
			return nil, err
		}
		if item.TemplateKey, err = exportKey(k.templates, "template", l.TemplateID); err != nil {
			return nil, err
		}
		if l.RoleID != "" {
			role, ok := k.roles[l.RoleID]
			if !ok {
				return nil, fmt.Errorf("export: unknown role %s", l.RoleID)
			}
			item.RoleName, item.RoleKind = role.Name, role.Kind
		}
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(a, b ExportLayoutV2) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

func exportLifecycleModels(list []ExportLifecycleModelV2) []ExportLifecycleModelV2 {
	out := make([]ExportLifecycleModelV2, 0, len(list))
	for i := range list {
		m := list[i]
		if m.IsSystem || m.ExportLifecycleModelBody == nil {
			m.ExportLifecycleModelBody = nil
			out = append(out, m)
			continue
		}
		body := *m.ExportLifecycleModelBody
		body.States = slices.Clone(body.States)
		if body.States == nil {
			body.States = []ExportLifecycleStateV2{}
		}
		for j := range body.States {
			body.States[j].RequiredFields = api.NonNilSlice(body.States[j].RequiredFields)
		}
		slices.SortStableFunc(body.States, func(a, b ExportLifecycleStateV2) int {
			return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), cmp.Compare(a.Key, b.Key))
		})
		body.Transitions = slices.Clone(body.Transitions)
		if body.Transitions == nil {
			body.Transitions = []ExportLifecycleTransitionV2{}
		}
		for j := range body.Transitions {
			body.Transitions[j].RequiredFields = api.NonNilSlice(body.Transitions[j].RequiredFields)
		}
		slices.SortStableFunc(body.Transitions, func(a, b ExportLifecycleTransitionV2) int {
			return cmp.Or(cmp.Compare(a.FromStateKey, b.FromStateKey), cmp.Compare(a.ToStateKey, b.ToStateKey),
				cmp.Compare(a.Key, b.Key))
		})
		m.ExportLifecycleModelBody = &body
		out = append(out, m)
	}
	slices.SortStableFunc(out, func(a, b ExportLifecycleModelV2) int {
		return cmp.Or(cmp.Compare(a.Key, b.Key), compareBool(b.IsSystem, a.IsSystem))
	})
	return out
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

func nonNilAny(s []any) []any {
	if s == nil {
		return []any{}
	}
	return s
}

// jsonOrNull is raw, or the JSON null for an empty value.
func jsonOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}
