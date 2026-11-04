package citype

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Tests of the field permission sections of the metamodel export v2
// (MET-54, WP-726): field_grants and field_sensitivity with keys instead of
// ids, only for a reader holding field_permission:manage for the whole
// organization, otherwise named in omitted_sections; v1 unchanged.

// snapshotMemory is the memory repository as an ExportV2Source with empty
// stores of the phase-2 parts: the snapshot is the call itself, marked in
// its context.
type snapshotMemory struct {
	*MemoryRepository
	AttributeDefinitionStore
	AttributeSetStore
	TemplateStore
	RuleStore
	LayoutStore
}

type snapshotKey struct{}

func (s *snapshotMemory) ListAttributeDefinitions(context.Context, string, AttributeFilter) ([]AttributeDefinition, error) {
	return nil, nil
}

func (s *snapshotMemory) ListAttributeSetPage(context.Context, string, api.PaginationParams) ([]AttributeSet, int, error) {
	return nil, 0, nil
}

func (s *snapshotMemory) GetTypeAttributeSets(context.Context, string, string) (*TypeAttributeSets, error) {
	return &TypeAttributeSets{}, nil
}

func (s *snapshotMemory) ListTemplatePage(context.Context, string, TemplateFilter, api.PaginationParams) ([]Template, int, error) {
	return nil, 0, nil
}

func (s *snapshotMemory) ListRulePage(context.Context, string, *RuleFilter, api.PaginationParams) ([]MetamodelRule, int, error) {
	return nil, 0, nil
}

func (s *snapshotMemory) ListLayoutPage(context.Context, string, *LayoutFilter, api.PaginationParams) ([]FormLayout, int, error) {
	return nil, 0, nil
}

func (s *snapshotMemory) RunInSnapshot(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	return fn(context.WithValue(ctx, snapshotKey{}, true))
}

var _ ExportV2Source = (*snapshotMemory)(nil)

type noModels struct{}

func (noModels) ExportLifecycleModels(context.Context, string) ([]ExportLifecycleModelV2, error) {
	return []ExportLifecycleModelV2{}, nil
}

type noRoles struct{}

func (noRoles) ExportRoles(context.Context, string) ([]ExportRole, error) { return []ExportRole{}, nil }

// fakeGrants is a GrantExporter returning its rows in an unsorted order and
// recording whether it was called inside the snapshot.
type fakeGrants struct {
	calls, inSnapshot int
	err               error
}

func (f *fakeGrants) ExportFieldPermissions(ctx context.Context, _ string) (*FieldPermissionsExportV2, error) {
	f.calls++
	if ctx.Value(snapshotKey{}) == true {
		f.inSnapshot++
	}
	if f.err != nil {
		return nil, f.err
	}
	return &FieldPermissionsExportV2{
		FieldGrants: []ExportFieldGrantV2{
			{Role: "ops", RoleKind: "custom_role", ObjectKind: "ci", Action: "ci:delete", Level: "none"},
			{Role: "engineer", RoleKind: "role", ObjectKind: "ci", CIType: "server", FieldKey: "core.serial_number", Level: "read"},
			{Role: "engineer", RoleKind: "role", ObjectKind: "asset", FieldKey: "core.purchase_cost", Level: "none"},
			{Role: "auditor", RoleKind: "role", ObjectKind: "ci", RelationshipType: "depends_on", Level: "read"},
		},
		Sensitivity: []ExportSensitiveFieldV2{
			{ObjectKind: "ci", CIType: "server", FieldKey: "attributes.secret"},
			{ObjectKind: "asset", FieldKey: "core.purchase_cost"},
		},
	}, nil
}

const grantsOrg = "72600000-0000-4000-8000-000000000001"

// exportAs calls GET /api/v1/ci-types/export?format_version=<v> as a
// principal holding perms in scope (nil principal: none).
func exportAs(t *testing.T, h *Handler, version string, p *identity.Principal) (int, []byte) {
	t.Helper()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: grantsOrg, UserID: "u"})
	if p != nil {
		ctx = identity.WithPrincipal(ctx, *p)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ci-types/export?format_version="+version, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func orgWideScope() *identity.Scope {
	all := identity.ScopeSet{All: true}
	return &identity.Scope{Clients: all, Sites: all, Teams: all}
}

func principalWith(scope *identity.Scope, perms ...identity.Permission) *identity.Principal {
	return &identity.Principal{Subject: "u", OrganizationID: grantsOrg, Type: identity.PrincipalTypeUser, Scope: scope,
		Permissions: perms}
}

// TestExportV2FieldPermissionSections: with field_permission:manage for the
// whole organization the export holds both sections, read in the snapshot,
// in a stable order with keys; without it, restricted to a client, as a
// client-scoped API key or without principal the sections are absent and
// omitted_sections names them, and the exporter is not called.
func TestExportV2FieldPermissionSections(t *testing.T) {
	grants := &fakeGrants{}
	h := NewHandler(&snapshotMemory{MemoryRepository: NewMemoryRepository()}).
		WithLifecycleModels(noModels{}).WithRoles(noRoles{}).WithGrantExporter(grants)

	manager := principalWith(orgWideScope(), identity.PermCIRead, identity.PermFieldPermissionManage)
	code, body := exportAs(t, h, "2", manager)
	if code != http.StatusOK {
		t.Fatalf("export v2 = %d %s", code, body)
	}
	var doc MetamodelExportV2
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if grants.calls != 1 || grants.inSnapshot != 1 {
		t.Errorf("exporter called %d times, %d in the snapshot; want once in it", grants.calls, grants.inSnapshot)
	}
	if len(doc.OmittedSections) != 0 || doc.FieldGrants == nil || doc.FieldSensitivity == nil {
		t.Fatalf("manager: omitted %v, sections %v %v", doc.OmittedSections, doc.FieldGrants, doc.FieldSensitivity)
	}
	var order []string
	for _, g := range *doc.FieldGrants {
		order = append(order, g.RoleKind+"/"+g.Role+"/"+g.ObjectKind+"/"+g.CIType+"/"+g.FieldKey+g.RelationshipType+g.Action+"="+g.Level)
	}
	want := []string{
		"custom_role/ops/ci//ci:delete=none",
		"role/auditor/ci//depends_on=read",
		"role/engineer/asset//core.purchase_cost=none",
		"role/engineer/ci/server/core.serial_number=read",
	}
	if !slices.Equal(order, want) {
		t.Errorf("field_grants order\n got %v\nwant %v", order, want)
	}
	if s := *doc.FieldSensitivity; len(s) != 2 || s[0].ObjectKind != "asset" || s[1].CIType != "server" || s[1].FieldKey != "attributes.secret" {
		t.Errorf("field_sensitivity = %+v", s)
	}
	// Keys only: the sections carry no id of a row, role or type.
	for _, forbidden := range []string{`"id"`, `"role_id"`, `"custom_role_id"`, `"ci_type_id"`, `"relationship_type_id"`, `"organization_id":"` + grantsOrg + `","omitted`} {
		section := body[bytes.Index(body, []byte(`"field_grants"`)):]
		if bytes.Contains(section, []byte(forbidden)) {
			t.Errorf("the field permission sections carry %s", forbidden)
		}
	}
	// Stable: a second export of the same state is byte-identical except
	// for exported_at.
	_, again := exportAs(t, h, "2", manager)
	var second MetamodelExportV2
	if err := json.Unmarshal(again, &second); err != nil {
		t.Fatal(err)
	}
	second.ExportedAt, doc.ExportedAt = time.Time{}, time.Time{}
	first, _ := json.Marshal(doc)
	next, _ := json.Marshal(second)
	if !bytes.Equal(first, next) {
		t.Error("a second export v2 differs")
	}

	clientScope := orgWideScope()
	clientScope.Clients = identity.ScopeSet{IDs: []string{"72600000-0000-4000-8000-0000000000c1"}}
	teamScoped := orgWideScope()
	teamScoped.Teams = identity.ScopeSet{IDs: []string{"72600000-0000-4000-8000-0000000000e1"}}
	narrowed := principalWith(orgWideScope(), identity.PermCIRead, identity.PermFieldPermissionManage)
	narrowed.PermissionScopes = map[identity.Permission]identity.Scope{identity.PermFieldPermissionManage: *clientScope}
	apiKey := &identity.Principal{Subject: "k", OrganizationID: grantsOrg, Type: identity.PrincipalTypeAPIKey,
		ClientScope: "72600000-0000-4000-8000-0000000000c1",
		Permissions: []identity.Permission{identity.PermCIRead, identity.PermFieldPermissionManage}}
	for name, p := range map[string]*identity.Principal{
		"engineer (ci:read only)":           principalWith(orgWideScope(), identity.PermCIRead, identity.PermCIWrite),
		"manager restricted to a client":    principalWith(clientScope, identity.PermCIRead, identity.PermFieldPermissionManage),
		"manager restricted to a team":      principalWith(teamScoped, identity.PermCIRead, identity.PermFieldPermissionManage),
		"permission narrowed to a client":   narrowed,
		"API key restricted to a client":    apiKey,
		"no principal":                      nil,
		"principal without a resolved role": {Subject: "x", OrganizationID: grantsOrg},
	} {
		calls := grants.calls
		code, body = exportAs(t, h, "2", p)
		var hidden MetamodelExportV2
		if err := json.Unmarshal(body, &hidden); code != http.StatusOK || err != nil {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if hidden.FieldGrants != nil || hidden.FieldSensitivity != nil || bytes.Contains(body, []byte(`"field_grants":`)) ||
			bytes.Contains(body, []byte(`"field_sensitivity":`)) {
			t.Errorf("%s sees the field permission sections: %s", name, body)
		}
		if !slices.Equal(hidden.OmittedSections, []string{SectionFieldGrants, SectionFieldSensitivity}) {
			t.Errorf("%s: omitted_sections = %v", name, hidden.OmittedSections)
		}
		if grants.calls != calls {
			t.Errorf("%s: the exporter was called", name)
		}
	}

	// An API key of the whole organization reads them.
	orgKey := &identity.Principal{Subject: "k", OrganizationID: grantsOrg, Type: identity.PrincipalTypeAPIKey,
		Permissions: []identity.Permission{identity.PermCIRead, identity.PermFieldPermissionManage}}
	if _, body = exportAs(t, h, "2", orgKey); !bytes.Contains(body, []byte(`"field_grants":[{`)) {
		t.Errorf("org-wide API key: %s", body)
	}

	// A failing exporter fails the export (no partial document).
	grants.err = errors.New("boom")
	if code, _ = exportAs(t, h, "2", manager); code != http.StatusInternalServerError {
		t.Errorf("failing exporter = %d, want 500", code)
	}
}

// TestExportV2WithoutGrantExporter: without an exporter both sections are
// empty lists, for every reader, and nothing is omitted (WP-262 behaviour).
func TestExportV2WithoutGrantExporter(t *testing.T) {
	h := NewHandler(&snapshotMemory{MemoryRepository: NewMemoryRepository()}).WithLifecycleModels(noModels{}).WithRoles(noRoles{})
	for _, p := range []*identity.Principal{nil, principalWith(orgWideScope(), identity.PermCIRead, identity.PermFieldPermissionManage)} {
		code, body := exportAs(t, h, "2", p)
		if code != http.StatusOK || !bytes.Contains(body, []byte(`"omitted_sections":[]`)) ||
			!bytes.Contains(body, []byte(`"field_grants":[]`)) || !bytes.Contains(body, []byte(`"field_sensitivity":[]`)) {
			t.Errorf("without exporter: %d %s", code, body)
		}
	}
	doc, err := BuildExportV2(context.Background(), &snapshotMemory{MemoryRepository: NewMemoryRepository()}, noModels{}, noRoles{},
		grantsOrg, time.Now())
	if err != nil || doc.FieldGrants == nil || len(*doc.FieldGrants) != 0 || len(doc.OmittedSections) != 0 {
		t.Errorf("BuildExportV2: %+v %v", doc, err)
	}
}

// TestExportV1HasNoFieldPermissions: format 1 (API-01) stays unchanged and
// never carries the sections, also for a manager with an exporter.
func TestExportV1HasNoFieldPermissions(t *testing.T) {
	grants := &fakeGrants{}
	h := NewHandler(&snapshotMemory{MemoryRepository: NewMemoryRepository()}).
		WithLifecycleModels(noModels{}).WithRoles(noRoles{}).WithGrantExporter(grants)
	manager := principalWith(orgWideScope(), identity.PermCIRead, identity.PermFieldPermissionManage)
	for _, v := range []string{"", "1"} {
		code, body := exportAs(t, h, v, manager)
		var members map[string]json.RawMessage
		if err := json.Unmarshal(body, &members); code != http.StatusOK || err != nil {
			t.Fatalf("v1 = %d %s", code, body)
		}
		for _, k := range []string{"field_grants", "field_sensitivity", "omitted_sections"} {
			if _, ok := members[k]; ok {
				t.Errorf("v1 carries %s", k)
			}
		}
		if string(members["format_version"]) != "1" {
			t.Errorf("v1 format_version = %s", members["format_version"])
		}
	}
	if grants.calls != 0 {
		t.Errorf("v1 called the exporter %d times", grants.calls)
	}
}
