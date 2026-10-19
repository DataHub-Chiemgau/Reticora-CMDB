//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (it fails until
// the product is fixed).
//
// WP-034 (SRC-02, VIEW-01, RBA-01; AC1 "Strukturierte Suche prüft das
// Leserecht der Entität") P2 "Structured search (POST /api/v1/search/query)
// requires search:write; read-only roles get 403": server/authz.go
// writePermissionOverrides["search"] = search:write applies to every
// non-GET /api/v1/search/* route and there is no route rule for
// search/query, so viewers and client technicians (ci:read,
// saved_view:read) see saved views but cannot run them. The index rebuild
// /api/v1/search/reindex must keep requiring search:write.
//
// The request runs through the complete PostgreSQL router with the
// permissions of the standard roles (permission.StandardRolePermissions).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1StructuredSearchForReadRoles' ./internal/tenant/rls/

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

func TestAuditT1StructuredSearchForReadRoles(t *testing.T) {
	w := newT1World(t, "ea")
	f := w.f
	own := f.CI(t, f.OrgA, f.Client1, "t1-sq-ci")
	call := func(role, path, body string) (int, string) {
		t.Helper()
		keys, ok := permission.StandardRolePermissions(role)
		if !ok {
			t.Fatalf("standard role %s missing", role)
		}
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		scope := identity.Scope{Clients: all(), Sites: all(), Teams: all()}
		principal := identity.Principal{Subject: w.userA, OrganizationID: f.OrgA, Scope: &scope, Type: identity.PrincipalTypeUser,
			Permissions: perms, Credential: "t1-" + role}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), perms)
		rec := httptest.NewRecorder()
		w.env.router.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code, rec.Body.String()
	}
	const query = `{"entity_kind":"ci"}`

	// Positive control: a role holding search:write runs the query and finds
	// the CI.
	if code, body := call("engineer", "/api/v1/search/query", query); code != http.StatusOK || !strings.Contains(body, own) {
		t.Fatalf("control: engineer POST /search/query: %d %s, want 200 with the CI", code, trim(body))
	}
	for _, role := range []string{"viewer", "client_technician"} {
		t.Run(role, func(t *testing.T) {
			if code, body := call(role, "/api/v1/search/query", query); code != http.StatusOK || !strings.Contains(body, own) {
				t.Errorf("WP-034 AC1: %s (holds ci:read) POST /search/query {entity_kind: ci}: %d %s, want 200 with the CI", role, code, trim(body))
			}
			// Regression guard: the index rebuild stays a write operation.
			if code, _ := call(role, "/api/v1/search/reindex", `{}`); code != http.StatusForbidden {
				t.Errorf("%s POST /search/reindex: %d, want 403", role, code)
			}
		})
	}
}
