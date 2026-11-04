package metamodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
)

// Import of a metamodel export v2 as a draft (WP-722, MET-54, E-112,
// E-115): POST /api/v1/metamodel/import reads the state of the target
// organization with the export v2 builder in one snapshot, resolves every
// key of the file against it, turns the differences into typed draft items
// in the order of their dependencies and captures them into a new draft in
// one transaction. A part of the file that the draft creates is referenced
// by the item that creates it ({"$item": "<item id>"} in the payload, or
// the item as target of a template version or of a type's set assignment
// and activation). System parts and lifecycle models are only resolved,
// roles by their name; parts of the target missing in the file stay as they
// are. Of a template only the current published version is taken over; the
// older versions and their rules are reported as history-skipped. The
// draft becomes productive only with its publication (WP-261).

// Conflict codes of the import report; a conflict refuses the import (422).
const (
	ConflictDatatype           = "datatype-conflict"
	ConflictMissingReference   = "missing-reference"
	ConflictSystemKeyCollision = "system-key-collision"
)

// Note codes of the import report.
const (
	// NoteHistorySkipped: older template versions and their rules are not
	// taken over.
	NoteHistorySkipped = "history-skipped"
	// NoteNotImported: a part of the file the import does not take over
	// (an unreferenced lifecycle model the target lacks, an immutable
	// field of an existing part).
	NoteNotImported = "not-imported"
	// NoteFieldPermissionsSkipped: the field grants and sensitive fields of
	// the file (field_grants, field_sensitivity, WP-726) are accepted but
	// not taken over; phase 2 imports no field permissions (E-115).
	NoteFieldPermissionsSkipped = "field-permissions-skipped"
)

// Actions of the parts in the import report.
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	ActionResolved  = "resolved"
)

// Kinds of the import report beside the item kinds.
const (
	KindLifecycleModel   = "lifecycle_model"
	kindParentType       = "parent_type"
	kindRole             = "role"
	kindRelationshipType = "relationship_type"
)

// ImportReport is the report of an import: every part of the file with its
// action, the conflicts that refused it and the notes.
type ImportReport struct {
	Unchanged bool               `json:"unchanged"`
	Items     []ImportReportItem `json:"items"`
	Conflicts []ImportConflict   `json:"conflicts"`
	Notes     []ImportNote       `json:"notes"`
}

// ImportReportItem is one part of the file: kind, key, the owner of an
// attribute definition or of a rule of a template version
// ("ci_type:<key>", "attribute_set:<key>", "template:<key>") and the action
// (create, update, unchanged, resolved).
type ImportReportItem struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Owner  string `json:"owner,omitempty"`
	Action string `json:"action"`
}

// ImportConflict is a conflict of a part of the file: the code, the part and
// for missing-reference the kind and key of the reference.
type ImportConflict struct {
	Code         string `json:"code"`
	Kind         string `json:"kind"`
	Key          string `json:"key"`
	Owner        string `json:"owner,omitempty"`
	Reference    string `json:"reference,omitempty"`
	ReferenceKey string `json:"reference_key,omitempty"`
	Detail       string `json:"detail"`
}

// ImportNote is a note of the report.
type ImportNote struct {
	Code   string `json:"code"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Detail string `json:"detail"`
}

// ImportItem is one item the import captures into its draft. Target is the
// id of an existing target or the planRef of the item that creates it;
// Template is the template (id or planRef) of a template_version update or
// publish whose version the draft creates. planRef values in Payload become
// item references {"$item": "<item id>"}.
type ImportItem struct {
	Kind     string
	Op       string
	Target   any
	Template any
	Payload  map[string]any
}

// planRef references the item of the plan with this index.
type planRef int

// ─── Target state ──────────────────────────────────────────────────────────

// TargetRef is a part of the target: its id and whether it is a system part.
type TargetRef struct {
	ID     string
	System bool
}

// DefKey names an attribute definition by its owner keys and key.
type DefKey struct {
	Type, Set, Key string
}

// VersionKey names a template version by template key and number.
type VersionKey struct {
	Template string
	Version  int
}

// VersionRuleKey names a rule of a template version.
type VersionRuleKey struct {
	VersionKey
	Key string
}

// ImportTarget is the state of the target organization: the export v2 of
// its metamodel (read in one snapshot) and the ids behind its keys.
type ImportTarget struct {
	Doc       *citype.MetamodelExportV2
	Types     map[string]TargetRef
	Sets      map[string]TargetRef
	Templates map[string]TargetRef
	Layouts   map[string]string
	Models    map[string]TargetRef
	Defs      map[DefKey]TargetRef
	// Rules are the rules without template version by key.
	Rules map[string]TargetRef
	// VersionRules are the rules of the template versions.
	VersionRules map[VersionRuleKey]string
	Versions     map[VersionKey]string
	// Roles are the ids of the roles and custom roles by name.
	Roles map[string][]string
	// RelationshipTypes are the keys of the visible relationship types.
	RelationshipTypes map[string]bool
}

// ImportTargetReader reads the state of the target organization.
type ImportTargetReader interface {
	ReadImportTarget(ctx context.Context, orgID string) (*ImportTarget, error)
}

// RelationshipTypeLister reads the relationship types an organization sees
// (relationshiptype.Repository).
type RelationshipTypeLister interface {
	ListAll(ctx context.Context, orgID string) ([]relationshiptype.Type, error)
}

// exportTarget reads the target with the export v2 builder (WP-262).
type exportTarget struct {
	catalog  citype.ExportV2Source
	models   citype.LifecycleModelReader
	roles    citype.RoleReader
	relTypes RelationshipTypeLister
}

// NewImportTarget returns the reader of the target state over the export
// v2 sources; nil when one is missing (no database).
func NewImportTarget(catalog citype.ExportV2Source, models citype.LifecycleModelReader, roles citype.RoleReader,
	relTypes RelationshipTypeLister) ImportTargetReader {
	if catalog == nil || models == nil || roles == nil || relTypes == nil {
		return nil
	}
	return &exportTarget{catalog: catalog, models: models, roles: roles, relTypes: relTypes}
}

// targetRows are the rows the builder read, recorded with their ids.
type targetRows struct {
	types     map[string]citype.Type
	defs      map[string]citype.AttributeDefinition
	sets      map[string]citype.AttributeSet
	templates map[string]citype.Template
	versions  map[string]citype.TemplateVersion
	rules     map[string]citype.MetamodelRule
	layouts   map[string]citype.FormLayout
	models    map[string]citype.ExportLifecycleModelV2
	roles     []citype.ExportRole
	relTypes  []relationshiptype.Type
}

// recordingSource is the source of the builder that records the rows it
// reads; its snapshot reads the relationship types as well.
type recordingSource struct {
	citype.ExportV2Source
	rows  *targetRows
	extra func(ctx context.Context) error
}

func (s *recordingSource) RunInSnapshot(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return s.ExportV2Source.RunInSnapshot(ctx, orgID, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return s.extra(ctx)
	})
}

func (s *recordingSource) List(ctx context.Context, orgID string, filter citype.FilterParams, page api.PaginationParams) ([]citype.Type, int, error) {
	out, total, err := s.ExportV2Source.List(ctx, orgID, filter, page)
	for i := range out {
		s.rows.types[out[i].ID] = out[i]
	}
	return out, total, err
}

func (s *recordingSource) ListAttributeDefinitions(ctx context.Context, orgID string, filter citype.AttributeFilter) ([]citype.AttributeDefinition, error) {
	out, err := s.ExportV2Source.ListAttributeDefinitions(ctx, orgID, filter)
	for i := range out {
		s.rows.defs[out[i].ID] = out[i]
	}
	return out, err
}

func (s *recordingSource) ListAttributeSetPage(ctx context.Context, orgID string, page api.PaginationParams) ([]citype.AttributeSet, int, error) {
	out, total, err := s.ExportV2Source.ListAttributeSetPage(ctx, orgID, page)
	for i := range out {
		s.rows.sets[out[i].ID] = out[i]
	}
	return out, total, err
}

func (s *recordingSource) ListTemplatePage(ctx context.Context, orgID string, filter citype.TemplateFilter, page api.PaginationParams) ([]citype.Template, int, error) {
	out, total, err := s.ExportV2Source.ListTemplatePage(ctx, orgID, filter, page)
	for i := range out {
		s.rows.templates[out[i].ID] = out[i]
	}
	return out, total, err
}

func (s *recordingSource) ListTemplateVersionPage(ctx context.Context, orgID, id string, page api.PaginationParams) ([]citype.TemplateVersion, int, error) {
	out, total, err := s.ExportV2Source.ListTemplateVersionPage(ctx, orgID, id, page)
	for i := range out {
		s.rows.versions[out[i].ID] = out[i]
	}
	return out, total, err
}

func (s *recordingSource) ListRulePage(ctx context.Context, orgID string, filter *citype.RuleFilter, page api.PaginationParams) ([]citype.MetamodelRule, int, error) {
	out, total, err := s.ExportV2Source.ListRulePage(ctx, orgID, filter, page)
	for i := range out {
		s.rows.rules[out[i].ID] = out[i]
	}
	return out, total, err
}

func (s *recordingSource) ListLayoutPage(ctx context.Context, orgID string, filter *citype.LayoutFilter, page api.PaginationParams) ([]citype.FormLayout, int, error) {
	out, total, err := s.ExportV2Source.ListLayoutPage(ctx, orgID, filter, page)
	for i := range out {
		s.rows.layouts[out[i].ID] = out[i]
	}
	return out, total, err
}

// recordingModels and recordingRoles record the lifecycle models and roles.
type recordingModels struct {
	citype.LifecycleModelReader
	rows *targetRows
}

func (m recordingModels) ExportLifecycleModels(ctx context.Context, orgID string) ([]citype.ExportLifecycleModelV2, error) {
	out, err := m.LifecycleModelReader.ExportLifecycleModels(ctx, orgID)
	for i := range out {
		m.rows.models[out[i].ID] = out[i]
	}
	return out, err
}

type recordingRoles struct {
	citype.RoleReader
	rows *targetRows
}

func (r recordingRoles) ExportRoles(ctx context.Context, orgID string) ([]citype.ExportRole, error) {
	out, err := r.RoleReader.ExportRoles(ctx, orgID)
	r.rows.roles = append(r.rows.roles, out...)
	return out, err
}

// ReadImportTarget implements ImportTargetReader: the export v2 of the
// organization and, in the same snapshot, the ids of its parts and the
// relationship types.
func (t *exportTarget) ReadImportTarget(ctx context.Context, orgID string) (*ImportTarget, error) {
	rows := &targetRows{types: map[string]citype.Type{}, defs: map[string]citype.AttributeDefinition{},
		sets: map[string]citype.AttributeSet{}, templates: map[string]citype.Template{},
		versions: map[string]citype.TemplateVersion{}, rules: map[string]citype.MetamodelRule{},
		layouts: map[string]citype.FormLayout{}, models: map[string]citype.ExportLifecycleModelV2{}}
	src := &recordingSource{ExportV2Source: t.catalog, rows: rows, extra: func(ctx context.Context) error {
		list, err := t.relTypes.ListAll(ctx, orgID)
		if err != nil {
			return fmt.Errorf("list relationship types: %w", err)
		}
		rows.relTypes = list
		return nil
	}}
	doc, err := citype.BuildExportV2(ctx, src, recordingModels{LifecycleModelReader: t.models, rows: rows},
		recordingRoles{RoleReader: t.roles, rows: rows}, orgID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return rows.target(doc), nil
}

// target maps the recorded rows onto the keys of the export.
func (rows *targetRows) target(doc *citype.MetamodelExportV2) *ImportTarget {
	out := newImportTarget(doc)
	typeKeys, setKeys, templateKeys := map[string]string{}, map[string]string{}, map[string]string{}
	for id := range rows.types {
		t := rows.types[id]
		typeKeys[id] = t.Key
		putRef(out.Types, t.Key, TargetRef{ID: id, System: t.IsSystem})
	}
	for id := range rows.sets {
		s := rows.sets[id]
		setKeys[id] = s.Key
		putRef(out.Sets, s.Key, TargetRef{ID: id, System: s.IsSystem})
	}
	for id := range rows.templates {
		t := rows.templates[id]
		templateKeys[id] = t.Key
		putRef(out.Templates, t.Key, TargetRef{ID: id, System: t.IsSystem})
	}
	versionKeys := map[string]VersionKey{}
	for id := range rows.versions {
		v := rows.versions[id]
		key := VersionKey{Template: templateKeys[v.TemplateID], Version: v.Version}
		versionKeys[id] = key
		out.Versions[key] = id
	}
	for id := range rows.defs {
		d := rows.defs[id]
		k := DefKey{Type: typeKeys[d.CITypeID], Set: setKeys[d.AttributeSetID], Key: d.Key}
		if existing, ok := out.Defs[k]; !ok || existing.System {
			out.Defs[k] = TargetRef{ID: id, System: d.IsSystem}
		}
	}
	for id := range rows.rules {
		r := rows.rules[id]
		if r.TemplateVersionID != "" {
			out.VersionRules[VersionRuleKey{VersionKey: versionKeys[r.TemplateVersionID], Key: r.Key}] = id
			continue
		}
		putRef(out.Rules, r.Key, TargetRef{ID: id, System: r.IsSystem})
	}
	for id := range rows.layouts {
		out.Layouts[rows.layouts[id].Key] = id
	}
	for id := range rows.models {
		m := rows.models[id]
		putRef(out.Models, m.Key, TargetRef{ID: id, System: m.IsSystem})
	}
	for _, r := range rows.roles {
		out.Roles[r.Name] = append(out.Roles[r.Name], r.ID)
	}
	for i := range rows.relTypes {
		out.RelationshipTypes[rows.relTypes[i].Key] = true
	}
	return out
}

// newImportTarget returns an empty target with doc.
func newImportTarget(doc *citype.MetamodelExportV2) *ImportTarget {
	return &ImportTarget{Doc: doc, Types: map[string]TargetRef{}, Sets: map[string]TargetRef{},
		Templates: map[string]TargetRef{}, Layouts: map[string]string{}, Models: map[string]TargetRef{},
		Defs: map[DefKey]TargetRef{}, Rules: map[string]TargetRef{}, VersionRules: map[VersionRuleKey]string{},
		Versions: map[VersionKey]string{}, Roles: map[string][]string{}, RelationshipTypes: map[string]bool{}}
}

// putRef keeps the part of the organization before a system part of the
// same key.
func putRef(m map[string]TargetRef, key string, ref TargetRef) {
	if existing, ok := m[key]; ok && !existing.System {
		return
	}
	m[key] = ref
}

// ─── Plan ──────────────────────────────────────────────────────────────────

// importPlan is the outcome of planImport: the items in the order of their
// dependencies and the report.
type importPlan struct {
	items  []ImportItem
	report ImportReport
	// activation are the activate and deactivate items of the types, put
	// after every other item.
	activation []ImportItem
}

// planner compares the file with the target.
type planner struct {
	file   *citype.MetamodelExportV2
	target *ImportTarget
	plan   *importPlan
	// docs of the target by key.
	types     map[string]*citype.ExportTypeV2
	sets      map[string]*citype.ExportAttributeSetV2
	defs      map[DefKey]*citype.ExportAttributeV2
	templates map[string]*citype.ExportTemplateV2
	rules     map[string]*citype.ExportRuleV2
	vrules    map[VersionRuleKey]*citype.ExportRuleV2
	layouts   map[string]*citype.ExportLayoutV2
	// created are the parts the plan creates: kind and key to the item.
	created map[string]planRef
	// fileTypes are the organization's types of the file by key.
	fileTypes map[string]*citype.ExportTypeV2
	// newVersions are the template versions the plan creates by template
	// key; currentVersions the current version of the target the template
	// keeps (id) when the plan creates none.
	newVersions     map[string]planRef
	currentVersions map[string]string
	// copiedRules is the version of an existing template whose rules the
	// new version gets as copies (metamodel_rule_copy_template_version,
	// migration 000207: the highest version below the new one).
	copiedRules map[string]VersionKey
	// publish are the publish items of the new template versions, put
	// after the rules.
	publish []ImportItem
	// referencedModels are the lifecycle model keys the types use.
	referencedModels map[string]bool
}

// planImport compares the file with the target and returns the items and
// the report.
func planImport(file *citype.MetamodelExportV2, target *ImportTarget) *importPlan {
	p := &planner{file: file, target: target, plan: &importPlan{report: ImportReport{Items: []ImportReportItem{},
		Conflicts: []ImportConflict{}, Notes: []ImportNote{}}},
		types: map[string]*citype.ExportTypeV2{}, sets: map[string]*citype.ExportAttributeSetV2{},
		defs: map[DefKey]*citype.ExportAttributeV2{}, templates: map[string]*citype.ExportTemplateV2{},
		rules: map[string]*citype.ExportRuleV2{}, vrules: map[VersionRuleKey]*citype.ExportRuleV2{},
		layouts: map[string]*citype.ExportLayoutV2{}, created: map[string]planRef{},
		fileTypes: map[string]*citype.ExportTypeV2{}, newVersions: map[string]planRef{},
		currentVersions: map[string]string{}, copiedRules: map[string]VersionKey{}, referencedModels: map[string]bool{}}
	p.indexTarget()
	p.planTypes()
	p.planSets()
	p.planAssignments()
	p.planDefinitions()
	p.planTemplates()
	p.planRules()
	p.plan.items = append(p.plan.items, p.publish...)
	p.planLayouts()
	p.planModels()
	p.plan.items = append(p.plan.items, p.plan.activation...)
	p.plan.activation = nil
	p.noteFieldPermissions()
	r := &p.plan.report
	r.Unchanged = len(p.plan.items) == 0 && len(r.Conflicts) == 0
	return p.plan
}

// noteFieldPermissions reports the field permission sections of the file
// as not taken over (E-115): one note per section with entries, which
// creates no item and does not keep the report from being unchanged.
func (p *planner) noteFieldPermissions() {
	if g := p.file.FieldGrants; g != nil && len(*g) > 0 {
		p.note(NoteFieldPermissionsSkipped, citype.SectionFieldGrants, "",
			fmt.Sprintf("%d field grant(s) are not imported; field permissions are managed under /api/v1/field-permissions", len(*g)))
	}
	if s := p.file.FieldSensitivity; s != nil && len(*s) > 0 {
		p.note(NoteFieldPermissionsSkipped, citype.SectionFieldSensitivity, "",
			fmt.Sprintf("%d sensitive field(s) are not imported; field permissions are managed under /api/v1/field-permissions", len(*s)))
	}
}

// indexTarget indexes the parts of the target document by key; an
// organization's part goes before a system part of the same key.
func (p *planner) indexTarget() {
	doc := p.target.Doc
	if doc == nil {
		return
	}
	for i := range doc.CITypes {
		if t := &doc.CITypes[i]; p.types[t.Key] == nil || !t.IsSystem {
			p.types[t.Key] = t
		}
	}
	for i := range doc.AttributeSets {
		if s := &doc.AttributeSets[i]; p.sets[s.Key] == nil || !s.IsSystem {
			p.sets[s.Key] = s
		}
	}
	for i := range doc.AttributeDefinitions {
		d := &doc.AttributeDefinitions[i]
		k := defKey(d.CITypeKey, d.AttributeSetKey, d.Key)
		if p.defs[k] == nil || !d.IsSystem {
			p.defs[k] = d
		}
	}
	for i := range doc.Templates {
		if t := &doc.Templates[i]; p.templates[t.Key] == nil || !t.IsSystem {
			p.templates[t.Key] = t
		}
	}
	for i := range doc.Rules {
		r := &doc.Rules[i]
		if r.ExportRuleBody != nil && r.TemplateKey != "" && r.TemplateVersion != nil {
			p.vrules[VersionRuleKey{VersionKey: VersionKey{Template: r.TemplateKey, Version: *r.TemplateVersion}, Key: r.Key}] = r
			continue
		}
		if p.rules[r.Key] == nil || !r.IsSystem {
			p.rules[r.Key] = r
		}
	}
	for i := range doc.Layouts {
		p.layouts[doc.Layouts[i].Key] = &doc.Layouts[i]
	}
}

func defKey(typeKey, setKey, key string) DefKey {
	return DefKey{Type: typeKey, Set: setKey, Key: key}
}

// add appends an item and returns its reference.
func (p *planner) add(it *ImportItem) planRef {
	p.plan.items = append(p.plan.items, *it)
	return planRef(len(p.plan.items) - 1)
}

func (p *planner) report(kind, key, owner, action string) {
	p.plan.report.Items = append(p.plan.report.Items, ImportReportItem{Kind: kind, Key: key, Owner: owner, Action: action})
}

func (p *planner) conflict(c *ImportConflict) {
	p.plan.report.Conflicts = append(p.plan.report.Conflicts, *c)
}

func (p *planner) note(code, kind, key, detail string) {
	p.plan.report.Notes = append(p.plan.report.Notes, ImportNote{Code: code, Kind: kind, Key: key, Detail: detail})
}

// missing reports a reference the target cannot resolve.
func (p *planner) missing(kind, key, owner, refKind, refKey string) {
	p.conflict(&ImportConflict{Code: ConflictMissingReference, Kind: kind, Key: key, Owner: owner, Reference: refKind,
		ReferenceKey: refKey, Detail: fmt.Sprintf("the %s %q is not known in the target", strings.ReplaceAll(refKind, "_", " "), refKey)})
}

// collision reports an organization's part whose key names a system part
// of the target.
func (p *planner) collision(kind, key, owner string) {
	p.conflict(&ImportConflict{Code: ConflictSystemKeyCollision, Kind: kind, Key: key, Owner: owner,
		Detail: "the key names a system part in the target"})
}

// resolved reports a system part of the file the target has (or a missing
// one as missing-reference).
func (p *planner) resolved(kind, key, owner string, found bool) {
	if !found {
		p.missing(kind, key, owner, kind, key)
		return
	}
	p.report(kind, key, owner, ActionResolved)
}

func createdKey(kind, key string) string { return kind + "\x00" + key }

// typeRef resolves a type key: a type the plan creates or a type of the
// target (also a system type).
func (p *planner) typeRef(key string) (any, bool) {
	if ref, ok := p.created[createdKey(KindCIType, key)]; ok {
		return ref, true
	}
	if t, ok := p.target.Types[key]; ok {
		return t.ID, true
	}
	return nil, false
}

func (p *planner) setRef(key string) (any, bool) {
	if ref, ok := p.created[createdKey(KindAttributeSet, key)]; ok {
		return ref, true
	}
	if s, ok := p.target.Sets[key]; ok {
		return s.ID, true
	}
	return nil, false
}

func (p *planner) templateRef(key string) (any, bool) {
	if ref, ok := p.created[createdKey(KindTemplate, key)]; ok {
		return ref, true
	}
	if t, ok := p.target.Templates[key]; ok {
		return t.ID, true
	}
	return nil, false
}

func (p *planner) defRef(k DefKey) (any, bool) {
	if ref, ok := p.created[createdKey(KindAttributeDefinition, k.Type+"\x00"+k.Set+"\x00"+k.Key)]; ok {
		return ref, true
	}
	if d, ok := p.target.Defs[k]; ok {
		return d.ID, true
	}
	return nil, false
}

// modelRef resolves a lifecycle model key: models are never created.
func (p *planner) modelRef(key string) (string, bool) {
	m, ok := p.target.Models[key]
	return m.ID, ok
}

// ownerOf names the owner of an attribute definition in the report.
func ownerOf(typeKey, setKey string) string {
	switch {
	case typeKey != "":
		return KindCIType + ":" + typeKey
	case setKey != "":
		return KindAttributeSet + ":" + setKey
	}
	return ""
}

// equalJSON compares two values by their JSON encoding; a nil value equals
// an empty one.
func equalJSON(a, b any) bool {
	ra, ea := json.Marshal(a)
	rb, eb := json.Marshal(b)
	if ea != nil || eb != nil {
		return false
	}
	return bytes.Equal(normalizeEmpty(ra), normalizeEmpty(rb))
}

// normalizeEmpty maps the empty values null, [], {} and "" onto null.
func normalizeEmpty(raw []byte) []byte {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "[]", "{}", `""`:
		return []byte("null")
	}
	return raw
}

// rawOrNil is a JSON value for a payload; nil for an empty one.
func rawOrNil(raw json.RawMessage) any {
	if string(normalizeEmpty(raw)) == "null" {
		return nil
	}
	return raw
}

// setIf sets key in m when v is no empty value.
func setIf(m map[string]any, key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil || string(normalizeEmpty(raw)) == "null" {
		return
	}
	m[key] = v
}

// ─── Types ─────────────────────────────────────────────────────────────────

// planTypes plans the types: system types are resolved; the organization's
// types are created or updated, parents before their subtypes.
func (p *planner) planTypes() {
	var own []*citype.ExportTypeV2
	for i := range p.file.CITypes {
		t := &p.file.CITypes[i]
		if t.IsSystem || t.ExportTypeBody == nil {
			target, ok := p.target.Types[t.Key]
			p.resolved(KindCIType, t.Key, "", ok && target.System)
			continue
		}
		p.fileTypes[t.Key] = t
		own = append(own, t)
	}
	done := map[string]bool{}
	var visit func(t *citype.ExportTypeV2, depth int)
	visit = func(t *citype.ExportTypeV2, depth int) {
		if done[t.Key] || depth > len(own) {
			return
		}
		done[t.Key] = true
		if parent, ok := p.fileTypes[t.ParentTypeKey]; ok && parent != t {
			visit(parent, depth+1)
		}
		p.planType(t)
	}
	for _, t := range own {
		visit(t, 0)
	}
}

// planType plans one type of the organization.
func (p *planner) planType(t *citype.ExportTypeV2) {
	b := t.ExportTypeBody
	if existing, ok := p.target.Types[t.Key]; ok && existing.System {
		p.collision(KindCIType, t.Key, "")
		return
	}
	refs, ok := p.typeReferences(t)
	if !ok {
		return
	}
	existing, exists := p.target.Types[t.Key]
	if !exists {
		payload := map[string]any{"key": t.Key, "name": b.Name}
		setIf(payload, "display_name", b.DisplayName)
		setIf(payload, "icon", b.Icon)
		setIf(payload, "description", b.Description)
		setIf(payload, "category", b.Category)
		setIf(payload, "is_logical", b.IsLogical)
		payload["is_observable"] = b.IsObservable
		if refs.parent != nil {
			payload["parent_type_id"] = refs.parent
		}
		setIf(payload, "template_key", b.TemplateKey)
		setIf(payload, "lifecycle_model_id", refs.model)
		setIf(payload, "capabilities", b.Capabilities)
		setIf(payload, "allowed_relationship_types", b.AllowedRelationshipTypes)
		setIf(payload, "ui_schema", b.UISchema)
		setIf(payload, "compliance_rules", b.ComplianceRules)
		setIf(payload, "discovery_mappings", b.DiscoveryMappings)
		ref := p.add(&ImportItem{Kind: KindCIType, Op: OpCreate, Payload: payload})
		p.created[createdKey(KindCIType, t.Key)] = ref
		if !b.IsActive {
			p.plan.activation = append(p.plan.activation, ImportItem{Kind: KindCIType, Op: OpDeactivate, Target: ref})
		}
		p.report(KindCIType, t.Key, "", ActionCreate)
		return
	}
	cur := p.types[t.Key]
	if cur == nil || cur.ExportTypeBody == nil {
		return
	}
	c := cur.ExportTypeBody
	patch := map[string]any{}
	diff := func(key string, want, have any) {
		if !equalJSON(want, have) {
			patch[key] = want
		}
	}
	diff("name", b.Name, c.Name)
	diff("display_name", b.DisplayName, c.DisplayName)
	diff("icon", b.Icon, c.Icon)
	diff("description", b.Description, c.Description)
	diff("category", b.Category, c.Category)
	if b.IsLogical != c.IsLogical {
		patch["is_logical"] = b.IsLogical
	}
	if b.IsObservable != c.IsObservable {
		patch["is_observable"] = b.IsObservable
	}
	if b.ParentTypeKey != c.ParentTypeKey {
		patch["parent_type_id"] = refs.parent
		if refs.parent == nil {
			patch["parent_type_id"] = ""
		}
	}
	if b.LifecycleModelKey != c.LifecycleModelKey {
		patch["lifecycle_model_id"] = refs.model
	}
	for _, f := range []struct {
		key        string
		want, have any
	}{
		{"capabilities", b.Capabilities, c.Capabilities},
		{"allowed_relationship_types", b.AllowedRelationshipTypes, c.AllowedRelationshipTypes},
		{"ui_schema", b.UISchema, c.UISchema},
		{"compliance_rules", b.ComplianceRules, c.ComplianceRules},
		{"discovery_mappings", b.DiscoveryMappings, c.DiscoveryMappings},
	} {
		diff(f.key, emptyContainer(f.want), f.have)
	}
	if b.TemplateKey != c.TemplateKey {
		p.note(NoteNotImported, KindCIType, t.Key, "template_key of an existing type cannot be changed; the target keeps "+
			strconv.Quote(c.TemplateKey))
	}
	action := ActionUnchanged
	if len(patch) > 0 {
		p.add(&ImportItem{Kind: KindCIType, Op: OpUpdate, Target: existing.ID, Payload: patch})
		action = ActionUpdate
	}
	if b.IsActive != c.IsActive {
		op := OpDeactivate
		if b.IsActive {
			op = OpActivate
		}
		p.plan.activation = append(p.plan.activation, ImportItem{Kind: KindCIType, Op: op, Target: existing.ID})
		action = ActionUpdate
	}
	if !slices.Equal(b.AttributeSetKeys, c.AttributeSetKeys) {
		action = ActionUpdate
	}
	p.report(KindCIType, t.Key, "", action)
}

// emptyContainer replaces a nil map or slice by an empty one, so a patch
// clears the field.
func emptyContainer(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if x == nil {
			return map[string]any{}
		}
	case []string:
		if x == nil {
			return []string{}
		}
	case []any:
		if x == nil {
			return []any{}
		}
	}
	return v
}

// typeRefs are the resolved references of a type.
type typeRefs struct {
	parent any
	model  string
}

// typeReferences resolves parent, lifecycle model and relationship types of
// a type; a missing one is a conflict.
func (p *planner) typeReferences(t *citype.ExportTypeV2) (typeRefs, bool) {
	b := t.ExportTypeBody
	var out typeRefs
	ok := true
	if b.ParentTypeKey != "" {
		parent, found := p.typeRef(b.ParentTypeKey)
		if !found {
			p.missing(KindCIType, t.Key, "", kindParentType, b.ParentTypeKey)
			ok = false
		}
		out.parent = parent
	}
	if b.LifecycleModelKey != "" {
		p.referencedModels[b.LifecycleModelKey] = true
		model, found := p.modelRef(b.LifecycleModelKey)
		if !found {
			p.missing(KindCIType, t.Key, "", KindLifecycleModel, b.LifecycleModelKey)
			ok = false
		}
		out.model = model
	}
	for _, rel := range b.AllowedRelationshipTypes {
		if !p.target.RelationshipTypes[rel] {
			p.missing(KindCIType, t.Key, "", kindRelationshipType, rel)
			ok = false
		}
	}
	return out, ok
}

// ─── Attribute sets and assignments ────────────────────────────────────────

func (p *planner) planSets() {
	for i := range p.file.AttributeSets {
		s := &p.file.AttributeSets[i]
		target, exists := p.target.Sets[s.Key]
		if s.IsSystem || s.ExportAttributeSetBody == nil {
			p.resolved(KindAttributeSet, s.Key, "", exists && target.System)
			continue
		}
		b := s.ExportAttributeSetBody
		switch {
		case exists && target.System:
			p.collision(KindAttributeSet, s.Key, "")
		case !exists:
			payload := map[string]any{"key": s.Key, "is_active": b.IsActive}
			setIf(payload, "display_name", b.DisplayName)
			setIf(payload, "description", b.Description)
			p.created[createdKey(KindAttributeSet, s.Key)] = p.add(&ImportItem{Kind: KindAttributeSet, Op: OpCreate, Payload: payload})
			p.report(KindAttributeSet, s.Key, "", ActionCreate)
		default:
			cur := p.sets[s.Key]
			patch := map[string]any{}
			if cur != nil && cur.ExportAttributeSetBody != nil {
				c := cur.ExportAttributeSetBody
				if !equalJSON(b.DisplayName, c.DisplayName) {
					patch["display_name"] = b.DisplayName
				}
				if b.Description != c.Description {
					patch["description"] = b.Description
				}
				if b.IsActive != c.IsActive {
					patch["is_active"] = b.IsActive
				}
			}
			if len(patch) == 0 {
				p.report(KindAttributeSet, s.Key, "", ActionUnchanged)
				continue
			}
			p.add(&ImportItem{Kind: KindAttributeSet, Op: OpUpdate, Target: target.ID, Payload: patch})
			p.report(KindAttributeSet, s.Key, "", ActionUpdate)
		}
	}
}

// planAssignments plans the set assignment of the types the plan creates
// with sets, or whose sets differ, after the types and sets.
func (p *planner) planAssignments() {
	for i := range p.file.CITypes {
		t := &p.file.CITypes[i]
		if t.IsSystem || t.ExportTypeBody == nil {
			continue
		}
		var target any
		if ref, ok := p.created[createdKey(KindCIType, t.Key)]; ok {
			if len(t.AttributeSetKeys) == 0 {
				continue
			}
			target = ref
		} else {
			existing, ok := p.target.Types[t.Key]
			cur := p.types[t.Key]
			if !ok || existing.System || cur == nil || cur.ExportTypeBody == nil || slices.Equal(t.AttributeSetKeys, cur.AttributeSetKeys) {
				continue
			}
			target = existing.ID
		}
		list := make([]any, 0, len(t.AttributeSetKeys))
		resolvedAll := true
		for pos, key := range t.AttributeSetKeys {
			set, ok := p.setRef(key)
			if !ok {
				p.missing(KindCIType, t.Key, "", KindAttributeSet, key)
				resolvedAll = false
				continue
			}
			list = append(list, map[string]any{"attribute_set_id": set, "position": pos})
		}
		if resolvedAll {
			p.add(&ImportItem{Kind: KindCIType, Op: OpAssign, Target: target, Payload: map[string]any{"attribute_sets": list}})
		}
	}
}

// ─── Attribute definitions ─────────────────────────────────────────────────

func (p *planner) planDefinitions() {
	for i := range p.file.AttributeDefinitions {
		d := &p.file.AttributeDefinitions[i]
		k := defKey(d.CITypeKey, d.AttributeSetKey, d.Key)
		owner := ownerOf(d.CITypeKey, d.AttributeSetKey)
		target, exists := p.target.Defs[k]
		if d.IsSystem || d.ExportAttributeBody == nil {
			p.resolved(KindAttributeDefinition, d.Key, owner, exists && target.System)
			continue
		}
		if exists && target.System {
			p.collision(KindAttributeDefinition, d.Key, owner)
			continue
		}
		if exists {
			p.updateDefinition(d, k, target.ID)
			continue
		}
		p.createDefinition(d, k)
	}
}

// definitionPayload is the body of a definition as request fields; ok is
// false when a referenced type is missing.
func (p *planner) definitionPayload(d *citype.ExportAttributeV2) (map[string]any, bool) {
	b := d.ExportAttributeBody
	owner := ownerOf(d.CITypeKey, d.AttributeSetKey)
	out := map[string]any{
		"display_name": b.DisplayName, "description": b.Description, "data_type": b.DataType, "required": b.Required,
		"default": rawOrNil(b.Default), "enum_values": b.EnumValues, "multi_value": b.MultiValue, "searchable": b.Searchable,
		"unique": b.Unique, "unique_scope": b.UniqueScope, "inheritable": b.Inheritable, "validation": b.Validation,
		"computed": b.Computed, "conditional": b.Conditional, "reference_target": b.ReferenceTarget, "group": b.Group,
		"order": b.Order, "unit": b.Unit, "applies_to": b.AppliesTo, "applies_to_categories": b.AppliesToCategories,
	}
	ok := true
	types := make([]any, 0, len(b.AppliesToTypeKeys))
	for _, key := range b.AppliesToTypeKeys {
		ref, found := p.typeRef(key)
		if !found {
			p.missing(KindAttributeDefinition, d.Key, owner, KindCIType, key)
			ok = false
			continue
		}
		types = append(types, ref)
	}
	out["applies_to_type_ids"] = types
	return out, ok
}

func (p *planner) createDefinition(d *citype.ExportAttributeV2, k DefKey) {
	owner := ownerOf(d.CITypeKey, d.AttributeSetKey)
	fields, ok := p.definitionPayload(d)
	payload := map[string]any{"key": d.Key}
	if d.CITypeKey != "" {
		ref, found := p.typeRef(d.CITypeKey)
		if !found {
			p.missing(KindAttributeDefinition, d.Key, owner, KindCIType, d.CITypeKey)
			ok = false
		}
		payload["ci_type_id"] = ref
	}
	if d.AttributeSetKey != "" {
		ref, found := p.setRef(d.AttributeSetKey)
		if !found {
			p.missing(KindAttributeDefinition, d.Key, owner, KindAttributeSet, d.AttributeSetKey)
			ok = false
		}
		payload["attribute_set_id"] = ref
	}
	if !ok {
		return
	}
	for key, v := range fields {
		if key == "applies_to" && v == citype.AppliesToAll {
			continue
		}
		setIf(payload, key, v)
	}
	// Booleans whose zero value differs from the default of a new
	// definition (inheritable) go along as they are.
	payload["inheritable"] = d.Inheritable
	ref := p.add(&ImportItem{Kind: KindAttributeDefinition, Op: OpCreate, Payload: payload})
	p.created[createdKey(KindAttributeDefinition, k.Type+"\x00"+k.Set+"\x00"+k.Key)] = ref
	p.report(KindAttributeDefinition, d.Key, owner, ActionCreate)
}

func (p *planner) updateDefinition(d *citype.ExportAttributeV2, k DefKey, id string) {
	owner := ownerOf(d.CITypeKey, d.AttributeSetKey)
	cur := p.defs[k]
	if cur == nil || cur.ExportAttributeBody == nil {
		return
	}
	if cur.DataType != d.DataType {
		p.conflict(&ImportConflict{Code: ConflictDatatype, Kind: KindAttributeDefinition, Key: d.Key, Owner: owner,
			Detail: fmt.Sprintf("the attribute has the data type %q in the target, %q in the file", cur.DataType, d.DataType)})
		return
	}
	want, ok := p.definitionPayload(d)
	if !ok {
		return
	}
	have := map[string]any{
		"display_name": cur.DisplayName, "description": cur.Description, "data_type": cur.DataType, "required": cur.Required,
		"default": rawOrNil(cur.Default), "enum_values": cur.EnumValues, "multi_value": cur.MultiValue,
		"searchable": cur.Searchable, "unique": cur.Unique, "unique_scope": cur.UniqueScope, "inheritable": cur.Inheritable,
		"validation": cur.Validation, "computed": cur.Computed, "conditional": cur.Conditional,
		"reference_target": cur.ReferenceTarget, "group": cur.Group, "order": cur.Order, "unit": cur.Unit,
		"applies_to": cur.AppliesTo, "applies_to_categories": cur.AppliesToCategories,
	}
	patch := map[string]any{}
	for key, v := range want {
		if key == "applies_to_type_ids" {
			continue
		}
		if !equalJSON(v, have[key]) {
			patch[key] = clearValue(key, v)
		}
	}
	if !slices.Equal(d.AppliesToTypeKeys, cur.AppliesToTypeKeys) {
		patch["applies_to_type_ids"] = want["applies_to_type_ids"]
	}
	if len(patch) == 0 {
		p.report(KindAttributeDefinition, d.Key, owner, ActionUnchanged)
		return
	}
	p.add(&ImportItem{Kind: KindAttributeDefinition, Op: OpUpdate, Target: id, Payload: patch})
	p.report(KindAttributeDefinition, d.Key, owner, ActionUpdate)
}

// clearValue is the patch value of a field the file leaves empty: a nil
// list becomes [], a nil rule null (validation, computed, default).
func clearValue(key string, v any) any {
	switch key {
	case "enum_values", "applies_to_categories":
		if l, ok := v.([]string); ok && l == nil {
			return []string{}
		}
	}
	return v
}

// ─── Templates ─────────────────────────────────────────────────────────────

func (p *planner) planTemplates() {
	for i := range p.file.Templates {
		t := &p.file.Templates[i]
		target, exists := p.target.Templates[t.Key]
		if t.IsSystem || t.ExportTemplateBody == nil {
			p.resolved(KindTemplate, t.Key, "", exists && target.System)
			continue
		}
		if exists && target.System {
			p.collision(KindTemplate, t.Key, "")
			continue
		}
		p.planTemplate(t, target.ID, exists)
	}
}

// planTemplate plans one template of the organization and its current
// version.
func (p *planner) planTemplate(t *citype.ExportTemplateV2, id string, exists bool) {
	b := t.ExportTemplateBody
	typeRef, ok := p.typeRef(b.CITypeKey)
	if !ok {
		p.missing(KindTemplate, t.Key, "", KindCIType, b.CITypeKey)
		return
	}
	var current *citype.ExportTemplateVersionV2
	skipped := 0
	for j := range b.Versions {
		v := &b.Versions[j]
		if b.CurrentVersion != nil && v.Version == *b.CurrentVersion {
			current = v
			continue
		}
		skipped++
	}
	if skipped > 0 {
		p.note(NoteHistorySkipped, KindTemplate, t.Key, fmt.Sprintf("%d version(s) besides the current published one are not taken over", skipped))
	}
	var template any = id
	if !exists {
		payload := map[string]any{"key": t.Key, "ci_type_id": typeRef, "is_active": b.IsActive}
		setIf(payload, "display_name", b.DisplayName)
		setIf(payload, "description", b.Description)
		ref := p.add(&ImportItem{Kind: KindTemplate, Op: OpCreate, Payload: payload})
		p.created[createdKey(KindTemplate, t.Key)] = ref
		template = ref
		p.report(KindTemplate, t.Key, "", ActionCreate)
	} else {
		p.updateTemplate(t)
	}
	if current == nil {
		return
	}
	items, ok := p.templateItems(t.Key, current)
	if !ok {
		return
	}
	if cur := p.templates[t.Key]; exists && cur != nil && cur.ExportTemplateBody != nil {
		latest := 0
		for j := range cur.Versions {
			v := &cur.Versions[j]
			latest = max(latest, v.Version)
			if cur.CurrentVersion != nil && v.Version == *cur.CurrentVersion && equalJSON(v.Items, current.Items) &&
				p.sameVersionRules(t.Key, current.Version, v.Version) {
				p.currentVersions[t.Key] = p.target.Versions[VersionKey{Template: t.Key, Version: v.Version}]
				p.report(KindTemplateVersion, t.Key, "", ActionUnchanged)
				return
			}
		}
		if latest > 0 {
			p.copiedRules[t.Key] = VersionKey{Template: t.Key, Version: latest}
		}
	}
	version := p.add(&ImportItem{Kind: KindTemplateVersion, Op: OpCreate, Target: template})
	p.add(&ImportItem{Kind: KindTemplateVersion, Op: OpUpdate, Target: version, Template: template,
		Payload: map[string]any{"items": items}})
	// Published after the rules of the version: the rules of a published
	// version are immutable.
	p.publish = append(p.publish, ImportItem{Kind: KindTemplateVersion, Op: OpPublish, Target: version, Template: template})
	p.newVersions[t.Key] = version
	p.report(KindTemplateVersion, t.Key, "", ActionCreate)
}

func (p *planner) updateTemplate(t *citype.ExportTemplateV2) {
	b := t.ExportTemplateBody
	cur := p.templates[t.Key]
	if cur == nil || cur.ExportTemplateBody == nil {
		return
	}
	c := cur.ExportTemplateBody
	if b.CITypeKey != c.CITypeKey {
		p.note(NoteNotImported, KindTemplate, t.Key, "the ci type of an existing template cannot be changed; the target keeps "+
			strconv.Quote(c.CITypeKey))
	}
	patch := map[string]any{}
	if !equalJSON(b.DisplayName, c.DisplayName) {
		patch["display_name"] = b.DisplayName
	}
	if b.Description != c.Description {
		patch["description"] = b.Description
	}
	if b.IsActive != c.IsActive {
		patch["is_active"] = b.IsActive
	}
	if len(patch) == 0 {
		p.report(KindTemplate, t.Key, "", ActionUnchanged)
		return
	}
	p.add(&ImportItem{Kind: KindTemplate, Op: OpUpdate, Target: p.target.Templates[t.Key].ID, Payload: patch})
	p.report(KindTemplate, t.Key, "", ActionUpdate)
}

// templateItems resolves the items of a template version onto attribute
// definitions.
func (p *planner) templateItems(templateKey string, v *citype.ExportTemplateVersionV2) ([]any, bool) {
	out := make([]any, 0, len(v.Items))
	ok := true
	for _, it := range v.Items {
		def, found := p.defRef(defKey(it.CITypeKey, it.AttributeSetKey, it.AttributeKey))
		if !found {
			p.missing(KindTemplateVersion, templateKey, "", KindAttributeDefinition, ownerKey(it.CITypeKey, it.AttributeSetKey, it.AttributeKey))
			ok = false
			continue
		}
		item := map[string]any{"attribute_definition_id": def, "position": it.Position}
		if it.RequiredOverride != nil {
			item["required_override"] = *it.RequiredOverride
		}
		setIf(item, "default_override", rawOrNil(it.DefaultOverride))
		setIf(item, "validation_override", rawOrNil(it.ValidationOverride))
		out = append(out, item)
	}
	return out, ok
}

// sameVersionRules reports whether the rules of version fileVersion of the
// template in the file equal those of version targetVersion in the target
// (keys and content).
func (p *planner) sameVersionRules(templateKey string, fileVersion, targetVersion int) bool {
	want := map[string]*citype.ExportRuleBody{}
	for i := range p.file.Rules {
		r := &p.file.Rules[i]
		if r.ExportRuleBody != nil && r.TemplateKey == templateKey && r.TemplateVersion != nil && *r.TemplateVersion == fileVersion {
			want[r.Key] = r.ExportRuleBody
		}
	}
	have := map[string]*citype.ExportRuleBody{}
	for k, r := range p.vrules {
		if k.Template == templateKey && k.Version == targetVersion && r.ExportRuleBody != nil {
			have[k.Key] = r.ExportRuleBody
		}
	}
	if len(want) != len(have) {
		return false
	}
	for key, body := range want {
		if other, ok := have[key]; !ok || !equalJSON(ruleFields(body), ruleFields(other)) {
			return false
		}
	}
	return true
}

// ownerKey names an attribute with its owner ("ci_type:server/os").
func ownerKey(typeKey, setKey, key string) string {
	if owner := ownerOf(typeKey, setKey); owner != "" {
		return owner + "/" + key
	}
	return key
}

// ─── Rules ─────────────────────────────────────────────────────────────────

func (p *planner) planRules() {
	skipped := map[string]int{}
	for i := range p.file.Rules {
		r := &p.file.Rules[i]
		if r.IsSystem || r.ExportRuleBody == nil {
			target, ok := p.target.Rules[r.Key]
			p.resolved(KindRule, r.Key, "", ok && target.System)
			continue
		}
		if r.TemplateKey != "" {
			if !p.isCurrentVersion(r.TemplateKey, r.TemplateVersion) {
				skipped[r.TemplateKey]++
				continue
			}
			p.planVersionRule(r)
			continue
		}
		p.planRule(r)
	}
	keys := make([]string, 0, len(skipped))
	for key := range skipped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.note(NoteHistorySkipped, KindRule, key, fmt.Sprintf("%d rule(s) of older versions of the template are not taken over", skipped[key]))
	}
}

// isCurrentVersion reports whether version is the current version of the
// template in the file.
func (p *planner) isCurrentVersion(templateKey string, version *int) bool {
	if version == nil {
		return false
	}
	for i := range p.file.Templates {
		t := &p.file.Templates[i]
		if t.Key != templateKey {
			continue
		}
		if t.ExportTemplateBody == nil {
			// A system template: its versions are the same in every
			// organization.
			return true
		}
		return t.CurrentVersion != nil && *t.CurrentVersion == *version
	}
	return false
}

// ruleFields are the changeable fields of a rule as request fields.
func ruleFields(b *citype.ExportRuleBody) map[string]any {
	return map[string]any{"attribute_key": b.AttributeKey, "kind": b.Kind, "condition": rawOrNil(b.Condition),
		"ast": rawOrNil(b.AST), "message": b.Message, "is_active": b.IsActive}
}

// planRule plans a rule of a type or set.
func (p *planner) planRule(r *citype.ExportRuleV2) {
	target, exists := p.target.Rules[r.Key]
	if exists && target.System {
		p.collision(KindRule, r.Key, "")
		return
	}
	if !exists {
		payload := map[string]any{"key": r.Key}
		switch {
		case r.CITypeKey != "":
			ref, ok := p.typeRef(r.CITypeKey)
			if !ok {
				p.missing(KindRule, r.Key, "", KindCIType, r.CITypeKey)
				return
			}
			payload["ci_type_id"] = ref
		case r.AttributeSetKey != "":
			ref, ok := p.setRef(r.AttributeSetKey)
			if !ok {
				p.missing(KindRule, r.Key, "", KindAttributeSet, r.AttributeSetKey)
				return
			}
			payload["attribute_set_id"] = ref
		}
		p.createRule(r, payload, "")
		return
	}
	cur := p.rules[r.Key]
	if cur != nil && cur.ExportRuleBody != nil && (cur.CITypeKey != r.CITypeKey || cur.AttributeSetKey != r.AttributeSetKey) {
		p.note(NoteNotImported, KindRule, r.Key, "the target of an existing rule cannot be changed")
	}
	p.updateRule(r, cur, target.ID, "")
}

// planVersionRule plans a rule of the current version of a template: on the
// version the plan creates, or on the current version the target keeps.
func (p *planner) planVersionRule(r *citype.ExportRuleV2) {
	owner := KindTemplate + ":" + r.TemplateKey
	if ref, ok := p.newVersions[r.TemplateKey]; ok {
		// The new version of an existing template starts with copies of
		// the rules of its latest version; a copy cannot be addressed by
		// an item before it exists.
		if from, copied := p.copiedRules[r.TemplateKey]; copied {
			if cur := p.vrules[VersionRuleKey{VersionKey: from, Key: r.Key}]; cur != nil {
				if cur.ExportRuleBody != nil && equalJSON(ruleFields(r.ExportRuleBody), ruleFields(cur.ExportRuleBody)) {
					p.report(KindRule, r.Key, owner, ActionUnchanged)
					return
				}
				p.note(NoteNotImported, KindRule, r.Key, fmt.Sprintf("the new version of template %q starts with a copy of "+
					"this rule of version %d; its changes are not taken over", r.TemplateKey, from.Version))
				return
			}
		}
		p.createRule(r, map[string]any{"key": r.Key, "template_version_id": ref}, owner)
		return
	}
	// The target keeps its current version: the rules of the file equal
	// its rules (planTemplate), or the template is a system template,
	// whose published versions an organization cannot change.
	if _, kept := p.currentVersions[r.TemplateKey]; kept {
		p.report(KindRule, r.Key, owner, ActionUnchanged)
		return
	}
	if _, ok := p.target.Versions[VersionKey{Template: r.TemplateKey, Version: *r.TemplateVersion}]; !ok {
		p.missing(KindRule, r.Key, owner, KindTemplateVersion, r.TemplateKey)
		return
	}
	if p.versionRuleUnchanged(r) {
		p.report(KindRule, r.Key, owner, ActionUnchanged)
		return
	}
	p.note(NoteNotImported, KindRule, r.Key, "the rules of a published version of a system template cannot be changed")
}

// versionRuleUnchanged reports whether the target has the rule of a template
// version with the same content.
func (p *planner) versionRuleUnchanged(r *citype.ExportRuleV2) bool {
	cur := p.vrules[VersionRuleKey{VersionKey: VersionKey{Template: r.TemplateKey, Version: *r.TemplateVersion}, Key: r.Key}]
	return cur != nil && cur.ExportRuleBody != nil && equalJSON(ruleFields(r.ExportRuleBody), ruleFields(cur.ExportRuleBody))
}

func (p *planner) createRule(r *citype.ExportRuleV2, payload map[string]any, owner string) {
	for key, v := range ruleFields(r.ExportRuleBody) {
		setIf(payload, key, v)
	}
	payload["is_active"] = r.IsActive
	p.add(&ImportItem{Kind: KindRule, Op: OpCreate, Payload: payload})
	p.report(KindRule, r.Key, owner, ActionCreate)
}

func (p *planner) updateRule(r, cur *citype.ExportRuleV2, id, owner string) {
	if cur == nil || cur.ExportRuleBody == nil {
		return
	}
	want, have := ruleFields(r.ExportRuleBody), ruleFields(cur.ExportRuleBody)
	patch := map[string]any{}
	for key, v := range want {
		if !equalJSON(v, have[key]) {
			patch[key] = v
		}
	}
	if len(patch) == 0 {
		p.report(KindRule, r.Key, owner, ActionUnchanged)
		return
	}
	p.add(&ImportItem{Kind: KindRule, Op: OpUpdate, Target: id, Payload: patch})
	p.report(KindRule, r.Key, owner, ActionUpdate)
}

// ─── Layouts ───────────────────────────────────────────────────────────────

func (p *planner) planLayouts() {
	for i := range p.file.Layouts {
		l := &p.file.Layouts[i]
		role, ok := p.layoutRole(l)
		if !ok {
			continue
		}
		id, exists := p.target.Layouts[l.Key]
		if !exists {
			payload := map[string]any{"key": l.Key, "priority": l.Priority, "is_active": l.IsActive, "layout": l.Layout}
			switch {
			case l.CITypeKey != "":
				ref, found := p.typeRef(l.CITypeKey)
				if !found {
					p.missing(KindLayout, l.Key, "", KindCIType, l.CITypeKey)
					continue
				}
				payload["ci_type_id"] = ref
			case l.TemplateKey != "":
				ref, found := p.templateRef(l.TemplateKey)
				if !found {
					p.missing(KindLayout, l.Key, "", KindTemplate, l.TemplateKey)
					continue
				}
				payload["template_id"] = ref
			}
			if role != nil {
				payload["role_id"] = role
			}
			p.add(&ImportItem{Kind: KindLayout, Op: OpCreate, Payload: payload})
			p.report(KindLayout, l.Key, "", ActionCreate)
			continue
		}
		cur := p.layouts[l.Key]
		if cur == nil {
			continue
		}
		if cur.CITypeKey != l.CITypeKey || cur.TemplateKey != l.TemplateKey {
			p.note(NoteNotImported, KindLayout, l.Key, "the target of an existing layout cannot be changed")
		}
		patch := map[string]any{}
		if l.Priority != cur.Priority {
			patch["priority"] = l.Priority
		}
		if l.IsActive != cur.IsActive {
			patch["is_active"] = l.IsActive
		}
		if !equalJSON(l.Layout, cur.Layout) {
			patch["layout"] = l.Layout
		}
		if l.RoleName != cur.RoleName || l.RoleKind != cur.RoleKind {
			patch["role_id"] = role
		}
		if len(patch) == 0 {
			p.report(KindLayout, l.Key, "", ActionUnchanged)
			continue
		}
		p.add(&ImportItem{Kind: KindLayout, Op: OpUpdate, Target: id, Payload: patch})
		p.report(KindLayout, l.Key, "", ActionUpdate)
	}
}

// layoutRole resolves the role of a layout by its name: exactly one role or
// custom role of the target must carry it. nil is no role.
func (p *planner) layoutRole(l *citype.ExportLayoutV2) (any, bool) {
	if l.RoleName == "" {
		return nil, true
	}
	ids := p.target.Roles[l.RoleName]
	if len(ids) != 1 {
		detail := fmt.Sprintf("no role is named %q in the target", l.RoleName)
		if len(ids) > 1 {
			detail = fmt.Sprintf("%d roles are named %q in the target", len(ids), l.RoleName)
		}
		p.conflict(&ImportConflict{Code: ConflictMissingReference, Kind: KindLayout, Key: l.Key, Reference: kindRole,
			ReferenceKey: l.RoleName, Detail: detail})
		return nil, false
	}
	return ids[0], true
}

// ─── Lifecycle models ──────────────────────────────────────────────────────

// planModels resolves the lifecycle models of the file: they are never
// created (WP-723); one the target lacks is a conflict where a type uses
// it (planTypes) and a note otherwise.
func (p *planner) planModels() {
	for i := range p.file.LifecycleModels {
		m := &p.file.LifecycleModels[i]
		if _, ok := p.target.Models[m.Key]; ok {
			p.report(KindLifecycleModel, m.Key, "", ActionResolved)
			continue
		}
		if !p.referencedModels[m.Key] {
			p.note(NoteNotImported, KindLifecycleModel, m.Key,
				"lifecycle models are not created by the import; create it under /api/v1/lifecycle-models")
		}
	}
}

// ─── Draft capture ─────────────────────────────────────────────────────────

// ImportDraftStore creates the draft of an import with its items in one
// transaction; PGRepository implements it.
type ImportDraftStore interface {
	CreateImportDraft(ctx context.Context, orgID, name string, items []ImportItem) (*Draft, error)
}

var _ ImportDraftStore = (*PGRepository)(nil)

// CreateImportDraft implements ImportDraftStore: the draft and every item in
// one tenant transaction (database.WithRequestTenantTx). An item is
// captured like POST …/items (payload check, base of its target); an item
// whose target another item of the draft creates is captured without base,
// with the creating item as its target (E-112).
func (r *PGRepository) CreateImportDraft(ctx context.Context, orgID, name string, items []ImportItem) (*Draft, error) {
	var out *Draft
	err := database.WithRequestTenantTx(ctx, r.pool, orgID, func(ctx context.Context) error {
		d, err := r.CreateDraft(ctx, orgID, &DraftInput{Name: name})
		if err != nil {
			return err
		}
		ids := make([]string, len(items))
		for i := range items {
			it := &items[i]
			payload, merr := json.Marshal(materialize(it.Payload, ids))
			if merr != nil {
				return fmt.Errorf("encode import item: %w", merr)
			}
			in := &ItemInput{Kind: it.Kind, Op: it.Op, Payload: payload}
			ref, linked := it.Target.(planRef)
			if linked {
				in.TargetID = ids[ref]
			} else if id, isID := it.Target.(string); isID {
				in.TargetID = id
			}
			c, cerr := checkItemPayload(in)
			if cerr != nil {
				return cerr
			}
			var added *Item
			if linked {
				d, added, err = r.addLinkedItem(ctx, orgID, d.ID, c, linkedRoute(c, it.Template, ids))
			} else {
				d, added, err = r.AddItem(ctx, orgID, d.ID, c, nil)
			}
			if err != nil {
				return err
			}
			ids[i] = added.ID
		}
		out = d
		return nil
	})
	return out, err
}

// materialize replaces every planRef in v by an item reference to the id
// the item got.
func materialize(v any, ids []string) any {
	switch x := v.(type) {
	case planRef:
		return map[string]any{"$item": ids[x]}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			out[k] = materialize(child, ids)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = materialize(child, ids)
		}
		return out
	}
	return v
}

// Placeholders of a route path that publishApply resolves (E-112): the id
// the item created, and the number of the template version it created.
const (
	routeItemPrefix    = "{$item:"
	routeVersionPrefix = "{$version:"
)

// linkedRoute is the route of an item whose target another item creates:
// the target id is the placeholder of that item; a template version update
// or publish names its template (id or the placeholder of its creating
// item) and the number of the version its target item created.
func linkedRoute(c *capture, template any, ids []string) string {
	target := routeItemPrefix + c.TargetID + "}"
	if c.Kind == KindTemplateVersion && c.Op != OpCreate {
		templateID, _ := template.(string)
		if ref, ok := template.(planRef); ok {
			templateID = routeItemPrefix + ids[ref] + "}"
		}
		return route(c.Spec.Route, "{id}", templateID, "{version}", routeVersionPrefix+c.TargetID+"}")
	}
	return route(c.Spec.Route, "{id}", target)
}

// addLinkedItem captures an item whose target is an earlier item of the
// draft that creates a part: no base (the target does not exist yet), the
// route with placeholders.
func (r *PGRepository) addLinkedItem(ctx context.Context, orgID, draftID string, c *capture, itemRoute string) (*Draft, *Item, error) {
	var added *Item
	d, err := r.mutate(ctx, orgID, draftID, nil, "metamodel_draft.item_added", func(ctx context.Context, _ pgx.Tx, d *Draft) error {
		itemID, err := newUUID()
		if err != nil {
			return err
		}
		if refErr := checkRefs(d.Items, itemID, append(slices.Clone(c.Refs), c.TargetID)); refErr != nil {
			return refErr
		}
		target := c.TargetID
		it := &Item{ID: itemID, Kind: c.Kind, Op: c.Op, TargetID: &target, Payload: c.Payload, Base: json.RawMessage("null"),
			SchemaVersions: map[string]int64{}, Route: itemRoute, CreatedBy: actor(ctx), CreatedAt: time.Now().UTC()}
		d.Items = append(d.Items, *it)
		resetTest(d)
		added = it
		return nil
	})
	return d, added, err
}

// errUnresolvedRoute refuses a route placeholder that names no item of the
// publication that created a part.
var errUnresolvedRoute = errors.New("a route of the draft names an item that created no part")

// resolveRoute replaces the placeholders of a route path by the id and the
// version number the referenced items created.
func resolveRoute(path string, created, versions map[string]string) (string, error) {
	for _, ph := range []struct {
		prefix string
		values map[string]string
	}{{routeItemPrefix, created}, {routeVersionPrefix, versions}} {
		for {
			start := strings.Index(path, ph.prefix)
			if start < 0 {
				break
			}
			end := strings.Index(path[start:], "}")
			if end < 0 {
				return "", errUnresolvedRoute
			}
			value, ok := ph.values[path[start+len(ph.prefix):start+end]]
			if !ok || value == "" {
				return "", errUnresolvedRoute
			}
			path = path[:start] + value + path[start+end+1:]
		}
	}
	return path, nil
}
