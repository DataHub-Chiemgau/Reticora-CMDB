package rls_test

// WP-460 (F-055, SRC-03; WP-034 AC1 "Strukturierte Suche prüft das
// Leserecht der Entität"): the structured search POST /api/v1/search/query
// needs search:read, not search:write, so viewers and client technicians
// (ci:read, saved_view:read, search:read) run saved views. The handler
// still requires the read permission of the queried entity kind, and the
// index rebuild /api/v1/search/reindex keeps requiring search:write.
//
// The request runs through the complete PostgreSQL router with the
// permissions of the standard roles (permission.StandardRolePermissions).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^TestStructuredSearchForReadRoles$' ./internal/tenant/rls/

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

func TestStructuredSearchForReadRoles(t *testing.T) {
	w := newT1World(t, "ea")
	f := w.f
	own := f.CI(t, f.OrgA, f.Client1, "t1-sq-ci")
	asset := f.Asset(t, f.OrgA, f.Client1, "t1-sq-asset")
	call := func(name string, keys []string, path, body string) (int, string) {
		t.Helper()
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		scope := identity.Scope{Clients: all(), Sites: all(), Teams: all()}
		principal := identity.Principal{Subject: w.userA, OrganizationID: f.OrgA, Scope: &scope, Type: identity.PrincipalTypeUser,
			Permissions: perms, Credential: "t1-" + name}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), perms)
		rec := httptest.NewRecorder()
		w.env.router.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code, rec.Body.String()
	}
	role := func(name string) []string {
		t.Helper()
		keys, ok := permission.StandardRolePermissions(name)
		if !ok {
			t.Fatalf("standard role %s missing", name)
		}
		return keys
	}
	const ciQuery, assetQuery = `{"entity_kind":"ci"}`, `{"entity_kind":"asset"}`

	// Positive control: a role holding search:write runs the query and finds
	// the CI.
	if code, body := call("engineer", role("engineer"), "/api/v1/search/query", ciQuery); code != http.StatusOK || !strings.Contains(body, own) {
		t.Fatalf("control: engineer POST /search/query: %d %s, want 200 with the CI", code, trim(body))
	}
	for _, name := range []string{"viewer", "client_technician"} {
		t.Run(name, func(t *testing.T) {
			keys := role(name)
			if code, body := call(name, keys, "/api/v1/search/query", ciQuery); code != http.StatusOK || !strings.Contains(body, own) {
				t.Errorf("WP-034 AC1: %s (holds ci:read) POST /search/query {entity_kind: ci}: %d %s, want 200 with the CI", name, code, trim(body))
			}
			if code, body := call(name, keys, "/api/v1/search/query", assetQuery); code != http.StatusOK || !strings.Contains(body, asset) {
				t.Errorf("%s (holds asset:read) POST /search/query {entity_kind: asset}: %d %s, want 200 with the asset", name, code, trim(body))
			}
			// Regression guard: the index rebuild stays a write operation.
			if code, _ := call(name, keys, "/api/v1/search/reindex", `{}`); code != http.StatusForbidden {
				t.Errorf("%s POST /search/reindex: %d, want 403", name, code)
			}
		})
	}

	// The entity read right stays required (fail-closed): search:read and
	// ci:read without asset:read run CI queries but no asset query, although
	// the user's roles in the database would grant it.
	ciOnly := []string{"ci:read", "saved_view:read", "search:read"}
	if code, body := call("ci-only", ciOnly, "/api/v1/search/query", ciQuery); code != http.StatusOK || !strings.Contains(body, own) {
		t.Errorf("ci-only POST /search/query {entity_kind: ci}: %d %s, want 200 with the CI", code, trim(body))
	}
	if code, body := call("ci-only", ciOnly, "/api/v1/search/query", assetQuery); code != http.StatusForbidden || strings.Contains(body, asset) {
		t.Errorf("ci-only (no asset:read) POST /search/query {entity_kind: asset}: %d %s, want 403", code, trim(body))
	}
	// search:read alone reads no entity kind, and the route needs search:read.
	if code, body := call("search-only", []string{"search:read"}, "/api/v1/search/query", ciQuery); code != http.StatusForbidden || strings.Contains(body, own) {
		t.Errorf("search:read only POST /search/query {entity_kind: ci}: %d %s, want 403", code, trim(body))
	}
	if code, _ := call("no-search", []string{"ci:read", "asset:read", "saved_view:read"}, "/api/v1/search/query", ciQuery); code != http.StatusForbidden {
		t.Errorf("without search:read POST /search/query: %d, want 403", code)
	}
	if code, _ := call("ci-only", ciOnly, "/api/v1/search/reindex", `{}`); code != http.StatusForbidden {
		t.Errorf("without search:write POST /search/reindex: %d, want 403", code)
	}
}
