package permission_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// standardRoleProblem is the problem type of a refused removal of a seed
// permission (api/openapi.yaml, replaceRolePermissions 409).
const standardRoleProblem = "https://reticora.io/problems/standard-role-permissions"

// TestReplaceRolePermissionsOfStandardRoleTakesEffectOrIsRefused covers
// WP-669 (RBA-02, RBA-03, AUD-07; finding F-398): the seed permissions of a
// standard role (role.permissions, the RBA-02 matrix) stay effective on
// every grant path, so PUT /roles/{id}/permissions without one of them is
// refused with 409 (problem type standard-role-permissions, naming the
// keys) and nothing changes; even the org-wide org_admin cannot remove one.
// Adding and removing further keys takes effect. After every request the
// response of the PUT, GET /roles/{id}/permissions and the route-audit
// snapshot of the PUT name exactly the permissions AccessGrants resolves for
// an org-wide holder of the role, also when role_permission lost seed rows
// (as a replacement before WP-669 left it).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestReplaceRolePermissionsOfStandardRoleTakesEffectOrIsRefused$' ./internal/permission/
func TestReplaceRolePermissionsOfStandardRoleTakesEffectOrIsRefused(t *testing.T) {
	f := scopetest.Seed(t, "7b")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	adminRole := str(`SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA)
	viewerRole := str(`SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA)
	orgAdmin := f.AppUser(t, f.OrgA, "seed-orgadmin")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, orgAdmin, adminRole)
	holder := f.AppUser(t, f.OrgA, "seed-viewer")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, holder, viewerRole)

	repo := permission.NewPGRepository(f.App)
	grants, err := repo.AccessGrants(bg, f.OrgA, orgAdmin)
	if err != nil {
		t.Fatal(err)
	}
	access := identity.ResolveAccess(grants)
	p := identity.Principal{Subject: orgAdmin, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Permissions: access.Permissions,
		Scope: &access.Scope, PermissionScopes: access.PermissionScopes, ClientScope: access.Scope.LegacyClientScope()}
	scope := middleware.TenantScopeFor(&p)
	adminCtx := tenant.WithTenant(database.ContextWithTenantScope(identity.WithPrincipal(bg, p), &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: orgAdmin})

	mux := chi.NewRouter()
	permission.NewHandler(repo).RegisterRoutes(mux)
	path := "/api/v1/roles/" + viewerRole + "/permissions"
	call := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(adminCtx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	put := func(keys []string) *httptest.ResponseRecorder {
		b, err := json.Marshal(map[string]any{"permission_keys": keys})
		if err != nil {
			t.Fatal(err)
		}
		return call(http.MethodPut, string(b))
	}
	grantKeys := func(body []byte) []string {
		var items []permission.RolePermissionGrant
		if err := json.Unmarshal(body, &items); err != nil {
			var page struct {
				Data []permission.RolePermissionGrant `json:"data"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}
			items = page.Data
		}
		keys := make([]string, 0, len(items))
		for _, it := range items {
			keys = append(keys, it.PermissionKey)
		}
		slices.Sort(keys)
		return keys
	}
	effective := func() []string {
		t.Helper()
		g, err := repo.AccessGrants(bg, f.OrgA, holder)
		if err != nil || len(g) != 1 {
			t.Fatalf("access grants of the viewer: %+v %v", g, err)
		}
		keys := make([]string, 0, len(g[0].Permissions))
		for _, perm := range g[0].Permissions {
			keys = append(keys, string(perm))
		}
		slices.Sort(keys)
		return keys
	}
	spec, ok := audit.RouteSnapshotFor("PUT /api/v1/roles/{id}/permissions")
	if !ok {
		t.Fatal("no route snapshot for PUT /api/v1/roles/{id}/permissions")
	}
	snapshot := func() []string {
		t.Helper()
		row, _, err := audit.NewPGRouteAuditor(f.App).RouteSnapshot(adminCtx, f.OrgA, &spec, []any{viewerRole})
		if err != nil || row == nil {
			t.Fatalf("route snapshot: %v %v", row, err)
		}
		raw, _ := row["permission_keys"].([]any)
		keys := make([]string, 0, len(raw))
		for _, k := range raw {
			keys = append(keys, k.(string))
		}
		slices.Sort(keys)
		return keys
	}
	listed := func() []string {
		t.Helper()
		w := call(http.MethodGet, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %.200s", path, w.Code, w.Body.String())
		}
		return grantKeys(w.Body.Bytes())
	}
	jsonbSeed := func() string {
		return str(`SELECT permissions::text FROM role WHERE id = $1`, viewerRole)
	}
	// consistent: GET, the route snapshot and (for a 200) the response name
	// the effective permissions.
	consistent := func(name string, w *httptest.ResponseRecorder, want []string) {
		t.Helper()
		eff := effective()
		if !slices.Equal(eff, want) {
			t.Errorf("%s: effective permissions of an org-wide viewer %v, want %v", name, eff, want)
		}
		if got := listed(); !slices.Equal(got, eff) {
			t.Errorf("%s: GET /roles/{viewer}/permissions %v, effective %v", name, got, eff)
		}
		if got := snapshot(); !slices.Equal(got, eff) {
			t.Errorf("%s: audit snapshot permission_keys %v, effective %v", name, got, eff)
		}
		if w != nil && w.Code == http.StatusOK {
			if got := grantKeys(w.Body.Bytes()); !slices.Equal(got, eff) {
				t.Errorf("%s: PUT response %v, effective %v", name, got, eff)
			}
		}
	}

	seed, ok := permission.StandardRolePermissions("viewer")
	if !ok {
		t.Fatal("no seed for viewer")
	}
	slices.Sort(seed)
	seedJSON := jsonbSeed()
	consistent("fixture", nil, seed)

	// Removing seed permissions is refused, even for the org-wide admin.
	for _, c := range []struct {
		name    string
		keys    []string
		missing string
	}{
		{"PUT []", []string{}, "agent:read"},
		{"PUT seed without location:read", slices.DeleteFunc(slices.Clone(seed), func(k string) bool { return k == "location:read" }), "location:read"},
	} {
		w := put(c.keys)
		var prob api.ProblemDetail
		_ = json.Unmarshal(w.Body.Bytes(), &prob)
		if w.Code != http.StatusConflict || prob.Type != standardRoleProblem ||
			!strings.Contains(prob.Detail, c.missing) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("admin %s = %d %.240s; want 409 %s naming %s", c.name, w.Code, w.Body.String(), standardRoleProblem, c.missing)
		}
		if jsonbSeed() != seedJSON {
			t.Errorf("admin %s changed role.permissions to %s", c.name, jsonbSeed())
		}
		consistent("admin "+c.name, w, seed)
	}

	// Adding and removing a further key takes effect.
	widened := append(slices.Clone(seed), "location:write")
	slices.Sort(widened)
	w := put(widened)
	if w.Code != http.StatusOK {
		t.Errorf("admin PUT seed + location:write = %d %.200s, want 200", w.Code, w.Body.String())
	}
	consistent("admin PUT seed + location:write", w, widened)
	w = put(seed)
	if w.Code != http.StatusOK {
		t.Errorf("admin PUT seed (removes location:write) = %d %.200s, want 200", w.Code, w.Body.String())
	}
	consistent("admin PUT seed", w, seed)

	// role_permission without seed rows (left by a replacement before the
	// fix): every reader still names the effective seed keys, and the next
	// replacement restores the rows.
	exec(`DELETE FROM role_permission WHERE organization_id = $1 AND role_id = $2`, f.OrgA, viewerRole)
	consistent("role_permission without seed rows", nil, seed)
	w = put(seed)
	if w.Code != http.StatusOK || str(`SELECT count(*)::text FROM role_permission WHERE role_id = $1`, viewerRole) != str(`SELECT $1::int::text`, len(seed)) {
		t.Errorf("admin PUT seed after the loss = %d %.200s, want 200 with %d rows", w.Code, w.Body.String(), len(seed))
	}
	consistent("admin PUT seed after the loss", w, seed)
}
