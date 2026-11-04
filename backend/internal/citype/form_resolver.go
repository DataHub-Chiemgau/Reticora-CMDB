package citype

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The resolved form (WP-719, MET-50, MET-52): the form of a stored CI
// (GET /api/v1/cis/{id}/form) or of a new CI of a type (GET
// /api/v1/ci-types/{id}/form) with its fields from the effective
// composition (ResolveFields, MET-16), arranged by the layout that applies
// to the caller (form_layout, WP-259) or by the standard layout, every
// field with its composed definition and its state after the attribute
// conditions and the catalog rules (WP-257). The resolution is HTTP
// independent (FormResolver), so WP-792 hangs it into the BFF as ciForm.

// Sources of a resolved form (ResolvedForm.Source).
const (
	FormSourceLayout  = "layout"
	FormSourceDefault = "default"
)

// Sections the resolver adds itself; layouts cannot use these keys
// (reservedSectionKeys).
const (
	FormSectionInstance = "_instance"
	FormSectionRequired = "_required"
	// formSectionDefault holds the fields without ui_group in the standard
	// layout.
	formSectionDefault = "_default"
)

// FormRequest names what to resolve: a stored CI (CIID) or a new CI of a
// type (CITypeID). TemplateID sets the current published version of that
// template in place of the CI's templates (ci_template); on a CI only in a
// preview. RoleID chooses the role: without Preview it must be a role of
// the caller. With Preview the form is resolved as role RoleID with
// template TemplateID (MET-52); the caller checks the right to preview
// (citype:manage).
type FormRequest struct {
	CIID       string
	CITypeID   string
	TemplateID string
	RoleID     string
	Preview    bool
}

// ResolvedForm is the resolved form. TemplateID, RoleID and LayoutID name
// the chosen layout (empty with the standard layout). Rules are the
// catalog rules of the CI, so a client recomputes the states while the
// form is edited (evaluateCondition, WP-715).
type ResolvedForm struct {
	CITypeID   string        `json:"ci_type_id"`
	TemplateID string        `json:"template_id,omitempty"`
	RoleID     string        `json:"role_id,omitempty"`
	LayoutID   string        `json:"layout_id,omitempty"`
	Source     string        `json:"source"`
	Sections   []FormSection `json:"sections"`
	Rules      []FormRule    `json:"rules"`
}

// FormSection is a section of the resolved form; Visible is false while a
// visible_when rule of the layout section does not hold.
type FormSection struct {
	Key     string        `json:"key"`
	Title   LocalizedText `json:"title"`
	Visible bool          `json:"visible"`
	Fields  []FormField   `json:"fields"`
}

// FormField is a field of the resolved form with its width (1 to 12), its
// composed definition and its current state.
type FormField struct {
	Name       string                    `json:"name"`
	Width      int                       `json:"width"`
	Definition fieldmeta.FieldDefinition `json:"definition"`
	State      fieldmeta.FieldState      `json:"state"`
}

// FormRule is a catalog rule of the CI (fieldmeta.CatalogRule) as JSON:
// the condition and the value rule in the form of api/rules.schema.json.
type FormRule struct {
	Key          string            `json:"key"`
	AttributeKey string            `json:"attribute_key"`
	Kind         string            `json:"kind"`
	Condition    *rules.Rule       `json:"condition,omitempty"`
	AST          *rules.Rule       `json:"ast,omitempty"`
	Message      map[string]string `json:"message,omitempty"`
}

// FieldPolicy decides per field whether the caller may read and change
// it. The field rights come with WP-264/WP-265; until then
// NoopFieldPolicy allows everything.
type FieldPolicy interface {
	Readable(field string) bool
	Editable(field string) bool
}

// NoopFieldPolicy allows reading and changing every field.
type NoopFieldPolicy struct{}

// Readable implements FieldPolicy.
func (NoopFieldPolicy) Readable(string) bool { return true }

// Editable implements FieldPolicy.
func (NoopFieldPolicy) Editable(string) bool { return true }

// Errors of the resolution.
var (
	ErrFormCINotFound       = errors.New("ci not found")
	ErrFormTypeNotFound     = errors.New("ci type not found")
	ErrFormTemplateNotFound = errors.New("template not found")
	ErrFormRoleNotFound     = errors.New("role not found")
	// ErrFormRoleNotOwn: role_id outside a preview names no role of the
	// caller; choosing another role needs citype:manage (403).
	ErrFormRoleNotOwn = errors.New("role_id is not a role of the caller")
	// ErrFormDraftNotFound: the draft of a preview does not exist or
	// belongs to another organization (404).
	ErrFormDraftNotFound = errors.New("metamodel draft not found")
	// ErrFormDraftFinal: the draft of a preview is published or discarded
	// (409).
	ErrFormDraftFinal = errors.New("published and discarded drafts have no preview")
	// ErrFormDraftsUnavailable: no draft source is wired (no database,
	// 503).
	ErrFormDraftsUnavailable = errors.New("the preview of a draft needs the database")
)

// DraftForms lays the items of a metamodel draft over the published
// metamodel for the preview of a form (E-113, WP-720). The package
// metamodel implements it (citype does not import metamodel); the server
// hands it to the form route in the request context (WithDraftForms).
type DraftForms interface {
	// DraftFormSource returns a FormSource that reads published with the
	// items of draft draftID laid over it: the fields, the catalog rules
	// and the layouts; ErrFormDraftNotFound for an unknown draft or one of
	// another organization, ErrFormDraftFinal for a published or
	// discarded one.
	DraftFormSource(ctx context.Context, orgID, draftID string, published FormSource) (FormSource, error)
}

type draftFormsKey struct{}

// WithDraftForms returns ctx carrying the draft source of the form
// preview.
func WithDraftForms(ctx context.Context, d DraftForms) context.Context {
	return context.WithValue(ctx, draftFormsKey{}, d)
}

// DraftFormsFromContext returns the draft source of the form preview, if
// the request carries one.
func DraftFormsFromContext(ctx context.Context) (DraftForms, bool) {
	d, ok := ctx.Value(draftFormsKey{}).(DraftForms)
	return d, ok && d != nil
}

// FormSubject is the CI or the type a form is resolved for: the type, its
// chain root → type (WP-248), the stored attribute values (instance
// attributes under their names), the templates of the CI in position order
// (ci_template, WP-710) and the names of its instance attributes (MET-14).
type FormSubject struct {
	CITypeID  string
	Chain     []string
	Values    map[string]any
	Templates []string
	Instance  []string
}

// FormSource is what the resolver reads; PGRepository implements it.
type FormSource interface {
	// FormCI reads a CI the caller sees (ErrFormCINotFound).
	FormCI(ctx context.Context, orgID, ciID string) (*FormSubject, error)
	// FormTypeChain returns the chain root → type of a visible type
	// (ErrFormTypeNotFound).
	FormTypeChain(ctx context.Context, orgID, typeID string) ([]string, error)
	// FormTemplate returns the CI type of a visible template
	// (ErrFormTemplateNotFound).
	FormTemplate(ctx context.Context, orgID, templateID string) (string, error)
	// FormFields returns the effective field definitions and the catalog
	// rules of a CI (ciID) or a new CI of typeID; templateID, when set,
	// replaces the CI's templates with the current version of that
	// template.
	FormFields(ctx context.Context, orgID, typeID, ciID, templateID string) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error)
	// FormRoles returns the roles and custom roles of a user
	// (role_assignment ∪ user_custom_role, whatever the scope).
	FormRoles(ctx context.Context, orgID, userID string) ([]string, error)
	// FormRoleExists reports whether roleID is a role or a custom role of
	// the organization.
	FormRoleExists(ctx context.Context, orgID, roleID string) (bool, error)
	// FormLayouts returns the active layouts of the types and templates
	// without a role or with one of roleIDs, in one query.
	FormLayouts(ctx context.Context, orgID string, typeIDs, templateIDs, roleIDs []string) ([]FormLayout, error)
	// FormSectionRules returns the conditions of the active catalog rules
	// with these keys; a key without an active rule or a rule without a
	// condition is missing.
	FormSectionRules(ctx context.Context, orgID string, keys []string) (map[string]*rules.Rule, error)
}

// FormResolver resolves forms (WP-719); WP-792 uses it as the source of
// the BFF field ciForm.
type FormResolver struct {
	source FormSource
	policy FieldPolicy
	// drafts and draftID lay the items of a metamodel draft over the
	// published metamodel (WithDraft).
	drafts  DraftForms
	draftID string
}

// NewFormResolver returns a resolver over source; policy nil is
// NoopFieldPolicy.
func NewFormResolver(source FormSource, policy FieldPolicy) *FormResolver {
	if policy == nil {
		policy = NoopFieldPolicy{}
	}
	return &FormResolver{source: source, policy: policy}
}

// formTemplateError is a refusal of template_id.
// validation: the field and a fixed message.
func formTemplateError(status int, message string) *api.FieldError {
	return &api.FieldError{Field: "template_id", Message: message, Status: status}
}

// WithDraft resolves the forms over the published metamodel with the
// items of metamodel draft draftID laid over it (E-113, WP-720): the
// preview of a draft, for a new CI of a type only; the caller checks
// citype:manage. Without a draft source (d nil) the resolution is
// ErrFormDraftsUnavailable.
func (f *FormResolver) WithDraft(d DraftForms, draftID string) *FormResolver {
	f.drafts, f.draftID = d, draftID
	return f
}

// ResolveForm resolves the form of req for the caller of ctx in orgID.
// The caller's roles are the roles and custom roles of the user of the
// tenant context; an API key has none (E-110: roles mapped from IdP
// groups count only from WP-725 on). With a draft (WithDraft) the form is
// resolved over the published metamodel with the items of the draft laid
// over it.
func (f *FormResolver) ResolveForm(ctx context.Context, orgID string, req FormRequest) (*ResolvedForm, error) {
	if f.draftID != "" {
		if req.CIID != "" || req.CITypeID == "" {
			return nil, &api.FieldError{Field: "draft", Message: "a draft preview resolves the form of a new CI of a type"}
		}
		if f.drafts == nil {
			return nil, ErrFormDraftsUnavailable
		}
		source, err := f.drafts.DraftFormSource(ctx, orgID, f.draftID, f.source)
		if err != nil {
			return nil, err
		}
		return NewFormResolver(source, f.policy).ResolveForm(ctx, orgID, req)
	}
	subject, err := f.subject(ctx, orgID, &req)
	if err != nil {
		return nil, err
	}
	templates := subject.Templates
	if req.TemplateID != "" {
		tplType, terr := f.source.FormTemplate(ctx, orgID, req.TemplateID)
		if terr != nil {
			return nil, terr
		}
		if !slices.ContainsFunc(subject.Chain, func(id string) bool { return strings.EqualFold(id, tplType) }) {
			return nil, formTemplateError(http.StatusUnprocessableEntity,
				"the template belongs to a CI type outside the type chain")
		}
		templates = []string{req.TemplateID}
	}
	roles, err := f.roles(ctx, orgID, &req)
	if err != nil {
		return nil, err
	}
	defs, catalog, err := f.source.FormFields(ctx, orgID, subject.CITypeID, req.CIID, req.TemplateID)
	if err != nil {
		var fe *api.FieldError
		if req.TemplateID != "" && errors.As(err, &fe) && strings.HasPrefix(fe.Field, "templates[") {
			return nil, formTemplateError(http.StatusUnprocessableEntity, fe.Message)
		}
		return nil, err
	}
	values := subject.Values
	if req.CIID == "" {
		values = defaultValues(defs)
	}
	candidates, err := f.source.FormLayouts(ctx, orgID, subject.Chain, templates, roles)
	if err != nil {
		return nil, err
	}
	layout := selectLayout(candidates, subject.Chain, templates, roles)
	var sectionRules map[string]*rules.Rule
	if layout != nil {
		if sectionRules, err = f.sectionRules(ctx, orgID, layout, catalog); err != nil {
			return nil, err
		}
	}
	return buildForm(&formInput{
		typeID: subject.CITypeID, defs: defs, catalog: catalog, values: values, instance: subject.Instance,
		layout: layout, sectionRules: sectionRules, policy: f.policy,
	}), nil
}

// subject reads the CI or the type of req.
func (f *FormResolver) subject(ctx context.Context, orgID string, req *FormRequest) (*FormSubject, error) {
	if req.CIID != "" {
		if req.TemplateID != "" && !req.Preview {
			return nil, formTemplateError(http.StatusBadRequest, "template_id is allowed on a CI only with preview=true")
		}
		if !api.IsUUID(req.CIID) {
			return nil, ErrFormCINotFound
		}
		return f.source.FormCI(ctx, orgID, req.CIID)
	}
	if !api.IsUUID(req.CITypeID) {
		return nil, ErrFormTypeNotFound
	}
	chain, err := f.source.FormTypeChain(ctx, orgID, req.CITypeID)
	if err != nil {
		return nil, err
	}
	return &FormSubject{CITypeID: req.CITypeID, Chain: chain}, nil
}

// roles returns the roles the layout is chosen for: the requested role
// (outside a preview only one of the caller's own) or all of the caller's.
func (f *FormResolver) roles(ctx context.Context, orgID string, req *FormRequest) ([]string, error) {
	if req.RoleID != "" {
		if !api.IsUUID(req.RoleID) {
			return nil, ErrFormRoleNotFound
		}
		found, err := f.source.FormRoleExists(ctx, orgID, req.RoleID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrFormRoleNotFound
		}
	}
	var own []string
	if p, ok := identity.PrincipalFromContext(ctx); !ok || p.Type != identity.PrincipalTypeAPIKey {
		if user := tenant.FromContext(ctx).UserID; user != "" {
			var err error
			if own, err = f.source.FormRoles(ctx, orgID, user); err != nil {
				return nil, err
			}
		}
	}
	if req.RoleID == "" {
		return own, nil
	}
	if !req.Preview && !slices.ContainsFunc(own, func(id string) bool { return strings.EqualFold(id, req.RoleID) }) {
		return nil, ErrFormRoleNotOwn
	}
	return []string{req.RoleID}, nil
}

// sectionRules returns the conditions of the visible_when keys of the
// layout: the catalog rule of the CI with the key, otherwise the active
// rule the organization sees.
func (f *FormResolver) sectionRules(ctx context.Context, orgID string, layout *FormLayout,
	catalog []fieldmeta.CatalogRule) (map[string]*rules.Rule, error) {
	out := map[string]*rules.Rule{}
	known := map[string]bool{}
	for i := range catalog {
		if !known[catalog[i].Key] {
			known[catalog[i].Key] = true
			out[catalog[i].Key] = catalog[i].Condition
		}
	}
	var missing []string
	for _, s := range layout.Layout.Sections {
		for _, key := range s.VisibleWhen {
			if !known[key] && !slices.Contains(missing, key) {
				missing = append(missing, key)
			}
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	found, err := f.source.FormSectionRules(ctx, orgID, missing)
	if err != nil {
		return nil, err
	}
	for key, cond := range found {
		out[key] = cond
	}
	return out, nil
}

// defaultValues are the values of a new CI: the default of every field
// (default_value as JSON, otherwise as text).
func defaultValues(defs []fieldmeta.FieldDefinition) map[string]any {
	values := map[string]any{}
	for i := range defs {
		raw := defs[i].DefaultValue
		if raw == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			v = raw
		}
		values[defs[i].Name] = v
	}
	return values
}

// selectLayout chooses the layout (nil: the standard layout): templates
// before types; of several templates the one with the highest position
// that has a layout; types from the leaf to the root. Per target a layout
// with a role of roles goes before the one without a role; of several
// roles the smallest priority, then the smallest role_id. Inactive layouts
// and layouts of other roles do not count.
func selectLayout(candidates []FormLayout, chain, templates, roles []string) *FormLayout {
	allowed := func(l *FormLayout) bool {
		return l.IsActive && (l.RoleID == "" || slices.ContainsFunc(roles, func(id string) bool { return strings.EqualFold(id, l.RoleID) }))
	}
	best := func(match func(l *FormLayout) bool) *FormLayout {
		var out *FormLayout
		for i := range candidates {
			l := &candidates[i]
			if !match(l) || !allowed(l) {
				continue
			}
			if out == nil || layoutBefore(l, out) {
				out = l
			}
		}
		return out
	}
	for i := len(templates) - 1; i >= 0; i-- {
		id := templates[i]
		if l := best(func(l *FormLayout) bool { return l.TemplateID != "" && strings.EqualFold(l.TemplateID, id) }); l != nil {
			return l
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		id := chain[i]
		if l := best(func(l *FormLayout) bool { return l.CITypeID != "" && strings.EqualFold(l.CITypeID, id) }); l != nil {
			return l
		}
	}
	return nil
}

// layoutBefore orders the layouts of one target: with a role before
// without, then priority, then role_id.
func layoutBefore(a, b *FormLayout) bool {
	if (a.RoleID != "") != (b.RoleID != "") {
		return a.RoleID != ""
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return strings.ToLower(a.RoleID) < strings.ToLower(b.RoleID)
}

// formInput is what buildForm arranges.
type formInput struct {
	typeID       string
	defs         []fieldmeta.FieldDefinition
	catalog      []fieldmeta.CatalogRule
	values       map[string]any
	instance     []string
	layout       *FormLayout
	sectionRules map[string]*rules.Rule
	policy       FieldPolicy
}

// fieldStates evaluates every field against values: its attribute
// conditions (fieldmeta.Evaluate) and the catalog rules that apply
// (WP-257: required and readonly while their condition holds, visible
// hides the field while it does not). A field required in the current
// state stays visible. A field the policy does not let change is read
// only.
func fieldStates(in *formInput) map[string]fieldmeta.FieldState {
	type catalogState struct{ required, readOnly, hidden bool }
	byField := map[string]*catalogState{}
	for i := range in.catalog {
		rule := &in.catalog[i]
		st := byField[rule.AttributeKey]
		if st == nil {
			st = &catalogState{}
			byField[rule.AttributeKey] = st
		}
		applies := rule.Applies(in.values)
		switch rule.Kind {
		case fieldmeta.CatalogRequired:
			st.required = st.required || applies
		case fieldmeta.CatalogReadonly:
			st.readOnly = st.readOnly || applies
		case fieldmeta.CatalogVisible:
			st.hidden = st.hidden || !applies
		}
	}
	out := make(map[string]fieldmeta.FieldState, len(in.defs))
	for i := range in.defs {
		def := &in.defs[i]
		state := fieldmeta.Evaluate(def, in.values)
		hidden := !state.Visible
		if st := byField[def.Name]; st != nil {
			state.Required = state.Required || st.required
			state.ReadOnly = state.ReadOnly || st.readOnly
			hidden = hidden || st.hidden
		}
		state.Visible = !hidden || state.Required
		if !in.policy.Editable(def.Name) {
			state.ReadOnly = true
		}
		out[def.Name] = state
	}
	return out
}

// buildForm arranges the fields by the layout (or the standard layout),
// with the instance attributes in _instance and the required fields the
// layout leaves out or shows only in a hidden section in _required.
func buildForm(in *formInput) *ResolvedForm {
	states := fieldStates(in)
	defs := map[string]*fieldmeta.FieldDefinition{}
	var ordered []*fieldmeta.FieldDefinition
	for i := range in.defs {
		if in.policy.Readable(in.defs[i].Name) {
			defs[in.defs[i].Name] = &in.defs[i]
			ordered = append(ordered, &in.defs[i])
		}
	}
	slices.SortStableFunc(ordered, func(a, b *fieldmeta.FieldDefinition) int { return cmp.Compare(a.SortOrder, b.SortOrder) })
	field := func(name string, width int) FormField {
		return FormField{Name: name, Width: width, Definition: *defs[name], State: states[name]}
	}
	instance := map[string]bool{}
	for _, name := range in.instance {
		instance[name] = true
	}

	form := &ResolvedForm{CITypeID: in.typeID, Source: FormSourceDefault, Sections: []FormSection{}, Rules: formRules(in.catalog)}
	placed := map[string]bool{}
	if in.layout != nil {
		form.Source, form.LayoutID = FormSourceLayout, in.layout.ID
		form.TemplateID, form.RoleID = in.layout.TemplateID, in.layout.RoleID
		for _, s := range in.layout.Layout.Sections {
			section := FormSection{Key: s.Key, Title: s.Title, Visible: sectionVisible(s.VisibleWhen, in.sectionRules, in.values),
				Fields: []FormField{}}
			for _, lf := range s.Fields {
				if defs[lf.Name] == nil || placed[lf.Name] {
					continue
				}
				if !section.Visible && states[lf.Name].Required {
					continue // moves to _required
				}
				placed[lf.Name] = true
				section.Fields = append(section.Fields, field(lf.Name, lf.Width))
			}
			form.Sections = append(form.Sections, section)
		}
	} else {
		groups := map[string]int{}
		for _, def := range ordered {
			if instance[def.Name] {
				continue
			}
			key := def.UIGroup
			if key == "" {
				key = formSectionDefault
			}
			pos, seen := groups[key]
			if !seen {
				title := LocalizedText{"de": def.UIGroup, "en": def.UIGroup}
				if def.UIGroup == "" {
					title = LocalizedText{"de": "Allgemein", "en": "General"}
				}
				pos = len(form.Sections)
				groups[key] = pos
				form.Sections = append(form.Sections, FormSection{Key: key, Title: title, Visible: true, Fields: []FormField{}})
			}
			placed[def.Name] = true
			form.Sections[pos].Fields = append(form.Sections[pos].Fields, field(def.Name, 12))
		}
	}
	extra := func(key string, title LocalizedText, take func(def *fieldmeta.FieldDefinition) bool) {
		section := FormSection{Key: key, Title: title, Visible: true, Fields: []FormField{}}
		for _, def := range ordered {
			if !placed[def.Name] && take(def) {
				placed[def.Name] = true
				section.Fields = append(section.Fields, field(def.Name, 12))
			}
		}
		if len(section.Fields) > 0 {
			form.Sections = append(form.Sections, section)
		}
	}
	extra(FormSectionInstance, LocalizedText{"de": "Instanzattribute", "en": "Instance attributes"},
		func(def *fieldmeta.FieldDefinition) bool { return instance[def.Name] })
	extra(FormSectionRequired, LocalizedText{"de": "Weitere Pflichtfelder", "en": "Further required fields"},
		func(def *fieldmeta.FieldDefinition) bool { return states[def.Name].Required })
	return form
}

// sectionVisible reports whether every visible_when rule of a section
// holds for values; a key without a condition holds.
func sectionVisible(keys []string, conditions map[string]*rules.Rule, values map[string]any) bool {
	for _, key := range keys {
		if cond := conditions[key]; cond != nil && !rules.EvaluateCondition(cond, values) {
			return false
		}
	}
	return true
}

// formRules is the JSON form of the catalog rules.
func formRules(catalog []fieldmeta.CatalogRule) []FormRule {
	out := make([]FormRule, 0, len(catalog))
	for i := range catalog {
		c := &catalog[i]
		out = append(out, FormRule{Key: c.Key, AttributeKey: c.AttributeKey, Kind: c.Kind, Condition: c.Condition,
			AST: c.AST, Message: c.Message})
	}
	return out
}

// FormCI implements FormSource: the CI under the caller's row level
// security (client and site scope), its type chain, its stored attribute
// values with the instance values under their names, its templates in
// position order and the names of its instance attributes, in one
// statement.
func (r *PGRepository) FormCI(ctx context.Context, orgID, ciID string) (*FormSubject, error) {
	if !api.IsUUID(ciID) {
		return nil, ErrFormCINotFound
	}
	s := &FormSubject{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT c.ci_type_id::text, c.attributes,
				ARRAY(SELECT a.id::text FROM ci_type_ancestors(c.ci_type_id) a ORDER BY a.level),
				ARRAY(SELECT ct.template_id::text FROM ci_template ct WHERE ct.ci_id = c.id ORDER BY ct.position),
				ARRAY(SELECT d.name FROM ci_instance_field_definition d WHERE d.ci_id = c.id ORDER BY d.name)
			FROM ci c WHERE c.id = $1`, ciID).Scan(&s.CITypeID, &raw, &s.Chain, &s.Templates, &s.Instance)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrFormCINotFound
		}
		if err != nil {
			return fmt.Errorf("read ci of form: %w", err)
		}
		stored := map[string]any{}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &stored); err != nil {
				return fmt.Errorf("decode ci attributes: %w", err)
			}
		}
		s.Values = formValues(stored)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// formValues is the value view of stored attributes: the reserved
// namespaces are left out, the instance values appear under their names.
func formValues(stored map[string]any) map[string]any {
	out := make(map[string]any, len(stored))
	for k, v := range stored {
		if !fieldmeta.ReservedNames[k] {
			out[k] = v
		}
	}
	if inst, ok := stored[FormSectionInstance].(map[string]any); ok {
		for k, v := range inst {
			out[k] = v
		}
	}
	return out
}

// FormTypeChain implements FormSource.
func (r *PGRepository) FormTypeChain(ctx context.Context, orgID, typeID string) ([]string, error) {
	if !api.IsUUID(typeID) {
		return nil, ErrFormTypeNotFound
	}
	var chain []string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ARRAY(SELECT a.id::text FROM ci_type_ancestors($1) a ORDER BY a.level)`,
			typeID).Scan(&chain)
	})
	if err != nil {
		return nil, fmt.Errorf("read type chain of form: %w", err)
	}
	if len(chain) == 0 {
		return nil, ErrFormTypeNotFound
	}
	return chain, nil
}

// FormTemplate implements FormSource.
func (r *PGRepository) FormTemplate(ctx context.Context, orgID, templateID string) (string, error) {
	var typeID string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tpl, err := getTemplateTx(ctx, tx, templateID, false)
		if errors.Is(err, ErrTemplateNotFound) {
			return ErrFormTemplateNotFound
		}
		if err != nil {
			return err
		}
		typeID = tpl.CITypeID
		return nil
	})
	return typeID, err
}

// FormFields implements FormSource with ResolveFieldsAndRules; a template
// is set with its current published version (template.current_version),
// checked like an assignment (ResolveTemplateRefsTx).
func (r *PGRepository) FormFields(ctx context.Context, orgID, typeID, ciID, templateID string) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	var source templateSource
	if templateID != "" {
		source = r.templateList(orgID, typeID, []CITemplate{{TemplateID: templateID}})
	}
	return ResolveFieldsAndRules(ctx, r, orgID, typeID, ciID, source)
}
