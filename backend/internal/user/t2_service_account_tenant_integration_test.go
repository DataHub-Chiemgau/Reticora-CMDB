// Phase-1 acceptance audit, group T2-auth-authz: regression test of the
// tenant isolation of the service-accounts resource against PostgreSQL.
//
//   - WP-069 (RBA-08, TEN tenant isolation), gap (high) "Tenant isolation of
//     the service-accounts REST resource": an organization B administrator
//     gets 404 for GET, PATCH, DELETE and PUT roles on an organization A
//     account, the list shows only the own organization's accounts, and an
//     organization B key cannot be bound to an organization A account; the
//     organization A account stays unchanged.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -count=1 -v -run 'TestT2ServiceAccountTenantIsolation' ./internal/user/
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

func TestT2ServiceAccountTenantIsolation(t *testing.T) {
	f := scopetest.Seed(t, "ef")
	bg := context.Background()
	adminA := f.AppUser(t, f.OrgA, "t2-sa-admin-a")
	adminB := f.AppUser(t, f.OrgB, "t2-sa-admin-b")
	repo := user.NewPGServiceAccounts(f.App)
	mux := chi.NewRouter()
	user.NewServiceAccountHandler(repo).RegisterRoutes(mux)
	identity.NewAPIKeyHandler(identity.NewPGAPIKeyStore(f.App)).RegisterRoutes(mux)
	call := func(orgID, userID, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: userID, OrganizationID: orgID, Type: identity.PrincipalTypeUser,
			Permissions: []identity.Permission{identity.PermUserManage, identity.PermUserRead, identity.PermRoleManage, identity.PermAPIKeyManage, identity.PermCIRead}}
		scope := database.OrgWideScope(orgID, userID)
		ctx := identity.WithPrincipal(req.Context(), p)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID, UserID: userID})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}

	w := call(f.OrgA, adminA, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-org-a","description":"org A"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create org A account: %d %s", w.Code, w.Body.String())
	}
	var saA user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &saA); err != nil {
		t.Fatal(err)
	}
	roleB := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-sa-role-b', '["ci:read"]')`, roleB, f.OrgB); err != nil {
		t.Fatal(err)
	}
	if bw := call(f.OrgB, adminB, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-org-b"}`); bw.Code != http.StatusCreated {
		t.Fatalf("create org B account: %d %s", bw.Code, bw.Body.String())
	}

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/service-accounts/" + saA.ID, ""},
		{http.MethodPatch, "/api/v1/service-accounts/" + saA.ID, `{"name":"t2-hijacked","is_active":false}`},
		{http.MethodPut, "/api/v1/service-accounts/" + saA.ID + "/roles", `{"roles":[{"role_id":"` + roleB + `"}]}`},
		{http.MethodDelete, "/api/v1/service-accounts/" + saA.ID, ""},
	} {
		if bw := call(f.OrgB, adminB, c.method, c.path, c.body); bw.Code != http.StatusNotFound {
			t.Errorf("org B %s %s on an org A account: %d %s, want 404", c.method, c.path, bw.Code, bw.Body.String())
		}
	}

	w = call(f.OrgB, adminB, http.MethodGet, "/api/v1/service-accounts", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), saA.ID) || !strings.Contains(w.Body.String(), "t2-sa-org-b") {
		t.Errorf("org B list: %d %s, want only org B accounts", w.Code, w.Body.String())
	}

	w = call(f.OrgB, adminB, http.MethodPost, "/api/v1/api-keys", `{"name":"t2-cross-bind","permissions":["ci:read"],"service_account_id":"`+saA.ID+`"}`)
	if w.Code < 400 || w.Code >= 500 {
		t.Errorf("org B key bound to an org A account: %d %s, want a 4xx refusal", w.Code, w.Body.String())
	}

	// The org A account is unchanged.
	w = call(f.OrgA, adminA, http.MethodGet, "/api/v1/service-accounts/"+saA.ID, "")
	var after user.ServiceAccount
	_ = json.Unmarshal(w.Body.Bytes(), &after)
	if w.Code != http.StatusOK || after.Name != "t2-sa-org-a" || !after.IsActive || len(after.Roles) != 0 {
		t.Errorf("org A account after the org B attempts: %d %s, want unchanged", w.Code, w.Body.String())
	}
}
