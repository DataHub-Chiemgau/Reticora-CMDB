//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the router
// and its route authorization. Each test encodes a requirement the product
// currently violates and fails on an assertion until the finding is fixed.
//
//   - WP-066 / WP-050 (AUT-02, SEC-06, API-04), finding P1 "SPA refresh
//     POST /api/v1/auth/refresh always rejected with 400 by the OpenAPI
//     validator"; gap (critical) "No API test of refresh/logout through the
//     production middleware chain". Uses the chain helper of
//     t2_auth_chain_test.go.
//   - WP-068 (RBA-01), finding P2 "Authz mapping ignores the RBA-01 keys
//     citype:manage and notification:manage"; gap (high) "every RBA-01 key
//     gates a route; no near-duplicate keys".
//   - WP-069 (RBA-08, API-05), finding P1 "Service-account binding and role
//     assignment are not authorized against the caller's rights" (route
//     part: PUT /service-accounts/{id}/roles needs only user:manage).
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/server/
package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// TestAuditT2CookieOnlyRefreshThroughProductionChain: the SPA renews the
// 15-minute access token with a POST that carries only the HttpOnly refresh
// cookie (frontend/src/auth/session.ts:22-25; OpenAPI refreshSession has no
// requestBody). Through the production chain it must get 200 with a new
// token and a rotated cookie; a missing cookie and a reused cookie get 401.
func TestAuditT2CookieOnlyRefreshThroughProductionChain(t *testing.T) {
	s := newT2Stack(t)
	_, cookie := s.login(t, identity.PermPermissionRead)

	t.Run("cookie only refresh rotates", func(t *testing.T) {
		w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh", cookie: cookie})
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/v1/auth/refresh with only the refresh cookie: %d %s, want 200", w.Code, strings.TrimSpace(w.Body.String()))
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Token == "" {
			t.Errorf("refresh body %s: no token", w.Body.String())
		}
		c := t2RefreshCookie(w)
		if c == nil || c.Value == "" || c.Value == cookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/auth" {
			t.Errorf("rotated refresh cookie %+v, want a new HttpOnly, Secure, SameSite=Strict cookie for /api/v1/auth", c)
		}
		if w := s.do(t2Request{method: http.MethodGet, path: "/api/v1/me", bearer: body.Token}); w.Code != http.StatusOK {
			t.Errorf("GET /api/v1/me with the refreshed token: %d", w.Code)
		}
		if w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh", cookie: cookie}); w.Code != http.StatusUnauthorized {
			t.Errorf("second use of the consumed cookie: %d %s, want 401", w.Code, strings.TrimSpace(w.Body.String()))
		}
	})
	t.Run("refresh without cookie", func(t *testing.T) {
		if w := s.do(t2Request{method: http.MethodPost, path: "/api/v1/auth/refresh"}); w.Code != http.StatusUnauthorized {
			t.Errorf("POST /api/v1/auth/refresh without cookie: %d %s, want 401", w.Code, strings.TrimSpace(w.Body.String()))
		}
	})
}

// authorizeAs runs AuthorizeRoute for a principal holding exactly perms.
func authorizeAs(method, path string, perms ...identity.Permission) int {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(method, path, nil)
	p := identity.Principal{Subject: "u", OrganizationID: "o", Type: identity.PrincipalTypeUser, Permissions: perms}
	w := httptest.NewRecorder()
	server.AuthorizeRoute(method, path)(ok).ServeHTTP(w, req.WithContext(identity.WithPrincipal(req.Context(), p)))
	return w.Code
}

// TestAuditT2RBA01KeysGateCITypesAndNotificationChannels: RBA-01 names
// citype:manage for CI-type management and notification:manage for the
// notification channels. A custom role built from the RBA-01 vocabulary must
// work, and webhook:manage (the webhook subscriptions) must not grant the
// notification channels with their provider credentials. The catalogue has no
// parallel near-duplicate of an RBA-01 key.
func TestAuditT2RBA01KeysGateCITypesAndNotificationChannels(t *testing.T) {
	for _, c := range []struct {
		method, path string
		perms        []identity.Permission
		want         int
	}{
		{http.MethodPost, "/api/v1/ci-types", []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodPatch, "/api/v1/ci-types/7a2a0000-0000-4000-8000-000000000101", []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodDelete, "/api/v1/ci-types/7a2a0000-0000-4000-8000-000000000101", []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodPost, "/api/v1/notification-channels", []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodPost, "/api/v1/notification-channels", []identity.Permission{identity.PermWebhookManage, identity.PermWebhookRead}, http.StatusForbidden},
	} {
		if got := authorizeAs(c.method, c.path, c.perms...); got != c.want {
			t.Errorf("%s %s with only %v -> %d, want %d (RBA-01)", c.method, c.path, c.perms, got, c.want)
		}
	}

	// No catalogue key may be a near-duplicate of an RBA-01 key
	// (ci_type:manage next to citype:manage).
	rba01 := []identity.Permission{
		"ci:read", "ci:write", "ci:delete", "citype:manage", "location:read", "location:write", "rack:write",
		"relationship:write", "contact:write", "topology:read", "discovery:ingest", "discovery:manage",
		"collector:manage", "credential:manage", "vrf:manage", "review:resolve", "webhook:manage", "export:run",
		"job:read", "notification:manage", "user:manage", "team:manage", "role:manage", "entitlement:manage",
		"audit:read", "apikey:manage",
	}
	normalized := map[string]identity.Permission{}
	for _, k := range rba01 {
		normalized[strings.ReplaceAll(string(k), "_", "")] = k
	}
	for _, k := range identity.AllPermissions() {
		if slices.Contains(rba01, k) {
			continue
		}
		if twin, ok := normalized[strings.ReplaceAll(string(k), "_", "")]; ok {
			t.Errorf("permission catalogue holds %q next to the RBA-01 key %q", k, twin)
		}
	}
}

// TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage: service accounts
// are on a par with users (RBA-08); assigning roles is managing roles
// (RBA-01 role:manage, as POST /role-assignments). user:manage alone must not
// assign roles to a service account.
func TestAuditT2ServiceAccountRoleAssignmentNeedsRoleManage(t *testing.T) {
	const path = "/api/v1/service-accounts/7a2a0000-0000-4000-8000-000000000201/roles"
	if got := authorizeAs(http.MethodPut, path, identity.PermUserManage, identity.PermUserRead); got != http.StatusForbidden {
		t.Errorf("PUT %s with user:manage but without role:manage -> %d, want 403", path, got)
	}
	if got := authorizeAs(http.MethodPut, path, identity.PermUserManage, identity.PermUserRead, identity.PermRoleManage); got != http.StatusOK {
		t.Errorf("control: PUT %s with user:manage and role:manage -> %d, want 200", path, got)
	}
	if got := authorizeAs(http.MethodPost, "/api/v1/role-assignments", identity.PermUserManage); got != http.StatusForbidden {
		t.Errorf("control: POST /api/v1/role-assignments with only user:manage -> %d, want 403", got)
	}
}
