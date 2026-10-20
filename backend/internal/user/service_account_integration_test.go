package user_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

// TestServiceAccounts covers WP-069 (RBA-08, API-05): service accounts are
// managed through /service-accounts with role assignments and scopes; an API
// key bound to an account acts with the account's rights within its scope and
// names the account as subject; the webhook filter lets an account read only
// objects in its scope; a deactivated account grants nothing; an account with
// bound keys cannot be deleted; every change is audited.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestServiceAccounts(t *testing.T) {
	f := scopetest.Seed(t, "62")
	bg := context.Background()
	admin := f.AppUser(t, f.OrgA, "sa-admin")
	repo := user.NewPGServiceAccounts(f.App)
	keys := identity.NewPGAPIKeyStore(f.App)

	mux := chi.NewRouter()
	user.NewServiceAccountHandler(repo).RegisterRoutes(mux)
	// Wired like the router: binding checks the account against the caller.
	identity.NewAPIKeyHandler(keys).WithOwnerAccess(permission.NewPGRepository(f.App)).
		WithServiceAccountAccess(user.ServiceAccountAccess{Repo: repo}).RegisterRoutes(mux)
	principal := identity.Principal{Subject: admin, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermUserManage, identity.PermRoleManage, identity.PermAPIKeyManage, identity.PermCIRead, identity.PermCIWrite}}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, admin)
		ctx := identity.WithPrincipal(req.Context(), principal)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}

	w := call(http.MethodPost, "/api/v1/service-accounts", `{"name":"ci-exporter","description":"nightly export"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var sa user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPost, "/api/v1/service-accounts", `{"name":"ci-exporter"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate name: %d, want 409", w.Code)
	}

	// The account reads and writes CIs, but only in client 1.
	roleID := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'sa-role', '["ci:read","ci:write"]')`, roleID, f.OrgA); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", `{"roles":[{"role_id":"`+roleID+`","scope_client_id":"`+f.Client1+`"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), f.Client1) {
		t.Fatalf("set roles: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", `{"roles":[{"role_id":"`+f.ID()+`"}]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown role: %d, want 422", w.Code)
	}
	if w = call(http.MethodGet, "/api/v1/service-accounts/"+sa.ID, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), roleID) {
		t.Errorf("get: %d %s (roles must be unchanged after the failed replacement)", w.Code, w.Body.String())
	}

	// Webhook filter: objects of client 1 and org-wide objects, not client 2.
	access := user.ServiceAccountAccess{Repo: repo}
	for _, tc := range []struct {
		permission, client string
		want               bool
	}{
		{"ci:read", f.Client1, true},
		{"ci:read", "", true},
		{"ci:read", f.Client2, false},
		{"asset:read", f.Client1, false},
	} {
		if ok, err := access.CanReadEvent(bg, f.OrgA, sa.ID, tc.permission, tc.client, ""); err != nil || ok != tc.want {
			t.Errorf("CanReadEvent(%s, %q) = %v, %v; want %v", tc.permission, tc.client, ok, err, tc.want)
		}
	}

	// A key bound to the account acts as the account.
	w = call(http.MethodPost, "/api/v1/api-keys", `{"name":"exporter-key","permissions":["ci:read","ci:write"],"service_account_id":"`+sa.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create bound key: %d %s", w.Code, w.Body.String())
	}
	var key struct {
		Key              string `json:"key"`
		ServiceAccountID string `json:"service_account_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil || key.ServiceAccountID != sa.ID {
		t.Fatalf("bound key %s, %v", w.Body.String(), err)
	}
	service := identity.NewAPIKeyServiceWithStore(keys).WithOwnerAccess(permission.NewPGRepository(f.App)).
		WithServiceAccountAccess(access)
	info, err := service.Validate(bg, key.Key)
	if err != nil {
		t.Fatal(err)
	}
	if info.OwnerID != sa.ID || len(info.Scopes) != 2 || info.Scope == nil || info.Scope.Clients.All ||
		len(info.Scope.Clients.IDs) != 1 || info.Scope.Clients.IDs[0] != f.Client1 {
		t.Errorf("bound key info %+v scope %+v, want the account's rights in client 1", info, info.Scope)
	}

	// Deactivation removes every right of the account.
	if w = call(http.MethodPatch, "/api/v1/service-accounts/"+sa.ID, `{"is_active":false}`); w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
	if info, err = service.Validate(bg, key.Key); err != nil || len(info.Scopes) != 0 {
		t.Errorf("key of a deactivated account: %+v, %v; want no permissions", info, err)
	}
	if ok, _ := access.CanReadEvent(bg, f.OrgA, sa.ID, "ci:read", f.Client1, ""); ok {
		t.Error("deactivated account still reads events")
	}

	// Bound accounts cannot be deleted; unbound ones can.
	if w = call(http.MethodDelete, "/api/v1/service-accounts/"+sa.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("delete bound account: %d, want 409", w.Code)
	}
	w = call(http.MethodPost, "/api/v1/service-accounts", `{"name":"spare"}`)
	var spare user.ServiceAccount
	_ = json.Unmarshal(w.Body.Bytes(), &spare)
	if w = call(http.MethodDelete, "/api/v1/service-accounts/"+spare.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete unbound account: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodGet, "/api/v1/service-accounts/"+f.ID(), ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown account: %d, want 404", w.Code)
	}
	if w = call(http.MethodGet, "/api/v1/service-accounts", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ci-exporter") {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}

	var actions []string
	rows, err := f.Admin.Query(bg, `SELECT action FROM audit_log WHERE organization_id = $1 AND resource_type = 'service_account' AND actor_id = $2 ORDER BY timestamp`, f.OrgA, admin)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	rows.Close()
	want := "service_account.created,service_account.roles_changed,service_account.updated,service_account.created,service_account.deleted"
	if strings.Join(actions, ",") != want {
		t.Errorf("audit %v, want %s", actions, want)
	}
}

// TestServiceAccountRoleCeiling covers WP-355 (RBA-08, API-05, F-008): a
// service account's roles are assigned like a user's, with role:manage and
// never beyond the caller's rights. Every assignment of the new list and
// every existing assignment the list drops must lie within the caller's
// rights in the assignment's scope; otherwise 403 naming what is not
// covered, the assignments stay unchanged and no roles_changed entry is
// audited. A service account of another organization is 404.
//
// Principals are resolved from their stored role assignments as at login,
// the request scope by middleware.TenantScopeFor.
func TestServiceAccountRoleCeiling(t *testing.T) {
	f := scopetest.Seed(t, "6a")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var engineer string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA).Scan(&engineer); err != nil {
		t.Fatalf("built-in engineer role: %v", err)
	}
	orgAdminID := f.AppUser(t, f.OrgA, "sa-ceiling-org-admin")
	clientAdminID := f.AppUser(t, f.OrgA, "sa-ceiling-client-admin")
	userManagerID := f.AppUser(t, f.OrgA, "sa-ceiling-user-manager")
	// org_admin organization-wide, org_admin limited to client 1, and an
	// organization-wide user manager with the engineer's rights but without
	// role:manage.
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, orgAdminID)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, clientAdminID, f.Client1)
	managerRole := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'sa-ceiling-user-manager', '["user:read","user:manage"]')`, managerRole, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3), ($1, $2, $4)`, f.OrgA, userManagerID, managerRole, engineer)

	perms := permission.NewPGRepository(f.App)
	resolve := func(userID string) identity.Principal {
		t.Helper()
		grants, err := perms.AccessGrants(bg, f.OrgA, userID)
		if err != nil {
			t.Fatalf("access grants of %s: %v", userID, err)
		}
		access := identity.ResolveAccess(grants)
		scope := access.Scope
		return identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: access.Permissions, Scope: &scope, PermissionScopes: access.PermissionScopes, ClientScope: scope.LegacyClientScope()}
	}
	orgAdmin, clientAdmin, userManager := resolve(orgAdminID), resolve(clientAdminID), resolve(userManagerID)
	if !clientAdmin.Has(identity.PermRoleManage) || clientAdmin.Scope == nil || clientAdmin.Scope.Clients.All {
		t.Fatalf("fixture: client admin %+v, want role:manage limited to client 1", clientAdmin)
	}

	repo := user.NewPGServiceAccounts(f.App)
	mux := chi.NewRouter()
	user.NewServiceAccountHandler(repo).RegisterRoutes(mux)
	call := func(p identity.Principal, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := middleware.TenantScopeFor(&p)
		ctx := identity.WithPrincipal(req.Context(), p)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: p.OrganizationID, UserID: p.Subject, ClientID: p.ClientScope})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}
	newSA := func(orgID, name string) string {
		t.Helper()
		sa := &user.ServiceAccount{OrganizationID: orgID, Name: name}
		if err := repo.CreateServiceAccount(f.OrgCtx(orgID), sa); err != nil {
			t.Fatalf("create service account %s: %v", name, err)
		}
		return sa.ID
	}
	// assignments reads the stored assignments directly.
	assignments := func(saID string) string {
		t.Helper()
		var got string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(string_agg(role_id::text || '/' || COALESCE(scope_client_id::text, '-') || '/' || COALESCE(scope_site_id::text, '-'), ',' ORDER BY role_id, scope_client_id, scope_site_id), '')
			FROM service_account_role WHERE service_account_id = $1`, saID).Scan(&got); err != nil {
			t.Fatalf("read assignments: %v", err)
		}
		return got
	}
	audited := func(saID string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = 'service_account'
			AND resource_id = $2 AND action = 'service_account.roles_changed'`, f.OrgA, saID).Scan(&n); err != nil {
			t.Fatalf("read audit: %v", err)
		}
		return n
	}
	rolesPath := func(saID string) string { return "/api/v1/service-accounts/" + saID + "/roles" }
	orgWide := `{"roles":[{"role_id":"` + engineer + `"}]}`
	inClient := func(client string) string {
		return `{"roles":[{"role_id":"` + engineer + `","scope_client_id":"` + client + `"}]}`
	}
	engineerOrgWide := engineer + "/-/-"
	engineerClient1 := engineer + "/" + f.Client1 + "/-"

	// refused asserts a 403 problem naming want, unchanged assignments and no
	// new audit entry.
	refused := func(t *testing.T, w *httptest.ResponseRecorder, saID, before string, auditBefore int, want ...string) {
		t.Helper()
		if w.Code != http.StatusForbidden || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("status %d %q %s, want 403 problem+json", w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
		for _, s := range want {
			if !strings.Contains(w.Body.String(), s) {
				t.Errorf("403 body %s does not name %q", w.Body.String(), s)
			}
		}
		if got := assignments(saID); got != before {
			t.Errorf("assignments after the refusal %q, want unchanged %q", got, before)
		}
		if n := audited(saID); n != auditBefore {
			t.Errorf("roles_changed audit entries %d after the refusal, want %d", n, auditBefore)
		}
	}

	t.Run("org admin assigns engineer organization-wide", func(t *testing.T) {
		sa := newSA(f.OrgA, "sa-ceiling-org")
		if w := call(orgAdmin, rolesPath(sa), orgWide); w.Code != http.StatusOK {
			t.Fatalf("status %d %s, want 200", w.Code, w.Body.String())
		}
		if got := assignments(sa); got != engineerOrgWide || audited(sa) != 1 {
			t.Errorf("assignments %q, audit %d; want %q and one roles_changed entry", got, audited(sa), engineerOrgWide)
		}
	})

	t.Run("client admin within and beyond client 1", func(t *testing.T) {
		sa := newSA(f.OrgA, "sa-ceiling-client")
		if w := call(clientAdmin, rolesPath(sa), inClient(f.Client1)); w.Code != http.StatusOK {
			t.Fatalf("engineer in client 1: %d %s, want 200", w.Code, w.Body.String())
		}
		if got := assignments(sa); got != engineerClient1 {
			t.Fatalf("assignments %q, want %q", got, engineerClient1)
		}
		before, auditBefore := assignments(sa), audited(sa)
		refused(t, call(clientAdmin, rolesPath(sa), orgWide), sa, before, auditBefore, "ci:read")
		refused(t, call(clientAdmin, rolesPath(sa), inClient(f.Client2)), sa, before, auditBefore, "ci:read")
		// Keeping the covered assignment does not admit an uncovered one.
		refused(t, call(clientAdmin, rolesPath(sa), `{"roles":[{"role_id":"`+engineer+`","scope_client_id":"`+f.Client1+`"},{"role_id":"`+engineer+`"}]}`),
			sa, before, auditBefore, "ci:read")
	})

	t.Run("removing an organization-wide assignment", func(t *testing.T) {
		sa := newSA(f.OrgA, "sa-ceiling-removal")
		if w := call(orgAdmin, rolesPath(sa), orgWide); w.Code != http.StatusOK {
			t.Fatalf("setup: %d %s", w.Code, w.Body.String())
		}
		before, auditBefore := assignments(sa), audited(sa)
		refused(t, call(clientAdmin, rolesPath(sa), `{"roles":[]}`), sa, before, auditBefore, engineer, "organization-wide", "ci:read")
		// Replacing it by a covered client-1 assignment is a removal as well.
		refused(t, call(clientAdmin, rolesPath(sa), inClient(f.Client1)), sa, before, auditBefore, engineer, "organization-wide")
		if w := call(orgAdmin, rolesPath(sa), `{"roles":[]}`); w.Code != http.StatusOK {
			t.Fatalf("org admin removes the assignment: %d %s, want 200", w.Code, w.Body.String())
		}
		if got := assignments(sa); got != "" || audited(sa) != auditBefore+1 {
			t.Errorf("assignments %q, audit %d after the org admin's removal; want none and %d", got, audited(sa), auditBefore+1)
		}
	})

	t.Run("caller without role:manage", func(t *testing.T) {
		sa := newSA(f.OrgA, "sa-ceiling-no-role-manage")
		refused(t, call(userManager, rolesPath(sa), orgWide), sa, "", 0, "role:manage")
	})

	t.Run("service account of another organization", func(t *testing.T) {
		saB := newSA(f.OrgB, "sa-ceiling-org-b")
		if w := call(orgAdmin, rolesPath(saB), orgWide); w.Code != http.StatusNotFound {
			t.Errorf("status %d %s, want 404", w.Code, w.Body.String())
		}
		if got := assignments(saB); got != "" {
			t.Errorf("org B account assignments %q, want none", got)
		}
	})
}
