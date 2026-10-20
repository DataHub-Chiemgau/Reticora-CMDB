//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the request
// chain (validation, pre-auth limit, rate limit). Each test encodes a
// requirement the product currently violates and fails on an assertion until
// the finding is fixed.
//
//   - WP-050 (AUT-10), finding P2 "Successful session refreshes consume the
//     per-IP pre-auth budget".
//   - WP-166 (API-04), finding P1 "Refused requests of one credential consume
//     the organization budget". Gap (critical): "refused credential traffic
//     leaves the org budget for others" (memory store and real Redis).
//   - WP-166 (API-04), finding P2 "'Per session' bucket is per access token
//     and resets on every refresh". Gap (high): session bucket across refresh.
//
// Retest:
//
//	cd backend && TEST_REDIS_URL=redis://localhost:56379/0 go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/middleware/
package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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

// auditRedisStore connects to TEST_REDIS_URL. The audit test needs it and
// fails without it instead of skipping.
func auditRedisStore(t *testing.T) cache.Store {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Fatal("TEST_REDIS_URL is required for this audit test (e.g. redis://localhost:56379/0)")
	}
	client, err := redisx.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return cache.NewRedisStore(client.Unwrap())
}

// TestAuditT2RefusedCredentialTrafficKeepsOrgBudget: API-04 gives every API
// key or session its own budget (600/min) and the organization a budget of
// admitted traffic (6,000/min). A client that keeps retrying after 429 must
// not use up the organization budget with refused requests and so lock out
// every other user of the organization.
func TestAuditT2RefusedCredentialTrafficKeepsOrgBudget(t *testing.T) {
	stores := map[string]func(t *testing.T) cache.Store{
		"memory store": func(*testing.T) cache.Store { return cache.NewMemoryStore() },
		"redis store":  auditRedisStore,
	}
	for _, name := range []string{"memory store", "redis store"} {
		t.Run(name, func(t *testing.T) {
			store := stores[name](t)
			org := "t2-org-" + strconv.FormatInt(time.Now().UnixNano(), 36)
			limits := RateLimits{PerCredential: 3, PerOrganization: 6}
			h := limitedHandler(limits, store)
			awaitFreshWindow()

			admitted, refused := 0, 0
			for range 10 {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, asPrincipal(org, "apikey:noisy", identity.PrincipalTypeAPIKey))
				switch w.Code {
				case http.StatusNoContent:
					admitted++
				case http.StatusTooManyRequests:
					refused++
				}
			}
			if admitted != 3 || refused != 7 {
				t.Fatalf("setup: noisy key admitted %d, refused %d; want 3 and 7 (credential budget 3)", admitted, refused)
			}

			// Only 3 of the 6 organization requests were admitted; another
			// session of the organization still has its full budget of 3.
			var codes []int
			var remaining []string
			for range 3 {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, asPrincipal(org, "session:victim", identity.PrincipalTypeUser))
				codes = append(codes, w.Code)
				remaining = append(remaining, w.Header().Get("X-RateLimit-Remaining"))
			}
			want := []int{http.StatusNoContent, http.StatusNoContent, http.StatusNoContent}
			if !slices.Equal(codes, want) {
				t.Errorf("victim session of the same organization after the noisy key's refused retries: codes %v (X-RateLimit-Remaining %v), want %v; refused requests must not count against the organization budget",
					codes, remaining, want)
			}
			if remaining[0] != "2" {
				t.Errorf("victim's first request: X-RateLimit-Remaining %q, want \"2\" (credential 3-1, organization 6-4 admitted)", remaining[0])
			}
		})
	}
}

// awaitFreshWindow waits for the next one-minute window when the current one
// ends within the next few seconds, so a test's requests share one window.
func awaitFreshWindow() {
	if left := 60 - time.Now().UTC().Unix()%60; left < 5 {
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
}

func auditSessionIssuer(t *testing.T) *identity.SessionIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

// TestAuditT2SessionBudgetSurvivesTokenRefresh: API-04 limits "600
// requests/min per API key or session". A refresh mints a new access token of
// the same session (AUT-02); it must not open a new budget, otherwise a
// client multiplies its limit by refreshing. Both tokens below are minted by
// the production refresh handler from one refresh family; a second login
// session keeps its own budget.
func TestAuditT2SessionBudgetSurvivesTokenRefresh(t *testing.T) {
	ctx := context.Background()
	store := cache.NewMemoryStore()
	revocations := identity.NewSessionRevocations(store)
	issuer := auditSessionIssuer(t).WithRevocations(revocations)
	refresh := identity.NewRefreshSessions(store, revocations)
	auth := identity.NewHandler(nil, issuer).WithRefreshSessions(refresh)
	org := "t2-org-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	// login starts a refresh family as the login callback does.
	login := func(subject string) string {
		t.Helper()
		cookie, err := refresh.Issue(ctx, &identity.RefreshGrant{Claims: identity.SessionClaims{
			Subject: subject, OrganizationID: org, Permissions: []identity.Permission{identity.PermCIRead},
			Scope: &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		}})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		return cookie
	}
	// doRefresh renews through the production handler and returns the new
	// access token and the rotated cookie.
	doRefresh := func(cookie string) (string, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
		w := httptest.NewRecorder()
		auth.Refresh(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("refresh: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Token == "" {
			t.Fatalf("refresh body %s: %v", w.Body.String(), err)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == identity.RefreshCookieName {
				return body.Token, c.Value
			}
		}
		t.Fatal("refresh set no rotated cookie")
		return "", ""
	}

	h := Chain(RequestID, AuthMiddlewareWithVerifier(issuer), TenantMiddleware,
		RateLimiterWithLimits(RateLimits{PerCredential: 3, PerOrganization: 1000}, store))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	get := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}

	awaitFreshWindow()
	tokenA, cookie := doRefresh(login("t2-user-1"))
	var first []int
	for range 3 {
		first = append(first, get(tokenA))
	}
	if !slices.Equal(first, []int{http.StatusNoContent, http.StatusNoContent, http.StatusNoContent}) {
		t.Fatalf("setup: the first three requests of the session -> %v, want 204", first)
	}
	tokenB, _ := doRefresh(cookie)
	if code := get(tokenB); code != http.StatusTooManyRequests {
		t.Errorf("4th request of the same session in the same minute, sent with the token of the next refresh -> %d, want 429 (the session budget of API-04 must not reset on refresh)", code)
	}
	if code := get(tokenA); code != http.StatusTooManyRequests {
		t.Errorf("control: 4th request with the first token -> %d, want 429", code)
	}

	// Control: a different login session has its own budget.
	tokenC, _ := doRefresh(login("t2-user-2"))
	if code := get(tokenC); code != http.StatusNoContent {
		t.Errorf("control: another session's first request -> %d, want 204", code)
	}
}
