package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func request(mux chi.Router, method, path, body, orgID, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if orgID != "" {
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: orgID, UserID: userID}))
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
	if _, err := repo.ReplaceRolePermissions(context.Background(), "org-1", "admin-role", "", []string{"permission:write"}); err != nil {
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

	w = request(mux, http.MethodPut, "/api/v1/roles/role-1/permissions", `{"permission_keys":["ci:read","ticket:write"]}`, "org-1", "admin")
	if w.Code != http.StatusOK {
		t.Fatalf("replace grants: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	repo.AssignRoleToUser("org-1", "user-1", "role-1")

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
	if _, err := repo.ReplaceRolePermissions(context.Background(), "org-1", "admin-role", "", []string{"permission:write"}); err != nil {
		t.Fatal(err)
	}
	repo.AssignRoleToUser("org-1", "admin", "admin-role")
	if w := request(mux, http.MethodGet, "/api/v1/permissions", "", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if w := request(mux, http.MethodPut, "/api/v1/roles/role-1/permissions", `{"permission_keys":["missing:key"]}`, "org-1", "admin"); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission: expected 400, got %d", w.Code)
	}
}
