package permission_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestPermissionRepositoryScope runs the permission repository with the
// principal's tenant scope (TEN-06, WP-012). Request paths need the
// principal's scope and its organization; resolving the access of a login or
// refresh (AccessGrants) runs before a principal exists and reads the user's
// own assignments org-wide.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPermissionRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "1b")
	ctx := context.Background()
	var userID string
	if err := f.Admin.QueryRow(ctx,
		`INSERT INTO app_user (organization_id, oidc_subject, email, display_name) VALUES ($1, 'scopetest-1b', 'p@scopetest.invalid', 'P') RETURNING id::text`,
		f.OrgA).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := f.Admin.Exec(ctx,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		 SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'viewer'`,
		f.OrgA, userID, f.Client1); err != nil {
		t.Fatalf("assign client-scoped role: %v", err)
	}

	repo := permission.NewPGRepository(f.App)
	grants, err := repo.AccessGrants(ctx, f.OrgA, userID)
	if err != nil {
		t.Fatalf("access grants without request scope: %v", err)
	}
	if len(grants) != 1 || len(grants[0].Clients) != 1 || grants[0].Clients[0] != f.Client1 {
		t.Fatalf("grants: %+v, want one grant scoped to client 1", grants)
	}

	reqCtx := f.ClientCtx(f.Client1)
	keys, err := repo.EffectivePermissions(reqCtx, f.OrgA, userID)
	if err != nil || len(keys) == 0 {
		t.Fatalf("effective permissions: %v err=%v", keys, err)
	}
	if keys, err = repo.EffectivePermissions(reqCtx, f.OrgB, userID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("permissions in organization B: %v err=%v, want ErrTenantMismatch", keys, err)
	}
	catalogue, err := repo.ListPermissions(reqCtx)
	if err != nil || len(catalogue) == 0 {
		t.Fatalf("permission catalogue: %d entries err=%v", len(catalogue), err)
	}
	if _, err = repo.ListPermissions(ctx); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("catalogue without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestRoleDefinitionWideningCeiling covers the role definitions of WP-394
// (RBA-03, F-015): a role applies to all its assignments, org-wide ones too,
// so PUT /roles/{id}/permissions (standard role) and PATCH /roles/{id} with
// permissions (custom role) may add only permissions the caller holds
// organization-wide. A client-1 delegate (org_admin with scope_client_id =
// client 1, role:manage there only) adding role:manage to viewer or to a
// custom role gets 403 (RFC 7807), the permissions stay unchanged and nothing
// is audited; replacing viewer's permissions with the same set is 200. The
// org-wide org_admin adds role:manage to both (200).
func TestRoleDefinitionWideningCeiling(t *testing.T) {
	f := scopetest.Seed(t, "f5")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var adminRole, viewerRole string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA).Scan(&adminRole); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA).Scan(&viewerRole); err != nil {
		t.Fatal(err)
	}
	customRole := f.ID()
	exec(`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ($1, $2, 'widening-custom', '["ci:read"]')`, customRole, f.OrgA)
	t.Cleanup(func() {
		// custom_role has no ON DELETE CASCADE to organization.
		_, _ = f.Admin.Exec(bg, `DELETE FROM custom_role WHERE id = $1`, customRole)
	})
	delegate := f.AppUser(t, f.OrgA, "widening-delegate")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, delegate, adminRole, f.Client1)
	orgAdmin := f.AppUser(t, f.OrgA, "widening-orgadmin")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, orgAdmin, adminRole)

	repo := permission.NewPGRepository(f.App)
	as := func(userID string) context.Context {
		t.Helper()
		grants, err := repo.AccessGrants(bg, f.OrgA, userID)
		if err != nil {
			t.Fatal(err)
		}
		access := identity.ResolveAccess(grants)
		p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Permissions: access.Permissions,
			Scope: &access.Scope, PermissionScopes: access.PermissionScopes, ClientScope: access.Scope.LegacyClientScope()}
		scope := middleware.TenantScopeFor(&p)
		ctx := database.ContextWithTenantScope(identity.WithPrincipal(bg, p), &scope)
		return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userID, ClientID: p.ClientScope})
	}
	delegateCtx, orgAdminCtx := as(delegate), as(orgAdmin)

	permissions := chi.NewRouter()
	permission.NewHandler(repo).RegisterRoutes(permissions)
	roles := chi.NewRouter()
	user.NewHandler(user.NewPGRepository(f.App)).RegisterRoutes(roles)
	call := func(mux http.Handler, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	forbidden := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusForbidden && strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json")
	}
	viewerKeys := func() []string {
		t.Helper()
		var keys []string
		if err := f.Admin.QueryRow(bg, `SELECT ARRAY(SELECT permission_key FROM role_permission WHERE role_id = $1 ORDER BY 1)`, viewerRole).Scan(&keys); err != nil {
			t.Fatal(err)
		}
		return keys
	}
	customKeys := func() string {
		t.Helper()
		var keys string
		if err := f.Admin.QueryRow(bg, `SELECT permissions::text FROM custom_role WHERE id = $1`, customRole).Scan(&keys); err != nil {
			t.Fatal(err)
		}
		return keys
	}
	audits := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*)::int FROM audit_log WHERE organization_id = $1`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	keysBody := func(keys []string) string {
		b, err := json.Marshal(map[string]any{"permission_keys": keys})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	original := viewerKeys()
	if len(original) == 0 || slices.Contains(original, "role:manage") {
		t.Fatalf("fixture: viewer permissions %v", original)
	}
	widened := append(slices.Clone(original), "role:manage")
	put := "/api/v1/roles/" + viewerRole + "/permissions"

	before := audits()
	if w := call(permissions, delegateCtx, http.MethodPut, put, keysBody(widened)); !forbidden(w) || !slices.Equal(viewerKeys(), original) || audits() != before {
		t.Errorf("delegate widens viewer: %d, permissions %v, %d new audit entries; want 403, %v and none; body %.200s",
			w.Code, viewerKeys(), audits()-before, original, w.Body.String())
	}
	if w := call(permissions, delegateCtx, http.MethodPut, put, keysBody(original)); w.Code != http.StatusOK || !slices.Equal(viewerKeys(), original) {
		t.Errorf("delegate replaces viewer with the same permissions: %d %.200s, want 200", w.Code, w.Body.String())
	}
	before = audits()
	if w := call(roles, delegateCtx, http.MethodPatch, "/api/v1/roles/"+customRole, `{"permissions":["ci:read","role:manage"]}`); !forbidden(w) ||
		customKeys() != `["ci:read"]` || audits() != before {
		t.Errorf("delegate widens the custom role: %d, permissions %s, %d new audit entries; want 403 and unchanged; body %.200s",
			w.Code, customKeys(), audits()-before, w.Body.String())
	}

	if w := call(permissions, orgAdminCtx, http.MethodPut, put, keysBody(widened)); w.Code != http.StatusOK || !slices.Contains(viewerKeys(), "role:manage") {
		t.Errorf("org-wide org_admin widens viewer: %d %.200s, want 200 with role:manage", w.Code, w.Body.String())
	}
	if w := call(roles, orgAdminCtx, http.MethodPatch, "/api/v1/roles/"+customRole, `{"permissions":["ci:read","role:manage"]}`); w.Code != http.StatusOK ||
		customKeys() != `["ci:read", "role:manage"]` {
		t.Errorf("org-wide org_admin widens the custom role: %d %.200s (permissions %s), want 200 with role:manage", w.Code, w.Body.String(), customKeys())
	}
}
