package citype

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Stub ids of the resolver tests.
const (
	fsRoot   = "00000000-0000-4000-8000-0000000000a1"
	fsLeaf   = "00000000-0000-4000-8000-0000000000a2"
	fsTpl1   = "00000000-0000-4000-8000-0000000000b1"
	fsTpl2   = "00000000-0000-4000-8000-0000000000b2"
	fsRoleA  = "00000000-0000-4000-8000-0000000000c1"
	fsRoleB  = "00000000-0000-4000-8000-0000000000c2"
	fsRoleX  = "00000000-0000-4000-8000-0000000000c3"
	fsCI     = "00000000-0000-4000-8000-0000000000d1"
	fsUser   = "00000000-0000-4000-8000-0000000000e1"
	fsOrg    = "00000000-0000-4000-8000-0000000000f1"
	fsOther  = "00000000-0000-4000-8000-0000000000f2"
	fsLayout = "layout-"
)

// stubFormSource is a FormSource over fixed data.
type stubFormSource struct {
	subject   FormSubject
	defs      []fieldmeta.FieldDefinition
	catalog   []fieldmeta.CatalogRule
	roles     []string
	layouts   []FormLayout
	section   map[string]*rules.Rule
	templates map[string]string // template id → its CI type
	fieldsTpl string            // the template FormFields was asked for
}

func (s *stubFormSource) FormCI(_ context.Context, _, ciID string) (*FormSubject, error) {
	if ciID != fsCI {
		return nil, ErrFormCINotFound
	}
	out := s.subject
	return &out, nil
}

func (s *stubFormSource) FormTypeChain(_ context.Context, _, typeID string) ([]string, error) {
	if typeID != s.subject.CITypeID {
		return nil, ErrFormTypeNotFound
	}
	return s.subject.Chain, nil
}

func (s *stubFormSource) FormTemplate(_ context.Context, _, templateID string) (string, error) {
	typ, ok := s.templates[templateID]
	if !ok {
		return "", ErrFormTemplateNotFound
	}
	return typ, nil
}

func (s *stubFormSource) FormFields(_ context.Context, _, _, _, templateID string) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	s.fieldsTpl = templateID
	return s.defs, s.catalog, nil
}

func (s *stubFormSource) FormRoles(context.Context, string, string) ([]string, error) {
	return s.roles, nil
}

func (s *stubFormSource) FormRoleExists(_ context.Context, _, roleID string) (bool, error) {
	return slices.Contains([]string{fsRoleA, fsRoleB, fsRoleX}, roleID), nil
}

func (s *stubFormSource) FormLayouts(context.Context, string, []string, []string, []string) ([]FormLayout, error) {
	return s.layouts, nil
}

func (s *stubFormSource) FormSectionRules(_ context.Context, _ string, keys []string) (map[string]*rules.Rule, error) {
	out := map[string]*rules.Rule{}
	for _, k := range keys {
		if c, ok := s.section[k]; ok {
			out[k] = c
		}
	}
	return out, nil
}

// userCtx is the context of a user principal (apiKey: an API key).
func userCtx(apiKey bool) context.Context {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: fsOrg, UserID: fsUser})
	p := identity.Principal{Subject: fsUser, OrganizationID: fsOrg, Type: identity.PrincipalTypeUser}
	if apiKey {
		p.Type = identity.PrincipalTypeAPIKey
	}
	return identity.WithPrincipal(ctx, p)
}

// typeLayout is an active layout on a type with one section holding field.
func typeLayout(id, typeID, role string, priority int, field string) FormLayout {
	return FormLayout{ID: fsLayout + id, CITypeID: typeID, RoleID: role, Priority: priority, IsActive: true,
		Layout: LayoutDocument{Sections: []LayoutSection{{Key: "s", Title: LocalizedText{"de": "S", "en": "S"},
			Fields: []LayoutField{{Name: field, Width: 6}}}}}}
}

func tplLayout(id, tplID, role string) FormLayout {
	l := typeLayout(id, "", role, 0, "a")
	l.TemplateID = tplID
	return l
}

func baseSource() *stubFormSource {
	return &stubFormSource{
		subject: FormSubject{CITypeID: fsLeaf, Chain: []string{fsRoot, fsLeaf}, Values: map[string]any{},
			Templates: []string{fsTpl1, fsTpl2}},
		defs: []fieldmeta.FieldDefinition{
			{Name: "a", DataType: "text", UIGroup: "G2", SortOrder: 5},
			{Name: "b", DataType: "text", UIGroup: "G1", SortOrder: 2},
			{Name: "c", DataType: "text", UIGroup: "G2", SortOrder: 1},
			{Name: "d", DataType: "text", SortOrder: 3},
		},
		templates: map[string]string{fsTpl1: fsRoot, fsTpl2: fsLeaf},
	}
}

func mustForm(t *testing.T, s *stubFormSource, ctx context.Context, req FormRequest, policy FieldPolicy) *ResolvedForm {
	t.Helper()
	form, err := NewFormResolver(s, policy).ResolveForm(ctx, fsOrg, req)
	if err != nil {
		t.Fatalf("resolve %+v: %v", req, err)
	}
	return form
}

// TestFormLayoutPrecedence covers MET-50 (WP-719): template layouts before
// type layouts, the template with the highest position, the leaf type
// before its parent, a layout with a role of the caller before the one
// without, then priority, then role_id; inactive layouts do not count.
func TestFormLayoutPrecedence(t *testing.T) {
	inactive := typeLayout("inactive", fsLeaf, fsRoleA, -9, "a")
	inactive.IsActive = false
	for _, c := range []struct {
		name    string
		layouts []FormLayout
		roles   []string
		want    string
	}{
		{"no layout", nil, nil, ""},
		{"template before type", []FormLayout{typeLayout("leaf", fsLeaf, "", 0, "a"), tplLayout("t1", fsTpl1, "")}, nil, "t1"},
		{"highest template position", []FormLayout{tplLayout("t1", fsTpl1, ""), tplLayout("t2", fsTpl2, "")}, nil, "t2"},
		{"template of a lower position with a layout", []FormLayout{tplLayout("t1", fsTpl1, ""), tplLayout("t2r", fsTpl2, fsRoleX)},
			[]string{fsRoleA}, "t1"},
		{"leaf before parent", []FormLayout{typeLayout("root", fsRoot, fsRoleA, 0, "a"), typeLayout("leaf", fsLeaf, "", 0, "a")},
			[]string{fsRoleA}, "leaf"},
		{"parent when the leaf has none", []FormLayout{typeLayout("root", fsRoot, "", 0, "a")}, nil, "root"},
		{"role before no role", []FormLayout{typeLayout("none", fsLeaf, "", -5, "a"), typeLayout("ra", fsLeaf, fsRoleA, 9, "a")},
			[]string{fsRoleA}, "ra"},
		{"other role does not count", []FormLayout{typeLayout("none", fsLeaf, "", 0, "a"), typeLayout("rx", fsLeaf, fsRoleX, 0, "a")},
			[]string{fsRoleA}, "none"},
		{"smallest priority", []FormLayout{typeLayout("ra", fsLeaf, fsRoleA, 5, "a"), typeLayout("rb", fsLeaf, fsRoleB, 1, "a")},
			[]string{fsRoleA, fsRoleB}, "rb"},
		{"then role_id", []FormLayout{typeLayout("rb", fsLeaf, fsRoleB, 1, "a"), typeLayout("ra", fsLeaf, fsRoleA, 1, "a")},
			[]string{fsRoleA, fsRoleB}, "ra"},
		{"inactive does not count", []FormLayout{inactive, typeLayout("none", fsLeaf, "", 0, "a")}, []string{fsRoleA}, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := baseSource()
			s.layouts, s.roles = c.layouts, c.roles
			s.subject.Templates = []string{fsTpl1, fsTpl2}
			form := mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, nil)
			got := ""
			if form.LayoutID != "" {
				got = form.LayoutID[len(fsLayout):]
			}
			if got != c.want {
				t.Fatalf("layout %q, want %q", got, c.want)
			}
			if (c.want == "") != (form.Source == FormSourceDefault) {
				t.Fatalf("source %q", form.Source)
			}
		})
	}
}

// TestFormRolesOfTheRequest covers the role choice (WP-719): an API key
// gets the layout without a role; role_id selects among the caller's
// roles; a foreign role outside a preview is ErrFormRoleNotOwn, in a
// preview it is used; an unknown role is ErrFormRoleNotFound.
func TestFormRolesOfTheRequest(t *testing.T) {
	s := baseSource()
	s.roles = []string{fsRoleA, fsRoleB}
	s.layouts = []FormLayout{typeLayout("none", fsLeaf, "", 0, "a"), typeLayout("ra", fsLeaf, fsRoleA, 5, "a"),
		typeLayout("rb", fsLeaf, fsRoleB, 1, "a"), typeLayout("rx", fsLeaf, fsRoleX, 0, "a")}
	for _, c := range []struct {
		name   string
		apiKey bool
		req    FormRequest
		want   string
		err    error
	}{
		{"user", false, FormRequest{CIID: fsCI}, fsLayout + "rb", nil},
		{"api key", true, FormRequest{CIID: fsCI}, fsLayout + "none", nil},
		{"own role", false, FormRequest{CIID: fsCI, RoleID: fsRoleA}, fsLayout + "ra", nil},
		{"foreign role", false, FormRequest{CIID: fsCI, RoleID: fsRoleX}, "", ErrFormRoleNotOwn},
		{"foreign role in a preview", false, FormRequest{CIID: fsCI, RoleID: fsRoleX, Preview: true}, fsLayout + "rx", nil},
		{"unknown role", false, FormRequest{CIID: fsCI, RoleID: fsOther}, "", ErrFormRoleNotFound},
		{"unknown ci", false, FormRequest{CIID: fsOther}, "", ErrFormCINotFound},
		{"unknown type", false, FormRequest{CITypeID: fsOther}, "", ErrFormTypeNotFound},
		{"unknown template", false, FormRequest{CITypeID: fsLeaf, TemplateID: fsOther}, "", ErrFormTemplateNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			form, err := NewFormResolver(s, nil).ResolveForm(userCtx(c.apiKey), fsOrg, c.req)
			if !errors.Is(err, c.err) {
				t.Fatalf("error %v, want %v", err, c.err)
			}
			if err == nil && form.LayoutID != c.want {
				t.Fatalf("layout %q, want %q", form.LayoutID, c.want)
			}
		})
	}
	// template_id on a CI only in a preview (400); a template of a type
	// outside the chain is 422.
	s.templates[fsOther] = "00000000-0000-4000-8000-0000000000a9"
	var fe *api.FieldError
	if _, err := NewFormResolver(s, nil).ResolveForm(userCtx(false), fsOrg, FormRequest{CIID: fsCI, TemplateID: fsTpl1}); !errors.As(err, &fe) ||
		fe.Field != "template_id" || fe.Status != http.StatusBadRequest {
		t.Fatalf("template on a ci without preview: %v", err)
	}
	if _, err := NewFormResolver(s, nil).ResolveForm(userCtx(false), fsOrg, FormRequest{CITypeID: fsLeaf, TemplateID: fsOther}); !errors.As(err, &fe) ||
		fe.Field != "template_id" || fe.Status != http.StatusUnprocessableEntity {
		t.Fatalf("template outside the chain: %v", err)
	}
	// A template of the request replaces the CI's templates in the layout
	// choice and in the fields.
	s.layouts = []FormLayout{tplLayout("t1", fsTpl1, ""), tplLayout("t2", fsTpl2, "")}
	form := mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI, TemplateID: fsTpl1, Preview: true}, nil)
	if form.LayoutID != fsLayout+"t1" || form.TemplateID != fsTpl1 || s.fieldsTpl != fsTpl1 {
		t.Fatalf("preview with template: %+v, fields of %q", form, s.fieldsTpl)
	}
}

func sectionKeys(form *ResolvedForm) []string {
	var keys []string
	for _, s := range form.Sections {
		keys = append(keys, s.Key)
	}
	return keys
}

func sectionFields(form *ResolvedForm, key string) []string {
	var names []string
	for _, s := range form.Sections {
		if s.Key == key {
			for _, f := range s.Fields {
				names = append(names, f.Name)
			}
		}
	}
	return names
}

func formField(form *ResolvedForm, name string) *FormField {
	for i := range form.Sections {
		for j := range form.Sections[i].Fields {
			if form.Sections[i].Fields[j].Name == name {
				return &form.Sections[i].Fields[j]
			}
		}
	}
	return nil
}

// TestFormStandardLayout covers the standard layout (WP-719): sections by
// ui_group ordered by the smallest sort_order of each group, fields by
// sort_order, instance attributes in _instance.
func TestFormStandardLayout(t *testing.T) {
	s := baseSource()
	s.defs = append(s.defs, fieldmeta.FieldDefinition{Name: "inst", DataType: "text", UIGroup: "G1", SortOrder: 0})
	s.subject.Instance = []string{"inst"}
	form := mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, nil)
	if !slices.Equal(sectionKeys(form), []string{"G2", "G1", "_default", FormSectionInstance}) {
		t.Fatalf("sections %v", sectionKeys(form))
	}
	if !slices.Equal(sectionFields(form, "G2"), []string{"c", "a"}) || !slices.Equal(sectionFields(form, "G1"), []string{"b"}) ||
		!slices.Equal(sectionFields(form, "_default"), []string{"d"}) || !slices.Equal(sectionFields(form, FormSectionInstance), []string{"inst"}) {
		t.Fatalf("fields: %+v", form.Sections)
	}
	if form.Sections[0].Title["de"] != "G2" || form.Sections[2].Title["en"] != "General" || formField(form, "a").Width != 12 ||
		form.Source != FormSourceDefault || form.LayoutID != "" || form.CITypeID != fsLeaf {
		t.Fatalf("form %+v", form)
	}
}

// TestFormLayoutSections covers a layout (WP-719): its sections and widths,
// _instance, required fields the layout leaves out or shows only in a
// hidden section in _required, and a section whose visible_when rule does
// not hold.
func TestFormLayoutSections(t *testing.T) {
	s := baseSource()
	s.defs[1].Required = true // b, not in the layout
	s.defs[2].Required = true // c, in the hidden section
	s.defs = append(s.defs, fieldmeta.FieldDefinition{Name: "inst", DataType: "text", SortOrder: 0})
	s.subject.Instance = []string{"inst"}
	s.subject.Values = map[string]any{"a": "x"}
	cond := rules.Rule{Op: rules.OpEq, Field: "a", Value: "y"}
	s.section = map[string]*rules.Rule{"show-hidden": &cond}
	s.layouts = []FormLayout{{ID: "l1", CITypeID: fsLeaf, IsActive: true, Layout: LayoutDocument{Sections: []LayoutSection{
		{Key: "main", Title: LocalizedText{"de": "Haupt", "en": "Main"}, Fields: []LayoutField{{Name: "a", Width: 4}, {Name: "gone", Width: 3}}},
		{Key: "hidden", Title: LocalizedText{"de": "V", "en": "H"}, VisibleWhen: []string{"show-hidden"},
			Fields: []LayoutField{{Name: "c", Width: 6}, {Name: "d", Width: 6}}},
		{Key: "open", Title: LocalizedText{"de": "O", "en": "O"}, VisibleWhen: []string{"unknown-rule"}},
	}}}}
	form := mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, nil)
	if !slices.Equal(sectionKeys(form), []string{"main", "hidden", "open", FormSectionInstance, FormSectionRequired}) {
		t.Fatalf("sections %v", sectionKeys(form))
	}
	if !form.Sections[0].Visible || form.Sections[1].Visible || !form.Sections[2].Visible {
		t.Fatalf("visibility %+v", form.Sections)
	}
	if !slices.Equal(sectionFields(form, "main"), []string{"a"}) || formField(form, "a").Width != 4 ||
		!slices.Equal(sectionFields(form, "hidden"), []string{"d"}) ||
		!slices.Equal(sectionFields(form, FormSectionRequired), []string{"c", "b"}) ||
		!slices.Equal(sectionFields(form, FormSectionInstance), []string{"inst"}) {
		t.Fatalf("fields %+v", form.Sections)
	}
	if form.Sections[4].Title["de"] != "Weitere Pflichtfelder" || form.Sections[4].Title["en"] != "Further required fields" {
		t.Fatalf("title of _required %+v", form.Sections[4].Title)
	}
	// The condition holds: the section is visible and keeps c.
	s.subject.Values = map[string]any{"a": "y"}
	form = mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, nil)
	if !form.Sections[1].Visible || !slices.Equal(sectionFields(form, "hidden"), []string{"c", "d"}) ||
		!slices.Equal(sectionFields(form, FormSectionRequired), []string{"b"}) {
		t.Fatalf("visible section %+v", form.Sections)
	}
}

// TestFormCatalogRuleStates covers the catalog rules in the field state
// (WP-257 semantics): required and readonly while the condition holds,
// visible hides while it does not, a required field stays visible; the
// rules are part of the answer. The type form evaluates against the
// defaults.
func TestFormCatalogRuleStates(t *testing.T) {
	s := baseSource()
	s.defs[0].DefaultValue = "on"
	s.defs[3].DefaultValue = "5"
	when := func(field string, value any) *rules.Rule {
		return &rules.Rule{Op: rules.OpEq, Field: field, Value: value}
	}
	s.catalog = []fieldmeta.CatalogRule{
		{Key: "req-b", AttributeKey: "b", Kind: fieldmeta.CatalogRequired, Condition: when("a", "on")},
		{Key: "ro-c", AttributeKey: "c", Kind: fieldmeta.CatalogReadonly, Condition: when("d", float64(5))},
		{Key: "vis-d", AttributeKey: "d", Kind: fieldmeta.CatalogVisible, Condition: when("a", "off")},
		{Key: "vis-b", AttributeKey: "b", Kind: fieldmeta.CatalogVisible, Condition: when("a", "off")},
	}
	form := mustForm(t, s, userCtx(false), FormRequest{CITypeID: fsLeaf}, nil)
	b, c, d := formField(form, "b").State, formField(form, "c").State, formField(form, "d").State
	if !b.Required || !b.Visible || !c.ReadOnly || c.Required || d.Visible {
		t.Fatalf("states b=%+v c=%+v d=%+v", b, c, d)
	}
	if len(form.Rules) != 4 || form.Rules[0].Key != "req-b" || form.Rules[0].Condition == nil || form.Rules[0].AttributeKey != "b" {
		t.Fatalf("rules %+v", form.Rules)
	}
	// Against other values (a stored CI): no rule applies but the
	// visible ones hide.
	s.subject.Values = map[string]any{"a": "x", "d": 1}
	form = mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, nil)
	b, c = formField(form, "b").State, formField(form, "c").State
	if b.Required || b.Visible || c.ReadOnly {
		t.Fatalf("states without rules b=%+v c=%+v", b, c)
	}
}

// stubPolicy hides and locks fields by name.
type stubPolicy struct{ hidden, locked string }

func (p stubPolicy) Readable(field string) bool { return field != p.hidden }
func (p stubPolicy) Editable(field string) bool { return field != p.locked }

// TestFormFieldPolicy covers FieldPolicy (WP-719): the no-op policy
// leaves every field readable and editable; a field the policy cannot
// read is left out, one it cannot change is read only.
func TestFormFieldPolicy(t *testing.T) {
	s := baseSource()
	form := mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, NoopFieldPolicy{})
	for _, name := range []string{"a", "b", "c", "d"} {
		if f := formField(form, name); f == nil || f.State.ReadOnly {
			t.Fatalf("no-op policy: %s = %+v", name, f)
		}
	}
	form = mustForm(t, s, userCtx(false), FormRequest{CIID: fsCI}, stubPolicy{hidden: "b", locked: "c"})
	if formField(form, "b") != nil || !formField(form, "c").State.ReadOnly || formField(form, "a").State.ReadOnly {
		t.Fatalf("policy: %+v", form.Sections)
	}
	if slices.Contains(sectionKeys(form), "G1") {
		t.Fatalf("a section of unreadable fields only: %v", sectionKeys(form))
	}
}
