package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func limitedHandler(limits RateLimits, store cache.Store) http.Handler {
	return RateLimiterWithLimits(limits, store)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func asPrincipal(org, credential string, kind identity.PrincipalType) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	return req.WithContext(identity.WithPrincipal(req.Context(), identity.Principal{
		Subject: "user-1", OrganizationID: org, Type: kind, Credential: credential,
	}))
}

// TestRateLimitPerCredential covers API-04: every session and every API
// key has its own budget, even when they belong to the same user; the
// limit is exact.
func TestRateLimitPerCredential(t *testing.T) {
	h := limitedHandler(RateLimits{PerCredential: 3, PerOrganization: 100}, cache.NewMemoryStore())
	serve := func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for i := 1; i <= 3; i++ {
		w := serve(asPrincipal("org-1", "session:a", identity.PrincipalTypeUser))
		if w.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i, w.Code)
		}
		if got := w.Header().Get("X-RateLimit-Remaining"); got != strconv.Itoa(3-i) {
			t.Errorf("request %d: remaining %s", i, got)
		}
	}
	w := serve(asPrincipal("org-1", "session:a", identity.PrincipalTypeUser))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || w.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Fatalf("4th request: %d, headers %v", w.Code, w.Header())
	}
	// Another session of the same user and an API key keep their budgets.
	for _, cred := range []string{"session:b", "apikey:rk_1"} {
		if w = serve(asPrincipal("org-1", cred, identity.PrincipalTypeUser)); w.Code != http.StatusNoContent {
			t.Errorf("%s: %d", cred, w.Code)
		}
	}
}

// TestRateLimitPerOrganization: the organization budget caps all
// credentials of the organization together; another organization is not
// affected.
func TestRateLimitPerOrganization(t *testing.T) {
	h := limitedHandler(RateLimits{PerCredential: 3, PerOrganization: 5}, cache.NewMemoryStore())
	codes := []int{}
	for i := range 6 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, asPrincipal("org-1", "session:"+strconv.Itoa(i), identity.PrincipalTypeUser))
		codes = append(codes, w.Code)
		if i == 4 && (w.Header().Get("X-RateLimit-Limit") != "5" || w.Header().Get("X-RateLimit-Remaining") != "0") {
			t.Errorf("5th request: the organization budget is the tighter one: %v", w.Header())
		}
	}
	if codes[4] != http.StatusNoContent || codes[5] != http.StatusTooManyRequests {
		t.Errorf("codes %v", codes)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, asPrincipal("org-2", "session:x", identity.PrincipalTypeUser))
	if w.Code != http.StatusNoContent {
		t.Errorf("other organization: %d", w.Code)
	}
}

// TestRateLimitHeadersAndDefaults: unauthenticated requests are counted
// per client IP and carry the headers; the defaults are 600 and 6,000;
// the reset lies within the minute.
func TestRateLimitHeadersAndDefaults(t *testing.T) {
	if l := (RateLimits{}).withDefaults(); l.PerCredential != 600 || l.PerOrganization != 6000 {
		t.Errorf("defaults %+v", l)
	}
	h := limitedHandler(RateLimits{PerCredential: 2}, cache.NewMemoryStore())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
	reset, _ := strconv.Atoi(w.Header().Get("X-RateLimit-Reset"))
	if w.Header().Get("X-RateLimit-Limit") != "2" || w.Header().Get("X-RateLimit-Remaining") != "1" || reset < 1 || reset > 60 {
		t.Errorf("headers %v", w.Header())
	}
	d, err := (RateLimits{PerCredential: 1, PerOrganization: 1}).take(asPrincipal("o", "c", identity.PrincipalTypeAPIKey), cache.NewMemoryStore(),
		time.Date(2026, 1, 1, 12, 0, 45, 0, time.UTC))
	if err != nil || !d.allowed || d.reset != 15 {
		t.Errorf("decision %+v %v", d, err)
	}
}

type failingStore struct{ cache.Store }

func (failingStore) Increment(context.Context, string, time.Duration) (int64, error) {
	return 0, errors.New("redis down")
}

// TestRateLimitFailsClosed: without the store every request is refused.
func TestRateLimitFailsClosed(t *testing.T) {
	w := httptest.NewRecorder()
	limitedHandler(RateLimits{}, failingStore{}).ServeHTTP(w, asPrincipal("org-1", "session:a", identity.PrincipalTypeUser))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status %d", w.Code)
	}
}

// forEachBudgetStore runs a budget test on the in-memory store of a single
// instance and on the Redis store all replicas share (auditRedisStore in
// ratelimit_redis_integration_test.go, skipped without TEST_REDIS_URL).
func forEachBudgetStore(t *testing.T, run func(t *testing.T, store cache.Store)) {
	t.Helper()
	t.Run("memory store", func(t *testing.T) { run(t, cache.NewMemoryStore()) })
	t.Run("redis store", func(t *testing.T) { run(t, auditRedisStore(t)) })
}

// awaitFreshWindow waits for the next one-minute window when the current one
// ends within the next few seconds, so a test's requests share one window.
func awaitFreshWindow() {
	if left := 60 - time.Now().UTC().Unix()%60; left < 5 {
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
}

// fixedWindow is a clock 45 seconds into a minute: every request of a test
// that uses it falls into one window, however long the test runs.
func fixedWindow() time.Time { return time.Date(2026, 1, 1, 12, 0, 45, 0, time.UTC) }

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

// TestAuditT2RefusedCredentialTrafficKeepsOrgBudget covers WP-410 (API-04,
// F-019): every API key or session has its own budget (600/min) and the
// organization a budget of admitted traffic (6,000/min). A client that keeps
// retrying after 429 must not use up the organization budget with refused
// requests and so lock out every other user of the organization.
func TestAuditT2RefusedCredentialTrafficKeepsOrgBudget(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
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

// TestAuditT2SessionBudgetSurvivesTokenRefresh covers WP-410 (API-04,
// F-076): the limit is "600 requests/min per API key or session". A refresh
// mints a new access token of the same session (AUT-02, same sid); it must
// not open a new budget, otherwise a client multiplies its limit by
// refreshing. Both tokens below are minted by the production refresh handler
// from one refresh family; a second login session keeps its own budget.
func TestAuditT2SessionBudgetSurvivesTokenRefresh(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		ctx := context.Background()
		revocations := identity.NewSessionRevocations(store)
		issuer := auditSessionIssuer(t).WithRevocations(revocations)
		refresh := identity.NewRefreshSessions(store, revocations)
		auth := identity.NewHandler(nil, issuer).WithRefreshSessions(refresh)
		run := strconv.FormatInt(time.Now().UnixNano(), 36)
		org := "t2-org-" + run

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
		tokenA, cookie := doRefresh(login("t2-user-1-" + run))
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
		tokenC, _ := doRefresh(login("t2-user-2-" + run))
		if code := get(tokenC); code != http.StatusNoContent {
			t.Errorf("control: another session's first request -> %d, want 204", code)
		}
	})
}

// TestRateLimitSessionCredentialIsSid covers WP-410 (API-04, AUT-02): the
// per-session budget counts the claim sid, so access tokens of one session
// share it; a token without sid (issued before the claim existed, valid for
// at most 15 more minutes) is counted by its token id.
func TestRateLimitSessionCredentialIsSid(t *testing.T) {
	issuer := auditSessionIssuer(t)
	org := "sid-org-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var credential string
	h := Chain(RequestID, AuthMiddlewareWithVerifier(issuer), TenantMiddleware,
		rateLimiter(RateLimits{PerCredential: 2, PerOrganization: 1000}, cache.NewMemoryStore(), fixedWindow))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, _ := PrincipalFromContext(r.Context())
			credential = principal.Credential
			w.WriteHeader(http.StatusNoContent)
		}))
	mint := func(sid, jti string) string {
		t.Helper()
		now := time.Now().UTC()
		token, err := issuer.Issue(identity.SessionClaims{
			Subject: "sid-user", OrganizationID: org, Permissions: []identity.Permission{identity.PermCIRead},
			SessionID: sid, ID: jti, IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	get := func(token string) int {
		credential = ""
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}

	first, second := mint("sid-1", "jti-1"), mint("sid-1", "jti-2")
	if code := get(first); code != http.StatusNoContent || credential != "session:sid-1" {
		t.Fatalf("token with sid: %d, credential %q; want 204 and \"session:sid-1\"", code, credential)
	}
	if code := get(second); code != http.StatusNoContent || credential != "session:sid-1" {
		t.Fatalf("second token of the session: %d, credential %q; want 204 and \"session:sid-1\"", code, credential)
	}
	if code := get(first); code != http.StatusTooManyRequests {
		t.Errorf("3rd request of the session (budget 2, two tokens) -> %d, want 429", code)
	}

	legacyA, legacyB := mint("", "jti-a"), mint("", "jti-b")
	if code := get(legacyA); code != http.StatusNoContent || credential != "session:jti-a" {
		t.Fatalf("token without sid: %d, credential %q; want 204 and \"session:jti-a\" (token id)", code, credential)
	}
	if code := get(legacyA); code != http.StatusNoContent {
		t.Errorf("2nd request of the token without sid -> %d, want 204", code)
	}
	if code := get(legacyA); code != http.StatusTooManyRequests {
		t.Errorf("3rd request of the token without sid -> %d, want 429", code)
	}
	if code := get(legacyB); code != http.StatusNoContent || credential != "session:jti-b" {
		t.Errorf("another token without sid: %d, credential %q; want 204 and \"session:jti-b\" (own budget)", code, credential)
	}
}

// rateAnswer is what a client sees of a rate-limited response.
type rateAnswer struct {
	code                    int
	limit, remaining, retry string
	detail                  string
}

func rateAnswerOf(t *testing.T, w *httptest.ResponseRecorder) rateAnswer {
	t.Helper()
	a := rateAnswer{code: w.Code, limit: w.Header().Get("X-RateLimit-Limit"),
		remaining: w.Header().Get("X-RateLimit-Remaining"), retry: w.Header().Get("Retry-After")}
	if w.Code != http.StatusNoContent {
		var problem struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatalf("429 body %q: %v", w.Body.String(), err)
		}
		a.detail = problem.Detail
	}
	return a
}

// TestRateLimitBudgetBoundaries covers WP-410 (API-04) with the default
// budgets of 600 requests per minute per credential and 6,000 per
// organization. Credential A sends 700 requests: from request 601 on it gets
// 429 with scope credential, while credential B of the same organization
// keeps getting 204. A's 100 refused requests do not count, so the request
// after 6,000 admitted ones of the organization (its request 6,001) gets 429
// with scope organization. X-RateLimit-* name the tighter budget.
func TestRateLimitBudgetBoundaries(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		org := "budget-org-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		h := rateLimiter(RateLimits{}, store, fixedWindow)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		serve := func(orgID, credential string) rateAnswer {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, asPrincipal(orgID, credential, identity.PrincipalTypeAPIKey))
			return rateAnswerOf(t, w)
		}
		admitted := func(limit, remaining int) rateAnswer {
			return rateAnswer{code: http.StatusNoContent, limit: strconv.Itoa(limit), remaining: strconv.Itoa(remaining)}
		}
		refused := func(limit int, scope string) rateAnswer {
			return rateAnswer{code: http.StatusTooManyRequests, limit: strconv.Itoa(limit), remaining: "0", retry: "15",
				detail: "rate limit exceeded for this " + scope + ", try again later"}
		}
		expect := func(what string, got, want rateAnswer) {
			t.Helper()
			if got != want {
				t.Fatalf("%s: %+v, want %+v", what, got, want)
			}
		}

		// Credential A: 700 requests. The credential budget is the tighter
		// one (600-i left against 6,000-i of the organization).
		for i := 1; i <= 700; i++ {
			want := admitted(600, 600-i)
			if i > 600 {
				want = refused(600, "credential")
			}
			expect("credential A, request "+strconv.Itoa(i), serve(org, "apikey:a"), want)
		}
		// Credential B of the same organization: 204; 599 left of its own
		// budget against 5,399 of the organization (601 admitted).
		expect("credential B after A's 700 requests", serve(org, "session:b"), admitted(600, 599))

		// Eight more credentials use their full budget: 5,401 admitted.
		for c := 1; c <= 8; c++ {
			cred := "apikey:fill-" + strconv.Itoa(c)
			for i := 1; i <= 600; i++ {
				if got := serve(org, cred); got.code != http.StatusNoContent {
					t.Fatalf("%s, request %d: %+v, want 204 (organization budget not yet spent)", cred, i, got)
				}
			}
		}
		// Credential D brings the organization to 6,000 admitted requests;
		// now the organization budget is the tighter one (599-i left against
		// 600-i of the credential).
		for i := 1; i <= 599; i++ {
			expect("credential D, request "+strconv.Itoa(i), serve(org, "apikey:d"), admitted(6000, 599-i))
		}

		// Request 6,001: refused by the organization budget, whichever
		// credential sends it.
		expect("request 6,001 of the organization (new credential E)", serve(org, "apikey:e"), refused(6000, "organization"))
		expect("credential B with budget left", serve(org, "session:b"), refused(6000, "organization"))
		// A spent credential is still refused by its own budget first.
		expect("credential A", serve(org, "apikey:a"), refused(600, "credential"))
		// Another organization is not affected.
		expect("credential of another organization", serve(org+"-other", "apikey:a"), admitted(600, 599))
	})
}

// TestRateLimitBudgetsHoldUnderConcurrency covers WP-410 (API-04): every
// budget decides on the value its atomic increment returns, so concurrent
// requests on two replicas of one store admit exactly the limit. A credential
// firing 50 requests at once gets 20 admitted, and only those count against
// the organization budget of 30, which leaves another credential exactly 10.
func TestRateLimitBudgetsHoldUnderConcurrency(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		org := "atomic-org-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		limits := RateLimits{PerCredential: 20, PerOrganization: 30}
		ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		replicas := []http.Handler{rateLimiter(limits, store, fixedWindow)(ok), rateLimiter(limits, store, fixedWindow)(ok)}
		fire := func(credential string, n int) map[string]int {
			var mu sync.Mutex
			var wg sync.WaitGroup
			outcome := map[string]int{}
			for i := range n {
				wg.Go(func() {
					w := httptest.NewRecorder()
					replicas[i%len(replicas)].ServeHTTP(w, asPrincipal(org, credential, identity.PrincipalTypeAPIKey))
					key := "admitted"
					if w.Code != http.StatusNoContent {
						var problem struct {
							Detail string `json:"detail"`
						}
						_ = json.Unmarshal(w.Body.Bytes(), &problem)
						key = strconv.Itoa(w.Code) + " " + strings.TrimSuffix(strings.TrimPrefix(problem.Detail, "rate limit exceeded for this "), ", try again later")
					}
					mu.Lock()
					outcome[key]++
					mu.Unlock()
				})
			}
			wg.Wait()
			return outcome
		}
		if got, want := fire("apikey:noisy", 50), map[string]int{"admitted": 20, "429 credential": 30}; !maps.Equal(got, want) {
			t.Errorf("50 concurrent requests of one credential (budget 20): %v, want %v", got, want)
		}
		if got, want := fire("apikey:other", 15), map[string]int{"admitted": 10, "429 organization": 5}; !maps.Equal(got, want) {
			t.Errorf("15 concurrent requests of another credential (organization budget 30, 20 admitted): %v, want %v", got, want)
		}
	})
}

// preAuthTestIP is a client address of its own per test run (group names
// the client within the test), so the pre-auth windows of the shared Redis
// store start empty.
func preAuthTestIP(group string) string {
	n := time.Now().UnixNano()
	return "[2001:db8:" + group + "::" + strconv.FormatInt(n>>16&0xffff, 16) + ":" + strconv.FormatInt(n&0xffff, 16) + "]"
}

// TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget covers WP-462
// (AUT-10, F-063): AUT-10 limits login, signup, invitation acceptance and
// API-key checks per IP (20/min). Legitimate token renewals of the users
// behind one IP (NAT) must not use up that budget, otherwise the SPA logs
// users out with 429.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestAuditT2SuccessfulRefreshDoesNotConsumePreAuthBudget$' ./internal/middleware/
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

// TestPreAuthAdmissionIsAtomic covers WP-462 (AUT-10): the pre-auth limit
// counts with an atomic increment before the handler and decides on the
// value it returns. 50 concurrent failed refreshes of one IP: exactly 20
// reach the handler, the other 30 get 429 with Retry-After and the
// X-RateLimit headers of the IP budget. Successful refreshes give their unit
// back, and a valid API key gives it back once it is authenticated: 50
// requests with a valid key of one IP pass while all their handlers are
// still running. Afterwards that IP still has its whole budget for failed
// attempts. On the memory store and on the Redis store all replicas share
// (Decrement of both stores).
func TestPreAuthAdmissionIsAtomic(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		awaitFreshWindow()
		keys := fakeAPIKeys{keys: map[string]*identity.APIKeyInfo{
			"valid": {ID: "key-1", OrganizationID: "org-1", Scopes: []identity.Permission{identity.PermCIRead}},
		}}
		var mu sync.Mutex
		reached := map[string]int{}
		entered, hold := make(chan struct{}, 50), make(chan struct{})
		handler := Chain(
			PreAuthRateLimiter(DefaultPreAuthPerMinute, store),
			AuthMiddlewareWithAPIKeys(nil, keys),
		)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			reached[r.RemoteAddr]++
			mu.Unlock()
			switch r.Header.Get("X-Test-Outcome") {
			case "fail":
				// The failed attempts overlap while their handlers run.
				time.Sleep(20 * time.Millisecond)
				w.WriteHeader(http.StatusUnauthorized)
				return
			case "hold":
				entered <- struct{}{}
				<-hold
			}
			w.WriteHeader(http.StatusOK)
		}))
		codes := map[int]int{}
		serve := func(ip, path, apiKey, outcome string, start <-chan struct{}) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.RemoteAddr = ip + ":40000"
			req.Header.Set("X-Test-Outcome", outcome)
			if apiKey != "" {
				req.Header.Set("X-API-Key", apiKey)
			}
			<-start
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			h := w.Header()
			if h.Get("X-RateLimit-Limit") != "20" || h.Get("X-RateLimit-Remaining") == "" || h.Get("X-RateLimit-Reset") == "" ||
				(w.Code == http.StatusTooManyRequests && (h.Get("Retry-After") == "" || h.Get("X-RateLimit-Remaining") != "0")) {
				t.Errorf("%s %d without the documented pre-auth headers: %v", path, w.Code, h)
			}
			mu.Lock()
			codes[w.Code]++
			mu.Unlock()
		}
		fire := func(ip, outcome string) map[int]int {
			codes = map[int]int{}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range 50 {
				wg.Go(func() { serve(ip, "/api/v1/auth/refresh", "", outcome, start) })
			}
			close(start)
			wg.Wait()
			return codes
		}

		failing := preAuthTestIP("4620")
		if got, want := fire(failing, "fail"), map[int]int{401: 20, 429: 30}; !maps.Equal(got, want) {
			t.Errorf("50 concurrent failed refreshes of one IP: %v, want %v", got, want)
		}
		if n := reached[failing+":40000"]; n != DefaultPreAuthPerMinute {
			t.Errorf("50 concurrent failed refreshes of one IP: %d reached the handler, want exactly %d", n, DefaultPreAuthPerMinute)
		}

		nat := preAuthTestIP("4621")
		codes = map[int]int{}
		for range 50 {
			serve(nat, "/api/v1/auth/refresh", "", "ok", closedChan)
		}
		if want := map[int]int{200: 50}; !maps.Equal(codes, want) {
			t.Errorf("50 successful refreshes of one IP: %v, want %v", codes, want)
		}
		// 50 requests with a valid key; each wave of 10 is let in only
		// while the earlier ones still run their handlers.
		codes = map[int]int{}
		var wg sync.WaitGroup
		for range 5 {
			for range 10 {
				wg.Go(func() { serve(nat, "/api/v1/cis", "valid", "hold", closedChan) })
			}
			for range 10 {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					close(hold)
					t.Fatalf("requests with a valid key did not reach their handlers while others ran: %v", codes)
				}
			}
		}
		close(hold)
		wg.Wait()
		if want := map[int]int{200: 50}; !maps.Equal(codes, want) {
			t.Errorf("50 overlapping requests with a valid API key from one IP: %v, want %v", codes, want)
		}
		if got, want := fire(nat, "fail"), map[int]int{401: 20, 429: 30}; !maps.Equal(got, want) {
			t.Errorf("failed refreshes after 100 successful requests of the IP: %v, want %v (the successful ones gave their units back)", got, want)
		}
	})
}

// closedChan lets a request start at once.
var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// slowAPIKeys validates like fakeAPIKeys after a delay, as the key store
// lookup of the server does, and counts the checks.
type slowAPIKeys struct {
	fakeAPIKeys
	delay time.Duration
	calls *atomic.Int64
}

func (s slowAPIKeys) Validate(ctx context.Context, rawKey string) (*identity.APIKeyInfo, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	return s.fakeAPIKeys.Validate(ctx, rawKey)
}

// slowSessions verifies session tokens after a delay.
type slowSessions struct {
	inner SessionVerifier
	delay time.Duration
}

func (s slowSessions) Validate(token string) (*identity.SessionClaims, error) {
	time.Sleep(s.delay)
	return s.inner.Validate(token)
}

// preAuthReplicas builds two replicas of the pre-auth limit and the
// authentication on one store; every credential check takes 25 ms, a
// successful refresh as long. It returns the replicas, a minter of valid
// session tokens and the number of requests that reached the handler.
func preAuthReplicas(t *testing.T, store cache.Store, keyChecks *atomic.Int64) ([]http.Handler, func(sid string) string, *atomic.Int64) {
	t.Helper()
	const delay = 25 * time.Millisecond
	issuer := auditSessionIssuer(t)
	keys := slowAPIKeys{fakeAPIKeys: fakeAPIKeys{keys: map[string]*identity.APIKeyInfo{
		"valid": {ID: "key-1", OrganizationID: "org-1", Scopes: []identity.Permission{identity.PermCIRead}},
	}}, delay: delay, calls: keyChecks}
	reached := &atomic.Int64{}
	replicas := make([]http.Handler, 2)
	for i := range replicas {
		replicas[i] = Chain(
			PreAuthRateLimiter(DefaultPreAuthPerMinute, store),
			AuthMiddlewareWithAPIKeys(slowSessions{inner: issuer, delay: delay}, keys),
		)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached.Add(1)
			if r.URL.Path == "/api/v1/auth/refresh" {
				time.Sleep(delay) // a valid refresh cookie
			}
			w.WriteHeader(http.StatusOK)
		}))
	}
	mint := func(sid string) string {
		t.Helper()
		now := time.Now().UTC()
		token, err := issuer.Issue(identity.SessionClaims{
			Subject: "user-1", OrganizationID: "org-1", Permissions: []identity.Permission{identity.PermCIRead},
			SessionID: sid, IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	return replicas, mint, reached
}

// preAuthCall is one request of a concurrency test: path and credential.
type preAuthCall struct {
	path, apiKey, bearer string
}

// fireConcurrently starts all calls from one IP at the same moment,
// alternating between the replicas, and returns the status histogram and
// one refused response.
func fireConcurrently(replicas []http.Handler, ip string, calls []preAuthCall) (map[int]int, *httptest.ResponseRecorder) {
	var mu sync.Mutex
	codes := map[int]int{}
	var refused *httptest.ResponseRecorder
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range calls {
		h := replicas[i%len(replicas)]
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			req.RemoteAddr = ip + ":40000"
			if c.apiKey != "" {
				req.Header.Set("X-API-Key", c.apiKey)
			}
			if c.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+c.bearer)
			}
			<-start
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			mu.Lock()
			codes[w.Code]++
			if w.Code == http.StatusTooManyRequests && refused == nil {
				refused = w
			}
			mu.Unlock()
		})
	}
	close(start)
	wg.Wait()
	return codes, refused
}

// TestValidCredentialsNeverHit429UnderConcurrency covers WP-658 (F-387;
// AUT-10, API-04): the pre-auth limit counts failed attempts, not
// concurrency. 60 concurrent requests with a valid API key, 60 with a valid
// bearer token and 30 successful refreshes of one IP (many users behind one
// NAT or proxy address, or one integration with parallel workers), spread
// over two replicas, while every credential check takes 25 ms: none of them
// gets 429, all reach the handler. Afterwards the IP still has its whole
// budget for failed attempts: 20 invalid keys get 401, the 21st 429.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestValidCredentialsNeverHit429UnderConcurrency$' ./internal/middleware/
func TestValidCredentialsNeverHit429UnderConcurrency(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		awaitFreshWindow()
		replicas, mint, reached := preAuthReplicas(t, store, &atomic.Int64{})
		var calls []preAuthCall
		for i := range 60 {
			calls = append(calls,
				preAuthCall{path: "/api/v1/cis", apiKey: "valid"},
				preAuthCall{path: "/api/v1/cis", bearer: mint("sid-" + strconv.Itoa(i%3))})
		}
		for range 30 {
			calls = append(calls, preAuthCall{path: "/api/v1/auth/refresh"})
		}
		ip := preAuthTestIP("6580")
		codes, refused := fireConcurrently(replicas, ip, calls)
		if want := map[int]int{http.StatusOK: len(calls)}; !maps.Equal(codes, want) {
			detail := ""
			if refused != nil {
				detail = fmt.Sprintf("; a refused one: X-RateLimit-Limit %s, %s", refused.Header().Get("X-RateLimit-Limit"), strings.TrimSpace(refused.Body.String()))
			}
			t.Errorf("%d concurrent requests with valid credentials from one IP: %v, want %v (no failed attempt, so no 429)%s", len(calls), codes, want, detail)
		}
		if n := reached.Load(); n != int64(len(calls)) {
			t.Errorf("%d of %d requests with valid credentials reached the handler", n, len(calls))
		}

		// The valid requests left no units behind.
		for i := 1; i <= DefaultPreAuthPerMinute+1; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
			req.RemoteAddr = ip + ":40000"
			req.Header.Set("X-API-Key", "guess-"+strconv.Itoa(i))
			w := httptest.NewRecorder()
			replicas[i%2].ServeHTTP(w, req)
			want := http.StatusUnauthorized
			if i > DefaultPreAuthPerMinute {
				want = http.StatusTooManyRequests
			}
			if w.Code != want {
				t.Errorf("invalid key %d after the concurrent valid requests: %d, want %d", i, w.Code, want)
			}
		}
	})
}

// TestPreAuthBruteForceStillTripsUnderConcurrency covers WP-658 (F-387;
// AUT-10): requests with valid credentials no longer get 429 for being
// concurrent, but guessing stays limited. 60 concurrent requests with
// invalid API keys of one IP over two replicas, every check 25 ms: exactly
// 20 keys are checked and answered 401, the other 40 get 429 with
// Retry-After and X-RateLimit-Remaining 0 as soon as the 20 have failed (not
// after the wait bound). Then even a valid key from that IP gets 429 until
// the minute is over; another IP keeps its budget.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestPreAuthBruteForceStillTripsUnderConcurrency$' ./internal/middleware/
func TestPreAuthBruteForceStillTripsUnderConcurrency(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		awaitFreshWindow()
		checks := &atomic.Int64{}
		replicas, _, reached := preAuthReplicas(t, store, checks)
		calls := make([]preAuthCall, 60)
		for i := range calls {
			calls[i] = preAuthCall{path: "/api/v1/cis", apiKey: "guess-" + strconv.Itoa(i)}
		}
		ip := preAuthTestIP("6581")
		began := time.Now()
		codes, refused := fireConcurrently(replicas, ip, calls)
		elapsed := time.Since(began)
		if want := map[int]int{http.StatusUnauthorized: DefaultPreAuthPerMinute, http.StatusTooManyRequests: len(calls) - DefaultPreAuthPerMinute}; !maps.Equal(codes, want) {
			t.Errorf("%d concurrent invalid keys from one IP: %v, want %v", len(calls), codes, want)
		}
		if n := checks.Load(); n != DefaultPreAuthPerMinute {
			t.Errorf("%d concurrent invalid keys from one IP: %d were checked, want exactly %d", len(calls), n, DefaultPreAuthPerMinute)
		}
		if n := reached.Load(); n != 0 {
			t.Errorf("%d requests with invalid keys reached the handler", n)
		}
		if refused != nil {
			if h := refused.Header(); h.Get("Retry-After") == "" || h.Get("X-RateLimit-Remaining") != "0" || h.Get("X-RateLimit-Limit") != "20" {
				t.Errorf("pre-auth 429 without the documented headers: %v", h)
			}
		}
		if elapsed > 5*time.Second {
			t.Errorf("the refused guesses took %s: once failures have spent the budget they must be refused at once", elapsed)
		}

		valid := []preAuthCall{{path: "/api/v1/cis", apiKey: "valid"}}
		if codes, _ := fireConcurrently(replicas, ip, valid); codes[http.StatusTooManyRequests] != 1 {
			t.Errorf("valid key from the IP that spent its budget on failures: %v, want 429", codes)
		}
		if codes, _ := fireConcurrently(replicas, preAuthTestIP("6582"), valid); codes[http.StatusOK] != 1 {
			t.Errorf("valid key from another IP: %v, want 200", codes)
		}
	})
}

// TestPreAuthWaitIsBounded covers WP-658 (F-387; AUT-10): a request that
// finds all units of its IP held by requests in flight waits for one and is
// served once one is given back; while none is given back it gets 429 with
// Retry-After after preAuthMaxWait, and a client that is gone stops waiting.
func TestPreAuthWaitIsBounded(t *testing.T) {
	forEachBudgetStore(t, func(t *testing.T, store cache.Store) {
		defer func(old time.Duration) { preAuthMaxWait = old }(preAuthMaxWait)
		preAuthMaxWait = 300 * time.Millisecond
		awaitFreshWindow()
		entered, hold := make(chan struct{}, DefaultPreAuthPerMinute), make(chan struct{})
		handler := PreAuthRateLimiter(DefaultPreAuthPerMinute, store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Hold") != "" {
				entered <- struct{}{}
				<-hold
			}
			w.WriteHeader(http.StatusOK) // a valid refresh cookie
		}))
		ip := preAuthTestIP("6583")
		refresh := func(ctx context.Context, held bool) *httptest.ResponseRecorder {
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/refresh", nil)
			req.RemoteAddr = ip + ":40000"
			if held {
				req.Header.Set("X-Test-Hold", "1")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			return w
		}
		// 20 refreshes hold every unit of the IP in their handlers.
		var wg sync.WaitGroup
		for range DefaultPreAuthPerMinute {
			wg.Go(func() {
				if w := refresh(context.Background(), true); w.Code != http.StatusOK {
					t.Errorf("held refresh: %d, want 200", w.Code)
				}
			})
		}
		for range DefaultPreAuthPerMinute {
			<-entered
		}

		began := time.Now()
		w := refresh(context.Background(), false)
		if waited := time.Since(began); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || waited < preAuthMaxWait {
			t.Errorf("refresh while every unit is held: %d (Retry-After %q) after %s, want 429 with Retry-After after %s", w.Code, w.Header().Get("Retry-After"), waited, preAuthMaxWait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		began = time.Now()
		if w := refresh(ctx, false); w.Code != http.StatusTooManyRequests || time.Since(began) >= preAuthMaxWait {
			t.Errorf("refresh of a client that is gone after 30 ms: %d after %s, want 429 before %s", w.Code, time.Since(began), preAuthMaxWait)
		}
		cancel()

		// A waiting refresh takes the first unit given back.
		waiting := make(chan int, 1)
		go func() { waiting <- refresh(context.Background(), false).Code }()
		time.Sleep(50 * time.Millisecond)
		close(hold)
		if code := <-waiting; code != http.StatusOK {
			t.Errorf("waiting refresh after the held ones finished: %d, want 200", code)
		}
		wg.Wait()
	})
}
