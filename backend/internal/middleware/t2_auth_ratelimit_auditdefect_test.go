//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the request
// chain (pre-auth limit). Each test encodes a requirement the product
// currently violates and fails on an assertion until the finding is fixed.
//
//   - WP-050 (AUT-10), finding P2 "Successful session refreshes consume the
//     per-IP pre-auth budget".
//
// The rate limit budgets of WP-166 (API-04; F-019, F-076) were fixed and
// moved to ratelimit_test.go and ratelimit_redis_integration_test.go
// (WP-410); awaitFreshWindow lives there.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/middleware/
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

// TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget: AUT-10 limits
// login, signup, invitation acceptance and API-key checks per IP (20/min).
// Legitimate token renewals of the users behind one IP (NAT) must not use up
// that budget, otherwise the SPA logs users out with 429.
func TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget(t *testing.T) {
	h := PreAuthRateLimiter(DefaultPreAuthPerMinute, cache.NewMemoryStore())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/callback" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK) // a valid refresh cookie
	}))
	serve := func(path, ip string) int {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = ip + ":40000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	awaitFreshWindow()
	var refused []int
	for i := 1; i <= 30; i++ {
		if code := serve("/api/v1/auth/refresh", "198.51.100.20"); code != http.StatusOK {
			refused = append(refused, i)
		}
	}
	if len(refused) > 0 {
		t.Errorf("30 successful refreshes from one IP: requests %v were refused with 429; a successful refresh must not consume the AUT-10 pre-auth budget", refused)
	}

	// Control: failed logins from another IP are still limited at 20/min.
	codes := make([]int, 0, DefaultPreAuthPerMinute+1)
	for range DefaultPreAuthPerMinute + 1 {
		codes = append(codes, serve("/api/v1/auth/callback", "198.51.100.21"))
	}
	if codes[len(codes)-1] != http.StatusTooManyRequests {
		t.Errorf("control: 21st failed login from one IP -> %d, want 429 (AUT-10)", codes[len(codes)-1])
	}
}
