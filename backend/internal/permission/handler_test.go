package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
	var catalogue struct {
		Data       []Permission `json:"data"`
		Total      int          `json:"total"`
		NextCursor string       `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalogue); err != nil || len(catalogue.Data) == 0 {
		t.Fatalf("expected permission catalogue, err=%v", err)
	}
	// The catalogue is a cursor page (API-02, F-075): limit=1 answers one
	// entry and next_cursor, and the cursors lead through every entry once.
	seen := map[string]bool{}
	for path := "/api/v1/permissions?limit=1"; ; {
		w = request(mux, http.MethodGet, path, "", "org-1", "user-1")
		var page struct {
			Data       []Permission `json:"data"`
			NextCursor string       `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK || len(page.Data) != 1 {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if seen[page.Data[0].Key] {
			t.Fatalf("GET %s: %s returned twice", path, page.Data[0].Key)
		}
		seen[page.Data[0].Key] = true
		if page.NextCursor == "" {
			break
		}
		path = "/api/v1/permissions?limit=1&cursor=" + page.NextCursor
	}
	if len(seen) != catalogue.Total || catalogue.Total < 2 {
		t.Fatalf("paged through %d of %d permissions", len(seen), catalogue.Total)
	}
	// A forged cursor and a keyset cursor, which this list never issues,
	// are 400.
	keyset := api.EncodeCursor(api.Cursor{Sort: "key", Dir: "asc", Value: "ci:read", ID: "x"})
	for _, cursor := range []string{"eyJvIjoxfQ.AAAAAAAAAAAAAAAAAAAAAA", keyset} {
		if w = request(mux, http.MethodGet, "/api/v1/permissions?cursor="+cursor, "", "org-1", "user-1"); w.Code != http.StatusBadRequest {
			t.Fatalf("cursor %s: expected 400, got %d %s", cursor, w.Code, w.Body.String())
		}
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

// TestEffectivePermissionsIncludeSessionGrants: GET /me/permissions of a user
// session lists the rights the session holds through its IdP groups (RBA-02,
// F-200), also without any role assignment, merged with the assignments and
// without duplicates; an API key reads only the assignments of its owner.
func TestEffectivePermissionsIncludeSessionGrants(t *testing.T) {
	repo, mux := setupPermission()
	effective := func(p identity.Principal) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/me/permissions", nil)
		ctx := identity.WithPrincipal(req.Context(), p)
		req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: "org-1", UserID: p.Subject}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /me/permissions: %d %s", w.Code, w.Body.String())
		}
		var body EffectivePermissionsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Permissions
	}

	groupOnly := identity.Principal{Subject: "group-viewer", OrganizationID: "org-1", Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{"ticket:read", "ci:read"}}
	if got := effective(groupOnly); !slices.Equal(got, []string{"ci:read", "ticket:read"}) {
		t.Fatalf("group member without role assignment: %v, want the rights of the session", got)
	}

	if _, err := repo.ReplaceRolePermissions(context.Background(), "org-1", "writer-role", "", orgWide, []string{"ci:read", "ticket:write"}); err != nil {
		t.Fatal(err)
	}
	repo.AssignRoleToUser("org-1", "both", "writer-role")
	both := identity.Principal{Subject: "both", OrganizationID: "org-1", Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{"ci:read", "ticket:read"}}
	if got := effective(both); !slices.Equal(got, []string{"ci:read", "ticket:read", "ticket:write"}) {
		t.Fatalf("assignment and group: %v, want the union without duplicates", got)
	}

	repo.AssignRoleToUser("org-1", "key-owner", "writer-role")
	key := identity.Principal{Subject: "key-owner", OrganizationID: "org-1", Type: identity.PrincipalTypeAPIKey,
		Permissions: []identity.Permission{"ci:read", "admin:manage"}}
	if got := effective(key); !slices.Equal(got, []string{"ci:read", "ticket:write"}) {
		t.Fatalf("API key: %v, want only the assignments of its owner", got)
	}
}
