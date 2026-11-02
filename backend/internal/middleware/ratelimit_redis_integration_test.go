package middleware

// Budgets of the authenticated rate limit (API-04) on the Redis store all
// replicas share. The budget tests of ratelimit_test.go run here as their
// "redis store" cases (forEachBudgetStore); this file adds the store and the
// case of several replicas with their own Redis connection. Without
// TEST_REDIS_URL the Redis cases are skipped; the CI job migrations sets it.
//
// Retest:
//
//	cd backend && TEST_REDIS_URL=redis://localhost:56379/0 go test -count=1 -v -run '^TestAuditT2RefusedCredentialTrafficKeepsOrgBudget$|^TestAuditT2SessionBudgetSurvivesTokenRefresh$|^TestRateLimitBudget|^TestRateLimitRedis' ./internal/middleware/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
)

// auditRedisStore connects to TEST_REDIS_URL with a connection of its own;
// without it the test is skipped.
func auditRedisStore(t *testing.T) cache.Store {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; skipping Redis rate limit test (e.g. redis://localhost:56379/0)")
	}
	client, err := redisx.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return cache.NewRedisStore(client.Unwrap())
}

// TestRateLimitRedisReplicasShareBudgets covers WP-410 (API-04) on Redis:
// two replicas, each with its own Redis connection, count the same budgets.
// The refused retries of one API key, spread over both replicas, leave the
// organization budget to another credential; two access tokens of one
// session (same sid) share the session budget across the replicas.
func TestRateLimitRedisReplicasShareBudgets(t *testing.T) {
	stores := []cache.Store{auditRedisStore(t), auditRedisStore(t)}
	issuer := auditSessionIssuer(t)
	org := "replica-org-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	limits := RateLimits{PerCredential: 3, PerOrganization: 6}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// Each replica has its own Redis connection. limiters[i] is the rate
	// limit of replica i; requests of an API key reach it with the principal
	// the auth middleware would set, session tokens through the auth chain.
	limiters, replicas := make([]http.Handler, len(stores)), make([]http.Handler, len(stores))
	for i, store := range stores {
		limiters[i] = rateLimiter(limits, store, fixedWindow)(ok)
		replicas[i] = Chain(RequestID, AuthMiddlewareWithVerifier(issuer), TenantMiddleware)(limiters[i])
	}
	session := func(jti string) string {
		t.Helper()
		now := time.Now().UTC()
		token, err := issuer.Issue(identity.SessionClaims{
			Subject: "replica-user", OrganizationID: org, Permissions: []identity.Permission{identity.PermCIRead},
			SessionID: "replica-sid", ID: jti, IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	get := func(replica int, token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		replicas[replica].ServeHTTP(w, req)
		return w.Code
	}

	admitted := 0
	for i := range 10 {
		w := httptest.NewRecorder()
		limiters[i%2].ServeHTTP(w, asPrincipal(org, "apikey:noisy", identity.PrincipalTypeAPIKey))
		if w.Code == http.StatusNoContent {
			admitted++
		}
	}
	if admitted != 3 {
		t.Fatalf("10 requests of one API key over two replicas: %d admitted, want 3 (credential budget 3)", admitted)
	}

	// The session's two tokens: 3 admitted over both replicas, then 429 on
	// either; the organization had 3 + 3 admitted requests.
	first, second := session("replica-jti-1"), session("replica-jti-2")
	codes := []int{get(0, first), get(1, second), get(0, second), get(1, first), get(0, second)}
	want := []int{http.StatusNoContent, http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests, http.StatusTooManyRequests}
	if !slices.Equal(codes, want) {
		t.Fatalf("requests of one session (two tokens, one sid) over two replicas after the API key's refused retries: %v, want %v", codes, want)
	}
	// Now the organization budget is spent on both replicas.
	for i := range limiters {
		w := httptest.NewRecorder()
		limiters[i].ServeHTTP(w, asPrincipal(org, "apikey:late-"+strconv.Itoa(i), identity.PrincipalTypeAPIKey))
		if w.Code != http.StatusTooManyRequests || w.Header().Get("X-RateLimit-Limit") != "6" {
			t.Errorf("replica %d, new API key after 6 admitted requests of the organization: %d, X-RateLimit-Limit %q; want 429 from the organization budget 6",
				i+1, w.Code, w.Header().Get("X-RateLimit-Limit"))
		}
	}
}
