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
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/server/
package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
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
