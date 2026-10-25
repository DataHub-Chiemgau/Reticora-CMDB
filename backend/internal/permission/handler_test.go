package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// orgWide is a caller holding every permission organization-wide; it seeds
// role permissions past the widening ceiling.
var orgWide = &identity.Principal{Permissions: identity.AllPermissions()}

// roleID is the standard role of the requests; the handler accepts UUIDs
// only (API-03).
const roleID = "00000000-0000-4000-8000-0000000000a1"

// request sends a request of userID in orgID as an org-wide principal with
// every permission (the authorization middleware is not part of the mux).
func request(mux chi.Router, method, path, body, orgID, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if orgID != "" {
		ctx := identity.WithPrincipal(req.Context(), identity.Principal{Subject: userID, OrganizationID: orgID, Permissions: identity.AllPermissions()})
		req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: orgID, UserID: userID}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func setupPermission() (*MemoryRepository, chi.Router) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return repo, mux
}

func TestPermissionCatalogueAndRoleGrants(t *testing.T) {
	repo, mux := setupPermission()
	if _, err := repo.ReplaceRolePermissions(context.Background(), "org-1", "admin-role", "", orgWide, []string{"role:manage"}); err != nil {
		t.Fatal(err)
	}
	repo.AssignRoleToUser("org-1", "admin", "admin-role")
	w := request(mux, http.MethodGet, "/api/v1/permissions", "", "org-1", "user-1")
	if w.Code != http.StatusOK {
		t.Fatalf("list catalogue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var catalogue []Permission
	if err := json.Unmarshal(w.Body.Bytes(), &catalogue); err != nil || len(catalogue) == 0 {
		t.Fatalf("expected permission catalogue, err=%v", err)
	}

	w = request(mux, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", `{"permission_keys":["ci:read","ticket:write"]}`, "org-1", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("replace grants: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	repo.AssignRoleToUser("org-1", "user-1", roleID)

	w = request(mux, http.MethodGet, "/api/v1/me/permissions", "", "org-1", "user-1")
	var effective EffectivePermissionsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &effective); err != nil {
		t.Fatal(err)
	}
	if len(effective.Permissions) != 2 || effective.Permissions[0] != "ci:read" || effective.Permissions[1] != "ticket:write" {
		t.Fatalf("unexpected effective permissions: %#v", effective.Permissions)
	}
}

func TestPermissionValidationAndTenant(t *testing.T) {
	repo, mux := setupPermission()
	if _, err := repo.ReplaceRolePermissions(context.Background(), "org-1", "admin-role", "", orgWide, []string{"role:manage"}); err != nil {
		t.Fatal(err)
	}
	repo.AssignRoleToUser("org-1", "admin", "admin-role")
	if w := request(mux, http.MethodGet, "/api/v1/permissions", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if w := request(mux, http.MethodPut, "/api/v1/roles/"+roleID+"/permissions", `{"permission_keys":["missing:key"]}`, "org-1", "admin"); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), `"field":"permission_keys"`) {
		t.Fatalf("unknown permission: expected 400 naming permission_keys, got %d %s", w.Code, w.Body.String())
	}
	// A role id that is no UUID names no role: 404 before any SQL (F-047).
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if w := request(mux, method, "/api/v1/roles/not-a-uuid/permissions", `{"permission_keys":["ci:read"]}`, "org-1", "admin"); w.Code != http.StatusNotFound ||
			!strings.Contains(w.Body.String(), "role not found") {
			t.Fatalf("%s with a malformed role id: expected 404 role not found, got %d %s", method, w.Code, w.Body.String())
		}
	}
}
