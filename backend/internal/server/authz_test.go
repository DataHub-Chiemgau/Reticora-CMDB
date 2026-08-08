package server

import (
	"net/http"
	"net/http/httptest"
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
