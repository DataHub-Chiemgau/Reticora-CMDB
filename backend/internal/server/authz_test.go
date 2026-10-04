package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// routeTable returns every route registered on the router together with its
// method.
func routeTable(t *testing.T, mux *chi.Mux) []speccheckOp {
	t.Helper()

	var ops []speccheckOp
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		ops = append(ops, speccheckOp{Method: method, Path: route})
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return ops
}

type speccheckOp struct {
	Method string
	Path   string
}

// TestEveryRouteHasAPermissionMapping fails the build when a route is
// registered without a permission mapping, so authorization cannot be
// forgotten when new endpoints are added.
func TestEveryRouteHasAPermissionMapping(t *testing.T) {
	mux := testRouter(t)

	for _, op := range routeTable(t, mux) {
		if _, access := PermissionForRoute(op.Method, op.Path); access == routeUnmapped {
			t.Errorf("route %s %s has no permission mapping and is not explicitly public", op.Method, op.Path)
		}
	}
}

// TestAuthorizationMiddlewareEnforcesPermissions is a table-driven negative-
// and positive-path suite over every route: no principal yields 401, a
// principal without the mapped permission yields 403, and a principal holding
// the mapped permission is allowed through.
func TestAuthorizationMiddlewareEnforcesPermissions(t *testing.T) {
	mux := testRouter(t)
	// Mirror the production chain: auth populates the principal (injected
	// directly by the test), tenant resolves the organization from it, and
	// the router-mounted authorization middleware enforces the mapped
	// permission.
	handler := middleware.TenantMiddleware(mux)

	for _, op := range routeTable(t, mux) {
		required, access := PermissionForRoute(op.Method, op.Path)
		if access != routeProtected {
			continue
		}

		t.Run(op.Method+" "+op.Path, func(t *testing.T) {
			target := op.Path
			for _, placeholder := range []string{"{id}", "{teamId}", "{userId}", "{itemId}", "{feature}"} {
				target = replaceAll(target, placeholder, "00000000-0000-0000-0000-000000000000")
			}

			// No principal: 401.
			req := httptest.NewRequest(op.Method, target, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without principal, got %d", w.Code)
			}

			if required == "" {
				// Authenticated-only route: a principal with no permissions
				// must pass authorization.
				req = withPrincipal(httptest.NewRequest(op.Method, target, nil), identity.Principal{
					Subject:        "user-1",
					OrganizationID: "org-1",
					Type:           identity.PrincipalTypeUser,
				})
				w = httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
					t.Fatalf("expected authenticated-only route to allow any principal, got %d", w.Code)
				}
				return
			}

			// Principal without the required permission: 403.
			req = withPrincipal(httptest.NewRequest(op.Method, target, nil), identity.Principal{
				Subject:        "user-1",
				OrganizationID: "org-1",
				Permissions:    nil,
				Type:           identity.PrincipalTypeUser,
			})
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403 without permission %s, got %d", required, w.Code)
			}

			// The audit handler requires a live database pool, which the
			// in-memory test router intentionally does not provide; its
			// permission mapping is still asserted above and in the mapping
			// test.
			if op.Path == "/api/v1/audit" || op.Path == "/api/v1/audit/verify" {
				return
			}

			// Principal with the required permission: anything but 401/403.
			req = withPrincipal(httptest.NewRequest(op.Method, target, nil), identity.Principal{
				Subject:        "user-1",
				OrganizationID: "org-1",
				Permissions:    []identity.Permission{required},
				Type:           identity.PrincipalTypeUser,
			})
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Fatalf("expected permission %s to authorize %s %s, got %d", required, op.Method, op.Path, w.Code)
			}
		})
	}
}

func withPrincipal(r *http.Request, p identity.Principal) *http.Request {
	ctx := identity.WithPrincipal(r.Context(), p)
	return r.WithContext(ctx)
}

func replaceAll(s, old, new string) string {
	return strings.ReplaceAll(s, old, new)
}

// TestGraphQLRouteRequiresAuthenticationOnly pins the GraphQL BFF mapping
// (GQL-04): the route needs an authenticated principal but no blanket
// permission; each resolver checks its own read permission.
func TestGraphQLRouteRequiresAuthenticationOnly(t *testing.T) {
	required, access := PermissionForRoute(http.MethodPost, "/api/v1/graphql")
	if access != routeProtected || required != "" {
		t.Fatalf("POST /api/v1/graphql: permission %q access %v, want authenticated-only", required, access)
	}
}

// routePermissionsFile is the reviewed table of the required permission of
// every operation (RBA-04). Regenerate it with
// RETICORA_UPDATE_ROUTE_PERMISSIONS=1 and review the diff: every changed line
// changes who may call an operation.
const routePermissionsFile = "testdata/route_permissions.txt"

// TestEveryOperationRequiresItsExpectedPermission covers WP-045 (RBA-04): the
// permission of every registered operation equals the reviewed table, so a
// route cannot silently fall back to the right of its first path segment.
func TestEveryOperationRequiresItsExpectedPermission(t *testing.T) {
	var b strings.Builder
	for _, op := range routeTable(t, testRouter(t)) {
		if !strings.HasPrefix(op.Path, "/api/") && !strings.HasPrefix(op.Path, "/scim/") {
			continue
		}
		required, access := PermissionForRoute(op.Method, op.Path)
		value := string(required)
		switch {
		case access == routePublic:
			value = "public"
		case access == routeProtected && required == "":
			value = "authenticated"
		case access == routeUnmapped:
			value = "UNMAPPED"
		}
		fmt.Fprintf(&b, "%s %s %s\n", op.Method, op.Path, value)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"

	if os.Getenv("RETICORA_UPDATE_ROUTE_PERMISSIONS") == "1" {
		if err := os.WriteFile(routePermissionsFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(routePermissionsFile)
	if err != nil {
		t.Fatalf("read %s: %v", routePermissionsFile, err)
	}
	if got != string(want) {
		wantLines := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(string(want)), "\n") {
			wantLines[l] = true
		}
		for _, l := range lines {
			if !wantLines[l] {
				t.Errorf("operation not in the reviewed table (or with another permission): %s", l)
			}
			delete(wantLines, l)
		}
		for l := range wantLines {
			t.Errorf("reviewed operation missing or changed: %s", l)
		}
	}
}

// TestSpecialActionsRequireTheirOwnPermission pins the corrections of WP-045
// (RBA-04, RBA-06, MET-14, CI-10).
func TestSpecialActionsRequireTheirOwnPermission(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         identity.Permission
	}{
		{http.MethodDelete, "/api/v1/cis/{id}", identity.PermCIDelete},
		{http.MethodPatch, "/api/v1/cis/{id}", identity.PermCIWrite},
		{http.MethodGet, "/api/v1/credentials/{id}/decrypt", identity.PermCredentialDecrypt},
		{http.MethodGet, "/api/v1/credentials/{id}", identity.PermCredentialRead},
		{http.MethodPost, "/api/v1/orders/{id}/approve", identity.PermOrderApprove},
		{http.MethodPost, "/api/v1/orders/{id}/reject", identity.PermOrderApprove},
		{http.MethodPost, "/api/v1/orders/{id}/submit", identity.PermOrderWrite},
		{http.MethodPut, "/api/v1/cis/{id}/field-definitions", identity.PermCIInstanceAttributeManage},
		{http.MethodDelete, "/api/v1/cis/{id}/field-definitions/{name}", identity.PermCIInstanceAttributeManage},
		{http.MethodGet, "/api/v1/cis/{id}/field-definitions", identity.PermCIRead},
		{http.MethodPut, "/api/v1/cis/{id}/fields/{name}/override", identity.PermOverrideWrite},
		{http.MethodDelete, "/api/v1/cis/{id}/fields/{name}/override", identity.PermOverrideWrite},
		{http.MethodPost, "/api/v1/cis/{id}/lifecycle-transitions", identity.PermLifecycleTransition},
		{http.MethodPost, "/api/v1/assets/{id}/lifecycle-transitions", identity.PermLifecycleTransition},
		{http.MethodPut, "/api/v1/reconciliation/source-policy", identity.PermReconciliationManage},
		{http.MethodGet, "/api/v1/reconciliation/source-policy", identity.PermDiscoveryRead},
		{http.MethodPost, "/api/v1/cis/{id}/contacts", identity.PermContactWrite},
		{http.MethodPost, "/api/v1/cis/{id}/interfaces", identity.PermIPAMWrite},
		{http.MethodGet, "/api/v1/cis/{id}/relationships", identity.PermRelationshipRead},
	} {
		got, access := PermissionForRoute(c.method, c.path)
		if access != routeProtected || got != c.want {
			t.Errorf("%s %s: %q (access %v), want %q", c.method, c.path, got, access, c.want)
		}
	}
}
