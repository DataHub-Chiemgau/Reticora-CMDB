// Administration of the field permissions over REST (API-06, MET-54,
// RBA-07, WP-726): /api/v1/field-permissions with object grants and
// sensitive fields through the production router (route authorization,
// OpenAPI validation, route audit) over the PostgreSQL repositories, and
// the field permission sections of the metamodel export v2.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestFieldPermissionAPI' ./internal/permission/
package permission_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// fpStack is the production router on a scopetest fixture; callers are
// fixed principals per request.
type fpStack struct {
	*scopetest.Fixture
	t        *testing.T
	handler  http.Handler
	store    cache.Store
	repos    server.Repositories
	admin    string // user of organization A
	adminB   string // user of organization B
	server   string // system CI type server
	relType  string // a system relationship type
	relKey   string
	engineer string // standard role engineer of organization A
}

func newFPStack(t *testing.T) *fpStack {
	t.Helper()
	f := scopetest.Seed(t, "1b")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{})
	store := cache.NewMemoryStore()
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-726", Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		AuditPool:   f.App, FieldPolicyCache: store,
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	s := &fpStack{Fixture: f, t: t, store: store, repos: repos,
		handler: middleware.Chain(middleware.RequestID, middleware.TenantMiddleware)(mux)}
	s.admin = f.AppUser(t, f.OrgA, "fp-admin")
	s.adminB = f.AppUser(t, f.OrgB, "fp-admin-b")
	s.server = s.str(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT id::text, key FROM relationship_type WHERE organization_id IS NULL ORDER BY key LIMIT 1`).Scan(&s.relType, &s.relKey); err != nil {
		t.Fatal(err)
	}
	s.engineer = s.role(f.OrgA, "engineer")
	return s
}

func (s *fpStack) str(sql string, args ...any) string {
	s.t.Helper()
	var v string
	if err := s.Admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (s *fpStack) count(sql string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (s *fpStack) role(orgID, name string) string {
	return s.str(`SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, orgID, name)
}

func (s *fpStack) customRole(orgID, name string) string {
	id := s.str(`INSERT INTO custom_role (organization_id, name, permissions) VALUES ($1, $2, '["ci:read","ci:write"]')
		RETURNING id::text`, orgID, name)
	s.t.Cleanup(func() {
		_, _ = s.Admin.Exec(context.Background(), `DELETE FROM user_custom_role WHERE custom_role_id = $1`, id)
		_, _ = s.Admin.Exec(context.Background(), `DELETE FROM field_grant WHERE custom_role_id = $1`, id)
		_, _ = s.Admin.Exec(context.Background(), `DELETE FROM object_grant WHERE grantee_custom_role_id = $1`, id)
		_, _ = s.Admin.Exec(context.Background(), `DELETE FROM custom_role WHERE id = $1`, id)
	})
	return id
}

func orgWide() *identity.Scope {
	all := identity.ScopeSet{All: true}
	return &identity.Scope{Clients: all, Sites: all, Teams: all}
}

// fpCaller is a principal of a request.
type fpCaller struct {
	org, user string
	scope     *identity.Scope
	perms     []identity.Permission
	narrowed  map[identity.Permission]identity.Scope
}

func (s *fpStack) adminA() *fpCaller {
	return &fpCaller{org: s.OrgA, user: s.admin, scope: orgWide(), perms: identity.AllPermissions()}
}

// without is every permission except drop.
func without(drop identity.Permission) []identity.Permission {
	return slices.DeleteFunc(identity.AllPermissions(), func(p identity.Permission) bool { return p == drop })
}

type fpResponse struct {
	code      int
	body      []byte
	requestID string
}

func (r *fpResponse) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (s *fpStack) call(c *fpCaller, method, path string, body any) *fpResponse {
	s.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	p := identity.Principal{Subject: c.user, OrganizationID: c.org, Scope: c.scope, PermissionScopes: c.narrowed,
		Type: identity.PrincipalTypeUser, Permissions: c.perms, Credential: "wp726-" + c.user}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req.WithContext(ctx))
	return &fpResponse{code: w.Code, body: w.Body.Bytes(), requestID: w.Header().Get("X-Request-ID")}
}

// must calls and fails unless the status is want.
func (s *fpStack) must(want int, c *fpCaller, method, path string, body any) *fpResponse {
	s.t.Helper()
	r := s.call(c, method, path, body)
	if r.code != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, r.code, r.body, want)
	}
	return r
}

// auditEntries counts the route entries of a request with row values.
func (s *fpStack) auditEntries(r *fpResponse) (entries, withValues int) {
	s.t.Helper()
	if err := s.Admin.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE changes ? 'values')
		FROM audit_log WHERE organization_id IN ($1, $2) AND request_id = $3 AND resource_type = 'field-permissions'`,
		s.OrgA, s.OrgB, r.requestID).Scan(&entries, &withValues); err != nil {
		s.t.Fatal(err)
	}
	return entries, withValues
}

type grantPage struct {
	Data       []map[string]any `json:"data"`
	Limit      int              `json:"limit"`
	HasMore    bool             `json:"has_more"`
	NextCursor string           `json:"next_cursor"`
}

// ids pages through path with limit 1 and returns every id in order.
func (s *fpStack) ids(c *fpCaller, path string) []string {
	s.t.Helper()
	var out []string
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	next := path + sep + "limit=1"
	for i := 0; next != "" && i < 50; i++ {
		var page grantPage
		s.must(http.StatusOK, c, http.MethodGet, next, nil).decode(s.t, &page)
		if len(page.Data) > 1 || page.Limit != 1 || page.HasMore != (page.NextCursor != "") {
			s.t.Fatalf("GET %s: page %+v", next, page)
		}
		for _, item := range page.Data {
			out = append(out, item["id"].(string))
		}
		next = ""
		if page.NextCursor != "" {
			next = path + sep + "limit=1&cursor=" + page.NextCursor
		}
	}
	return out
}

// TestFieldPermissionAPI covers the CRUD of the three resources with
// cursor lists, the audit entry of every write, 409 and 422, the immediate
// effect on the resolver and the export v2 sections.
func TestFieldPermissionAPI(t *testing.T) {
	s := newFPStack(t)
	admin := s.adminA()
	bg := context.Background()
	ops := s.customRole(s.OrgA, "wp726-ops")
	operator := s.AppUser(t, s.OrgA, "fp-operator")
	if _, err := s.Admin.Exec(bg, `INSERT INTO user_custom_role (user_id, custom_role_id, scope_type) VALUES ($1, $2, 'organization')`,
		operator, ops); err != nil {
		t.Fatal(err)
	}
	ciA := s.CI(t, s.OrgA, s.Client1, "fp-ci-a")
	team := s.str(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp726-team') RETURNING id::text`, s.OrgA)

	// The resolver of the operator before the change: no field permission
	// in the organization, serial_number writable; the snapshot is cached.
	resolver := permission.NewFieldResolver(s.repos.Permission, nil, s.repos.FieldGrants, s.store)
	opPrincipal := identity.Principal{Subject: operator, OrganizationID: s.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermCIRead, identity.PermCIWrite}}
	policy, err := resolver.Resolve(bg, opPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	obj := permission.ObjectRef{ID: ciA, ClientID: s.Client1}
	if lvl := policy.Level(permission.KindCI, s.server, "core.serial_number", obj); lvl != permission.LevelWrite {
		t.Fatalf("before: serial_number = %s, want write", lvl)
	}

	// Field grants: create three, each audited with the row values.
	created := map[string]permission.FieldGrant{}
	for name, body := range map[string]map[string]any{
		"serial":       {"custom_role_id": ops, "object_kind": "ci", "ci_type_id": s.server, "field_key": "core.serial_number", "level": "read"},
		"delete":       {"custom_role_id": ops, "object_kind": "ci", "action": "ci:delete", "level": "none"},
		"engineer-rel": {"role_id": s.engineer, "object_kind": "ci", "relationship_type_id": s.relType, "level": "read"},
	} {
		r := s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions", body)
		var g permission.FieldGrant
		r.decode(t, &g)
		if g.ID == "" || g.OrganizationID != s.OrgA || g.CreatedBy == nil || *g.CreatedBy != s.admin {
			t.Errorf("%s: created %+v", name, g)
		}
		if n, values := s.auditEntries(r); n != 1 || values != 1 {
			t.Errorf("POST field grant %s: %d audit entries (%d with values), want 1 with values", name, n, values)
		}
		created[name] = g
	}
	if created["engineer-rel"].RelationshipType != s.relKey {
		t.Errorf("relationship_type = %q, want %q", created["engineer-rel"].RelationshipType, s.relKey)
	}

	// The change is effective at once: the write dropped the cached
	// snapshot after its commit.
	if policy, err = resolver.Resolve(bg, opPrincipal); err != nil {
		t.Fatal(err)
	}
	if lvl := policy.Level(permission.KindCI, s.server, "core.serial_number", obj); lvl != permission.LevelRead {
		t.Errorf("after POST: serial_number = %s, want read", lvl)
	}
	if policy.ActionAllowed(permission.KindCI, s.server, "ci:delete", obj) {
		t.Error("after POST: ci:delete still allowed")
	}

	// Cursor list: every row once, in creation order; filters.
	all := s.ids(admin, "/api/v1/field-permissions")
	if len(all) != 3 {
		t.Fatalf("field grants %v, want 3", all)
	}
	if got := s.ids(admin, "/api/v1/field-permissions?custom_role_id="+ops); len(got) != 2 {
		t.Errorf("filter custom_role_id: %v", got)
	}
	if got := s.ids(admin, "/api/v1/field-permissions?role_id="+s.engineer+"&object_kind=ci"); !slices.Equal(got, []string{created["engineer-rel"].ID}) {
		t.Errorf("filter role_id: %v", got)
	}
	var empty map[string]json.RawMessage
	s.must(http.StatusOK, admin, http.MethodGet, "/api/v1/field-permissions?object_kind=asset", nil).decode(t, &empty)
	if string(empty["data"]) != "[]" {
		t.Errorf("empty list data = %s", empty["data"])
	}
	s.must(http.StatusBadRequest, admin, http.MethodGet, "/api/v1/field-permissions?object_kind=server", nil)
	s.must(http.StatusBadRequest, admin, http.MethodGet, "/api/v1/field-permissions?cursor=eyJvIjoxfQ.AAAAAAAAAAAAAAAAAAAAAA", nil)

	// PATCH changes only the level; an action grant cannot be read.
	r := s.must(http.StatusOK, admin, http.MethodPatch, "/api/v1/field-permissions/"+created["serial"].ID, map[string]any{"level": "none"})
	var patched permission.FieldGrant
	r.decode(t, &patched)
	if patched.Level != permission.LevelNone || patched.FieldKey == nil || *patched.FieldKey != "core.serial_number" {
		t.Errorf("patched = %+v", patched)
	}
	if n, values := s.auditEntries(r); n != 1 || values != 1 {
		t.Errorf("PATCH: %d audit entries (%d with values)", n, values)
	}
	if policy, err = resolver.Resolve(bg, opPrincipal); err != nil {
		t.Fatal(err)
	}
	if lvl := policy.Level(permission.KindCI, s.server, "core.serial_number", obj); lvl != permission.LevelNone {
		t.Errorf("after PATCH: serial_number = %s, want none", lvl)
	}
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPatch, "/api/v1/field-permissions/"+created["delete"].ID, map[string]any{"level": "read"})
	s.must(http.StatusBadRequest, admin, http.MethodPatch, "/api/v1/field-permissions/"+created["delete"].ID,
		map[string]any{"level": "none", "field_key": "core.fqdn"})

	// 409 for an equal grant, 422 for unknown keys and references.
	s.must(http.StatusConflict, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"custom_role_id": ops, "object_kind": "ci", "action": "ci:delete", "level": "write"})
	unknownKey := s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"custom_role_id": ops, "object_kind": "ci", "field_key": "core.no_such_column", "level": "read"})
	if !bytes.Contains(unknownKey.body, []byte(`"field":"field_key"`)) {
		t.Errorf("unknown field key: %s", unknownKey.body)
	}
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"custom_role_id": ops, "object_kind": "ci", "field_key": "attributes.wp726_missing", "level": "read"})
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"custom_role_id": ops, "object_kind": "ci", "ci_type_id": "72600000-0000-4000-8000-0000000000ff", "level": "read"})
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"custom_role_id": ops, "object_kind": "ci", "action": "ci:no_such_action", "level": "none"})
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"role_id": s.engineer, "custom_role_id": ops, "object_kind": "ci", "level": "read"})

	// Object grants: a team gets write on a CI of the organization.
	r = s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions/object-grants",
		map[string]any{"grantee_team_id": team, "object_kind": "ci", "object_id": ciA, "level": "write"})
	var og permission.ObjectGrant
	r.decode(t, &og)
	if n, values := s.auditEntries(r); n != 1 || values != 1 {
		t.Errorf("POST object grant: %d audit entries (%d with values)", n, values)
	}
	og2 := s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions/object-grants",
		map[string]any{"grantee_custom_role_id": ops, "object_kind": "ci", "object_id": ciA, "level": "read"})
	var second permission.ObjectGrant
	og2.decode(t, &second)
	if got := s.ids(admin, "/api/v1/field-permissions/object-grants?object_kind=ci&object_id="+ciA); !slices.Equal(got, []string{og.ID, second.ID}) {
		t.Errorf("object grants of the CI %v, want %v", got, []string{og.ID, second.ID})
	}
	if got := s.ids(admin, "/api/v1/field-permissions/object-grants?grantee_team_id="+team); !slices.Equal(got, []string{og.ID}) {
		t.Errorf("object grants of the team %v", got)
	}
	s.must(http.StatusConflict, admin, http.MethodPost, "/api/v1/field-permissions/object-grants",
		map[string]any{"grantee_team_id": team, "object_kind": "ci", "object_id": ciA, "level": "read"})
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions/object-grants",
		map[string]any{"grantee_team_id": team, "object_kind": "ci", "object_id": "72600000-0000-4000-8000-0000000000fe", "level": "read"})
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions/object-grants",
		map[string]any{"grantee_team_id": team, "object_kind": "contract", "object_id": ciA, "level": "read"})

	// Sensitive fields.
	r = s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions/sensitive-fields",
		map[string]any{"object_kind": "asset", "field_key": "core.purchase_cost"})
	var sens permission.SensitiveField
	r.decode(t, &sens)
	if n, values := s.auditEntries(r); n != 1 || values != 1 {
		t.Errorf("POST sensitive field: %d audit entries (%d with values)", n, values)
	}
	s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions/sensitive-fields",
		map[string]any{"object_kind": "ci", "ci_type_id": s.server, "field_key": "core.management_ip"})
	if got := s.ids(admin, "/api/v1/field-permissions/sensitive-fields"); len(got) != 2 || got[0] != sens.ID {
		t.Errorf("sensitive fields %v", got)
	}
	if got := s.ids(admin, "/api/v1/field-permissions/sensitive-fields?object_kind=asset"); !slices.Equal(got, []string{sens.ID}) {
		t.Errorf("sensitive fields of assets %v", got)
	}
	s.must(http.StatusConflict, admin, http.MethodPost, "/api/v1/field-permissions/sensitive-fields",
		map[string]any{"object_kind": "asset", "field_key": "core.purchase_cost"})
	s.must(http.StatusUnprocessableEntity, admin, http.MethodPost, "/api/v1/field-permissions/sensitive-fields",
		map[string]any{"object_kind": "document", "field_key": "core.purchase_cost"})

	// Export v2: the sections with keys instead of ids for the manager.
	var doc citype.MetamodelExportV2
	exp := s.must(http.StatusOK, admin, http.MethodGet, "/api/v1/ci-types/export?format_version=2", nil)
	exp.decode(t, &doc)
	if len(doc.OmittedSections) != 0 || doc.FieldGrants == nil || doc.FieldSensitivity == nil {
		t.Fatalf("export v2 for the manager: omitted %v, grants %v", doc.OmittedSections, doc.FieldGrants)
	}
	want := citype.ExportFieldGrantV2{Role: "wp726-ops", RoleKind: "custom_role", ObjectKind: "ci", CIType: "server",
		FieldKey: "core.serial_number", Level: "none"}
	if !slices.Contains(*doc.FieldGrants, want) {
		t.Errorf("field_grants %+v lack %+v", *doc.FieldGrants, want)
	}
	if !slices.Contains(*doc.FieldGrants, citype.ExportFieldGrantV2{Role: "engineer", RoleKind: "role", ObjectKind: "ci",
		RelationshipType: s.relKey, Level: "read"}) {
		t.Errorf("field_grants %+v lack the relationship grant of engineer", *doc.FieldGrants)
	}
	if !slices.Contains(*doc.FieldSensitivity, citype.ExportSensitiveFieldV2{ObjectKind: "ci", CIType: "server", FieldKey: "core.management_ip"}) {
		t.Errorf("field_sensitivity %+v", *doc.FieldSensitivity)
	}
	section := exp.body[bytes.Index(exp.body, []byte(`"field_grants"`)):]
	for _, id := range []string{ops, s.engineer, s.server, s.relType, ciA, team} {
		if bytes.Contains(section, []byte(id)) {
			t.Errorf("the field permission sections carry the id %s", id)
		}
	}
	// Engineer (no field_permission:manage) and a client-scoped admin: the
	// sections are left out and named.
	clientScope := orgWide()
	clientScope.Clients = identity.ScopeSet{IDs: []string{s.Client1}}
	for name, c := range map[string]*fpCaller{
		"engineer":     {org: s.OrgA, user: s.admin, scope: orgWide(), perms: without(identity.PermFieldPermissionManage)},
		"client admin": {org: s.OrgA, user: s.admin, scope: clientScope, perms: identity.AllPermissions()},
	} {
		var hidden citype.MetamodelExportV2
		r = s.must(http.StatusOK, c, http.MethodGet, "/api/v1/ci-types/export?format_version=2", nil)
		r.decode(t, &hidden)
		if hidden.FieldGrants != nil || hidden.FieldSensitivity != nil || bytes.Contains(r.body, []byte(`"field_grants":`)) ||
			!slices.Equal(hidden.OmittedSections, []string{citype.SectionFieldGrants, citype.SectionFieldSensitivity}) {
			t.Errorf("%s: export v2 omitted %v, sections present %v", name, hidden.OmittedSections, hidden.FieldGrants != nil)
		}
	}
	// The export of the manager imports into the organization: the
	// sections are accepted and reported as skipped.
	imp := s.call(admin, http.MethodPost, "/api/v1/metamodel/import", json.RawMessage(exp.body))
	if imp.code != http.StatusOK && imp.code != http.StatusCreated {
		t.Errorf("import of the export v2 = %d %s", imp.code, imp.body)
	} else if !bytes.Contains(imp.body, []byte(`"field-permissions-skipped"`)) {
		t.Errorf("import report without field-permissions-skipped: %s", imp.body)
	}

	// Deletes: 204 with an audit entry holding the old values, then 404.
	for _, path := range []string{
		"/api/v1/field-permissions/" + created["serial"].ID,
		"/api/v1/field-permissions/object-grants/" + og.ID,
		"/api/v1/field-permissions/sensitive-fields/" + sens.ID,
	} {
		r = s.must(http.StatusNoContent, admin, http.MethodDelete, path, nil)
		if n, values := s.auditEntries(r); n != 1 || values != 1 {
			t.Errorf("DELETE %s: %d audit entries (%d with values)", path, n, values)
		}
		s.must(http.StatusNotFound, admin, http.MethodDelete, path, nil)
	}
	s.must(http.StatusNotFound, admin, http.MethodPatch, "/api/v1/field-permissions/"+created["serial"].ID, map[string]any{"level": "read"})
	// The object grant of ops on ciA remains (read); another CI follows the
	// role again.
	if policy, err = resolver.Resolve(bg, opPrincipal); err != nil {
		t.Fatal(err)
	}
	other := permission.ObjectRef{ID: s.CI(t, s.OrgA, s.Client1, "fp-ci-other"), ClientID: s.Client1}
	if lvl := policy.Level(permission.KindCI, s.server, "core.serial_number", other); lvl != permission.LevelWrite {
		t.Errorf("after DELETE: serial_number = %s, want write again", lvl)
	}
	if lvl := policy.Level(permission.KindCI, s.server, "core.serial_number", obj); lvl != permission.LevelRead {
		t.Errorf("after DELETE: serial_number of the CI with the object grant = %s, want read", lvl)
	}
	if n := s.count(`SELECT count(*) FROM field_grant WHERE organization_id = $1`, s.OrgA); n != 2 {
		t.Errorf("%d field grants left, want 2", n)
	}
}

// TestFieldPermissionAPIRefusesForeignAndRestricted covers the fail-closed
// rules: 403 without field_permission:manage (reads included), 403 for a
// holder restricted to a client, a site, a team or with the permission
// narrowed to a client; references into another organization are 422, rows
// of another organization 404 and invisible in its lists; no refused write
// leaves a row or an audit entry.
func TestFieldPermissionAPIRefusesForeignAndRestricted(t *testing.T) {
	s := newFPStack(t)
	admin := s.adminA()
	adminB := &fpCaller{org: s.OrgB, user: s.adminB, scope: orgWide(), perms: identity.AllPermissions()}
	ops := s.customRole(s.OrgA, "wp726-ops")
	opsB := s.customRole(s.OrgB, "wp726-ops-b")
	ciA := s.CI(t, s.OrgA, s.Client1, "fp-ci-a")
	ciB := s.CI(t, s.OrgB, "", "fp-ci-b")
	teamB := s.str(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp726-team-b') RETURNING id::text`, s.OrgB)
	typeB := s.str(`INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'wp726_type_b', 'B') RETURNING id::text`, s.OrgB)

	var grant permission.FieldGrant
	s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions",
		map[string]any{"custom_role_id": ops, "object_kind": "ci", "field_key": "core.serial_number", "level": "read"}).decode(t, &grant)
	var og permission.ObjectGrant
	s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions/object-grants",
		map[string]any{"grantee_custom_role_id": ops, "object_kind": "ci", "object_id": ciA, "level": "read"}).decode(t, &og)
	var sens permission.SensitiveField
	s.must(http.StatusCreated, admin, http.MethodPost, "/api/v1/field-permissions/sensitive-fields",
		map[string]any{"object_kind": "ci", "field_key": "core.fqdn"}).decode(t, &sens)
	rows := func() int {
		return s.count(`SELECT (SELECT count(*) FROM field_grant WHERE organization_id IN ($1, $2)) +
			(SELECT count(*) FROM object_grant WHERE organization_id IN ($1, $2)) +
			(SELECT count(*) FROM field_sensitivity WHERE organization_id IN ($1, $2))`, s.OrgA, s.OrgB)
	}
	audited := func() int {
		return s.count(`SELECT count(*) FROM audit_log WHERE organization_id IN ($1, $2) AND resource_type = 'field-permissions'`, s.OrgA, s.OrgB)
	}
	before, entries := rows(), audited()

	// Refused principals: every operation is 403, reads included.
	restricted := func(scope *identity.Scope) *fpCaller {
		return &fpCaller{org: s.OrgA, user: s.admin, scope: scope, perms: identity.AllPermissions()}
	}
	clientScope, siteScope, teamScope := orgWide(), orgWide(), orgWide()
	clientScope.Clients = identity.ScopeSet{IDs: []string{s.Client1}}
	siteScope.Sites = identity.ScopeSet{IDs: []string{"72600000-0000-4000-8000-00000000005e"}}
	teamScope.Teams = identity.ScopeSet{IDs: []string{"72600000-0000-4000-8000-0000000000e1"}}
	narrowed := s.adminA()
	narrowed.narrowed = map[identity.Permission]identity.Scope{identity.PermFieldPermissionManage: *clientScope}
	refused := map[string]*fpCaller{
		"engineer":         {org: s.OrgA, user: s.admin, scope: orgWide(), perms: without(identity.PermFieldPermissionManage)},
		"client admin":     restricted(clientScope),
		"site admin":       restricted(siteScope),
		"team admin":       restricted(teamScope),
		"narrowed to site": narrowed,
	}
	requests := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/field-permissions", nil},
		{http.MethodGet, "/api/v1/field-permissions/object-grants", nil},
		{http.MethodGet, "/api/v1/field-permissions/sensitive-fields", nil},
		{http.MethodPost, "/api/v1/field-permissions", map[string]any{"custom_role_id": ops, "object_kind": "ci", "field_key": "core.fqdn", "level": "none"}},
		{http.MethodPatch, "/api/v1/field-permissions/" + grant.ID, map[string]any{"level": "write"}},
		{http.MethodDelete, "/api/v1/field-permissions/" + grant.ID, nil},
		{http.MethodPost, "/api/v1/field-permissions/object-grants", map[string]any{"grantee_custom_role_id": ops, "object_kind": "ci", "object_id": ciA, "level": "write"}},
		{http.MethodDelete, "/api/v1/field-permissions/object-grants/" + og.ID, nil},
		{http.MethodPost, "/api/v1/field-permissions/sensitive-fields", map[string]any{"object_kind": "ci", "field_key": "core.hostname"}},
		{http.MethodDelete, "/api/v1/field-permissions/sensitive-fields/" + sens.ID, nil},
	}
	for name, c := range refused {
		for _, req := range requests {
			if r := s.call(c, req.method, req.path, req.body); r.code != http.StatusForbidden {
				t.Errorf("%s: %s %s = %d %s, want 403", name, req.method, req.path, r.code, r.body)
			}
		}
	}

	// Organization B: no row of A in its lists; A's ids are 404.
	for _, path := range []string{"/api/v1/field-permissions", "/api/v1/field-permissions/object-grants",
		"/api/v1/field-permissions/sensitive-fields"} {
		if got := s.ids(adminB, path); len(got) != 0 {
			t.Errorf("organization B lists %s: %v", path, got)
		}
	}
	s.must(http.StatusNotFound, adminB, http.MethodPatch, "/api/v1/field-permissions/"+grant.ID, map[string]any{"level": "write"})
	s.must(http.StatusNotFound, adminB, http.MethodDelete, "/api/v1/field-permissions/"+grant.ID, nil)
	s.must(http.StatusNotFound, adminB, http.MethodDelete, "/api/v1/field-permissions/object-grants/"+og.ID, nil)
	s.must(http.StatusNotFound, adminB, http.MethodDelete, "/api/v1/field-permissions/sensitive-fields/"+sens.ID, nil)
	s.must(http.StatusNotFound, admin, http.MethodDelete, "/api/v1/field-permissions/not-a-uuid", nil)

	// References into organization B from A: 422.
	for name, req := range map[string]struct {
		path string
		body map[string]any
	}{
		"custom role of B":    {"/api/v1/field-permissions", map[string]any{"custom_role_id": opsB, "object_kind": "ci", "level": "read"}},
		"standard role of B":  {"/api/v1/field-permissions", map[string]any{"role_id": s.role(s.OrgB, "engineer"), "object_kind": "ci", "level": "read"}},
		"ci type of B":        {"/api/v1/field-permissions", map[string]any{"custom_role_id": ops, "object_kind": "ci", "ci_type_id": typeB, "level": "read"}},
		"sensitive type of B": {"/api/v1/field-permissions/sensitive-fields", map[string]any{"object_kind": "ci", "ci_type_id": typeB, "field_key": "core.fqdn"}},
		"object of B":         {"/api/v1/field-permissions/object-grants", map[string]any{"grantee_custom_role_id": ops, "object_kind": "ci", "object_id": ciB, "level": "read"}},
		"team of B":           {"/api/v1/field-permissions/object-grants", map[string]any{"grantee_team_id": teamB, "object_kind": "ci", "object_id": ciA, "level": "read"}},
		"role of B on own CI": {"/api/v1/field-permissions/object-grants", map[string]any{"grantee_custom_role_id": opsB, "object_kind": "ci", "object_id": ciA, "level": "read"}},
	} {
		if r := s.call(admin, http.MethodPost, req.path, req.body); r.code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", name, r.code, r.body)
		} else if bytes.Contains(r.body, []byte("field_grant")) || bytes.Contains(r.body, []byte("object_grant")) ||
			bytes.Contains(r.body, []byte("fkey")) {
			t.Errorf("%s: the answer names the schema: %s", name, r.body)
		}
	}
	if got, n := rows(), audited(); got != before || n != entries {
		t.Errorf("refused requests changed rows %d -> %d or audit entries %d -> %d", before, got, entries, n)
	}
}
