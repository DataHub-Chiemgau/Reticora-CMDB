package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestTenantMiddlewareRejectsHeaderOnlyRequests locks in the WS-2 tenant
// isolation rule: the X-Organization-ID header is client-controlled and must
// never establish a tenant context on its own.
func TestTenantMiddlewareRejectsHeaderOnlyRequests(t *testing.T) {
	handler := Chain(AuthMiddleware, TenantMiddleware)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a header-only request must not reach the handler")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("X-Organization-ID", "org-dev")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestTenantMiddlewareFromJWT(t *testing.T) {
	handler := Chain(AuthMiddleware, TenantMiddleware)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantInfo := tenant.FromContext(r.Context())
		if tenantInfo.OrganizationID != "org-jwt" {
			t.Fatalf("expected org-jwt, got %s", tenantInfo.OrganizationID)
		}
		scope, ok := database.TenantScopeFromContext(r.Context())
		if !ok || scope.OrgID != "org-jwt" || scope.Clients.IsAll() || len(scope.Clients.IDs()) != 1 || scope.Clients.IDs()[0] != "client-1" {
			t.Fatalf("expected the principal's scope in the context, got %+v (%v)", scope, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(t, Claims{OrganizationID: "org-jwt", ClientID: "client-1"}))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

// TestTenantScopeFor: no client scope means org-wide; a client scope is a
// restricted list, and a scope without ids grants nothing (E-08).
func TestTenantScopeFor(t *testing.T) {
	const org = "aaaa1111-0000-4000-8000-000000000001"
	const c1, c2 = "aaaa1111-0000-4000-8000-0000000000a1", "aaaa1111-0000-4000-8000-0000000000a2"
	cases := []struct {
		name    string
		clients string
		all     bool
		ids     []string
	}{
		{"org-wide", "", true, nil},
		{"one client", c1, false, []string{c1}},
		{"several clients", c1 + ", " + c2, false, []string{c1, c2}},
		{"separators only", " , ", false, nil},
	}
	for _, tc := range cases {
		scope := TenantScopeFor(&identity.Principal{OrganizationID: org, Subject: "user-1", ClientScope: tc.clients})
		if err := scope.Validate(); err != nil {
			t.Fatalf("%s: scope is not complete: %v", tc.name, err)
		}
		if scope.UserID != "user-1" || !scope.Sites.IsAll() || !scope.Teams.IsAll() {
			t.Fatalf("%s: unexpected scope %+v", tc.name, scope)
		}
		got := scope.Clients.IDs()
		if scope.Clients.IsAll() != tc.all || len(got) != len(tc.ids) {
			t.Fatalf("%s: clients = %v (all %v), want %v (all %v)", tc.name, got, scope.Clients.IsAll(), tc.ids, tc.all)
		}
		for i := range got {
			if got[i] != tc.ids[i] {
				t.Fatalf("%s: clients = %v, want %v", tc.name, got, tc.ids)
			}
		}
	}
}

// WP-009: a session with resolved role scopes is taken over per dimension; a
// client-only user is never org-wide.
func TestTenantScopeForResolvedScope(t *testing.T) {
	const org = "aaaa1111-0000-4000-8000-000000000001"
	const c1, s1 = "aaaa1111-0000-4000-8000-0000000000a1", "aaaa1111-0000-4000-8000-0000000000b1"
	resolved := identity.Scope{
		Clients: identity.ScopeSet{IDs: []string{c1}},
		Sites:   identity.ScopeSet{IDs: []string{s1}},
		Teams:   identity.ScopeSet{All: true},
	}
	// The legacy client scope string is ignored when a resolved scope exists.
	scope := TenantScopeFor(&identity.Principal{OrganizationID: org, Subject: "user-1", ClientScope: "", Scope: &resolved})
	if err := scope.Validate(); err != nil {
		t.Fatalf("scope is not complete: %v", err)
	}
	if scope.Clients.IsAll() || len(scope.Clients.IDs()) != 1 || scope.Clients.IDs()[0] != c1 {
		t.Fatalf("clients = %v, want only %s", scope.Clients.IDs(), c1)
	}
	if scope.Sites.IsAll() || scope.Sites.IDs()[0] != s1 || !scope.Teams.IsAll() {
		t.Fatalf("unexpected scope %+v", scope)
	}

	none := TenantScopeFor(&identity.Principal{OrganizationID: org, Subject: "user-1", Scope: &identity.Scope{}})
	if err := none.Validate(); err != nil {
		t.Fatalf("empty scope must stay decided: %v", err)
	}
	if none.Clients.IsAll() || len(none.Clients.IDs()) != 0 || none.Sites.IsAll() || none.Teams.IsAll() {
		t.Fatalf("principal without grants must get no scope, got %+v", none)
	}
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddlewareSkipsPublicAuthRoutes(t *testing.T) {
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{
		"/api/v1/auth/config",
		"/api/v1/auth/callback",
		"/api/v1/auth/refresh",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Fatalf("expected 204 for %s, got %d", path, w.Code)
		}
	}
}

func TestTenantMiddlewareFromSessionStyleJWT(t *testing.T) {
	handler := Chain(AuthMiddleware, TenantMiddleware)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantInfo := tenant.FromContext(r.Context())
		if tenantInfo.OrganizationID != "org-session" {
			t.Fatalf("expected org-session, got %s", tenantInfo.OrganizationID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+testRawToken(t, map[string]any{
		"sub":    "user-1",
		"org_id": "org-session",
		"exp":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func testToken(t *testing.T, claims Claims) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// TestAuthMiddlewareRejectsAlgNoneAndHMACConfusion locks in the algorithm
// pinning of the development parser: even without a verifier a token whose
// header does not declare RS256 must be rejected.
func TestAuthMiddlewareRejectsAlgNoneAndHMACConfusion(t *testing.T) {
	claims := map[string]any{
		"sub":             "user-1",
		"organization_id": "org-1",
		"exp":             time.Now().Add(time.Hour).Unix(),
	}

	for _, alg := range []string{"none", "HS256", ""} {
		header, err := json.Marshal(map[string]any{"alg": alg, "typ": "JWT"})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		token := base64.RawURLEncoding.EncodeToString(header) + "." +
			base64.RawURLEncoding.EncodeToString(payload) + ".sig"

		handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("alg=%q token must not reach the handler", alg)
		}))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for alg=%q, got %d", alg, w.Code)
		}
	}
}

func testRawToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// TestTenantMiddlewareIgnoresHeaderWhenTokenCarriesOrganization pins the tenant
// isolation rule: a caller holding a valid token for one organization must not
// be able to act on another organization by sending an X-Organization-ID
// header. The token claim always wins.
func TestTenantMiddlewareIgnoresHeaderWhenTokenCarriesOrganization(t *testing.T) {
	handler := Chain(AuthMiddleware, TenantMiddleware)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantInfo := tenant.FromContext(r.Context())
		if tenantInfo.OrganizationID != "org-jwt" {
			t.Fatalf("expected the token organization org-jwt, got %s", tenantInfo.OrganizationID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(t, Claims{OrganizationID: "org-jwt"}))
	req.Header.Set("X-Organization-ID", "org-victim")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

// TestPreAuthLimitCountsFailedAuthentication covers WP-050 and WP-462
// (AUT-10, API-04): the pre-auth limit sits in front of authentication,
// counts pre-auth endpoints, failed refreshes and failed authentications
// (invalid API keys) per client IP, and once the budget is spent even a
// valid key from that IP is refused; other IPs keep their own budget.
// Successful refreshes do not count (F-063).
func TestPreAuthLimitCountsFailedAuthentication(t *testing.T) {
	store := cache.NewMemoryStore()
	keys := fakeAPIKeys{keys: map[string]*identity.APIKeyInfo{
		"valid": {ID: "key-1", OrganizationID: "org-1", Scopes: []identity.Permission{identity.PermCIRead}},
	}}
	// The handler answers 401 for a refresh with an invalid cookie.
	handler := Chain(
		PreAuthRateLimiter(5, store),
		AuthMiddlewareWithAPIKeys(nil, keys),
		TenantMiddleware,
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Refresh") == "invalid" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	callWith := func(ip, path, apiKey, refresh string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = ip + ":40000"
		if apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
		if refresh != "" {
			req.Header.Set("X-Test-Refresh", refresh)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	call := func(ip, path, apiKey string) int {
		t.Helper()
		return callWith(ip, path, apiKey, "")
	}
	awaitFreshWindow()

	if code := call("203.0.113.1", "/api/v1/cis", "valid"); code != http.StatusOK {
		t.Fatalf("valid key: %d", code)
	}
	for i := 0; i < 5; i++ {
		if code := call("203.0.113.1", "/api/v1/cis", "guess"); code != http.StatusUnauthorized {
			t.Fatalf("invalid key attempt %d: %d, want 401", i, code)
		}
	}
	if code := call("203.0.113.1", "/api/v1/cis", "guess"); code != http.StatusTooManyRequests {
		t.Errorf("attempt beyond the budget: %d, want 429", code)
	}
	if code := call("203.0.113.1", "/api/v1/cis", "valid"); code != http.StatusTooManyRequests {
		t.Errorf("valid key from the blocked IP: %d, want 429", code)
	}
	if code := call("203.0.113.2", "/api/v1/cis", "valid"); code != http.StatusOK {
		t.Errorf("valid key from another IP: %d, want 200", code)
	}
	// Successful requests do not consume the budget.
	for i := 0; i < 10; i++ {
		if code := call("203.0.113.3", "/api/v1/cis", "valid"); code != http.StatusOK {
			t.Fatalf("valid request %d: %d", i, code)
		}
	}
	// Failed refreshes (handler 401) count: the sixth pre-auth request of
	// the IP is refused.
	for i := 0; i < 5; i++ {
		if code := callWith("203.0.113.4", "/api/v1/auth/refresh", "", "invalid"); code != http.StatusUnauthorized {
			t.Fatalf("failed refresh %d: %d, want 401", i, code)
		}
	}
	if code := call("203.0.113.4", "/api/v1/auth/callback", ""); code != http.StatusTooManyRequests {
		t.Errorf("sixth pre-auth request after 5 failed refreshes: %d, want 429", code)
	}
	// Successful refreshes (200) do not count: the sixth request passes,
	// and so do the next ones.
	for i := 0; i < 5; i++ {
		if code := call("203.0.113.5", "/api/v1/auth/refresh", ""); code != http.StatusOK {
			t.Fatalf("successful refresh %d: %d, want 200", i, code)
		}
	}
	if code := call("203.0.113.5", "/api/v1/auth/refresh", ""); code != http.StatusOK {
		t.Errorf("sixth request after 5 successful refreshes: %d, want 200", code)
	}
	// The login callback still counts every request.
	for i := 0; i < 5; i++ {
		if code := call("203.0.113.5", "/api/v1/auth/callback", ""); code != http.StatusOK {
			t.Fatalf("login callback %d: %d, want 200", i, code)
		}
	}
	if code := call("203.0.113.5", "/api/v1/auth/refresh", ""); code != http.StatusTooManyRequests {
		t.Errorf("refresh after 5 login callbacks: %d, want 429", code)
	}
	if code := call("203.0.113.4", "/api/v1/auth/config", ""); code == http.StatusTooManyRequests {
		t.Error("public configuration endpoint was limited")
	}
}

// TestClientIPTrustsOnlyConfiguredProxies: X-Real-IP names the client only
// when the peer is a trusted proxy.
func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	defer func() { _ = SetTrustedProxies("127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7") }()
	if err := SetTrustedProxies("10.0.0.0/8"); err != nil {
		t.Fatal(err)
	}
	ip := func(peer, real string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = peer + ":1234"
		req.Header.Set("X-Real-IP", real)
		return clientIP(req)
	}
	if got := ip("10.0.0.7", "198.51.100.9"); got != "198.51.100.9" {
		t.Errorf("trusted proxy: %s", got)
	}
	if got := ip("203.0.113.5", "198.51.100.9"); got != "203.0.113.5" {
		t.Errorf("untrusted peer spoofing X-Real-IP: %s", got)
	}
	if err := SetTrustedProxies("not-a-cidr"); err == nil {
		t.Error("invalid CIDR accepted")
	}
}

// TestRateLimitAndRecoveryAnswerWithTheProblemWriter covers API-03: the 429
// of the rate limiter and the 500 of Recovery are problem+json with the
// request id that RequestID put on the response — not a value the client
// sent in its own X-Request-ID header.
func TestRateLimitAndRecoveryAnswerWithTheProblemWriter(t *testing.T) {
	limited := Chain(RequestID, RateLimiter(1))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	panicking := Chain(RequestID, Recovery)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	check := func(name string, h http.Handler, requests, want int) {
		t.Helper()
		var w *httptest.ResponseRecorder
		for i := 0; i < requests; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
			req.RemoteAddr = "192.0.2.10:1234"
			req.Header.Set("X-Request-ID", "client-chosen")
			w = httptest.NewRecorder()
			h.ServeHTTP(w, req)
		}
		if w.Code != want {
			t.Fatalf("%s: status %d, want %d", name, w.Code, want)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type %q", name, ct)
		}
		var body struct {
			TraceID string `json:"trace_id"`
			Status  int    `json:"status"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.TraceID == "" || body.TraceID == "client-chosen" || body.TraceID != w.Header().Get("X-Request-ID") || body.Status != want {
			t.Errorf("%s: trace_id %q, X-Request-ID %q, status %d", name, body.TraceID, w.Header().Get("X-Request-ID"), body.Status)
		}
	}
	check("rate limit", limited, 2, http.StatusTooManyRequests)
	check("recovery", panicking, 1, http.StatusInternalServerError)
}

// intentStore is a RouteAuditor over an in-memory intent table: Complete
// and the sweep take an intent like DELETE ... RETURNING, so only the one
// that finds it writes the entry.
type intentStore struct {
	mu           sync.Mutex
	open         map[string]audit.RouteIntent
	entries      []audit.Entry
	failIntend   bool
	failComplete bool
	seq          int
	completeCtx  context.Context
}

func (s *intentStore) Intend(_ context.Context, in *audit.RouteIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failIntend {
		return errors.New("audit store down")
	}
	s.seq++
	in.ID = fmt.Sprint(s.seq)
	s.open[in.ID] = *in
	return nil
}

// take is the DELETE ... RETURNING of the intent.
func (s *intentStore) take(id string) (audit.RouteIntent, bool) {
	in, ok := s.open[id]
	delete(s.open, id)
	return in, ok
}

func (s *intentStore) Complete(ctx context.Context, in *audit.RouteIntent, e *audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCtx = ctx
	if s.failComplete {
		return errors.New("audit store down")
	}
	if _, ok := s.take(in.ID); ok {
		s.entries = append(s.entries, *e)
	}
	return nil
}

func (s *intentStore) Discard(_ context.Context, in *audit.RouteIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.take(in.ID)
	return nil
}

func (s *intentStore) RecordRoute(_ context.Context, e *audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, *e)
	return nil
}

// sweep takes every open intent and writes its entry with outcome unknown.
func (s *intentStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, in := range s.open {
		s.take(id)
		s.entries = append(s.entries, audit.Entry{Action: in.Action, RequestID: in.RequestID,
			Changes: map[string]interface{}{"outcome": audit.OutcomeUnknown}})
	}
}

// TestAuditRouteStoresTheIntentBeforeTheHandler covers the intent flow of
// the route audit (AUD-07, WP-504): the intent is stored before the
// handler runs, and without it the request is refused with 503 and the
// handler never runs; a successful response completes the intent with one
// entry; a refused request or one a domain recorder audited discards it; a
// failed completion keeps it for the scheduler, and a completion that
// finds no intent (the scheduler took it) writes nothing. A route with a
// domain recorder stores no intent.
func TestAuditRouteStoresTheIntentBeforeTheHandler(t *testing.T) {
	const org, user = "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-aaaaaaaaaaaa"
	store := &intentStore{open: map[string]audit.RouteIntent{}}
	ran := 0
	var seenIntent int
	handler := func(status int, recorded bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ran++
			store.mu.Lock()
			seenIntent = len(store.open)
			store.mu.Unlock()
			if recorded {
				audit.MarkRecorded(r.Context())
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}`))
		}
	}
	serve := func(mode AuditMode, h http.HandlerFunc) *httptest.ResponseRecorder {
		ran, seenIntent = 0, 0
		r := chi.NewRouter()
		r.With(AuditRoute(http.MethodPost, "/api/v1/vrfs", store, mode, "")).Post("/api/v1/vrfs", h)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/vrfs", strings.NewReader(`{"name":"a","rd":"1:1"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPrincipal(req.Context(), identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser})
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: user})
		w := httptest.NewRecorder()
		RequestID(r).ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	reset := func() []audit.Entry {
		store.mu.Lock()
		defer store.mu.Unlock()
		out := store.entries
		store.entries = nil
		return out
	}

	// The audit store is down: 503 with Retry-After, the handler never runs.
	store.failIntend = true
	if w := serve(AuditIntent, handler(http.StatusCreated, false)); w.Code != http.StatusServiceUnavailable ||
		w.Header().Get("Retry-After") == "" || ran != 0 || len(reset()) != 0 {
		t.Fatalf("intent failed: %d, handler ran %d times", w.Code, ran)
	}
	store.failIntend = false

	// Success: the intent exists while the handler runs, then one entry.
	w := serve(AuditIntent, handler(http.StatusCreated, false))
	entries := reset()
	if w.Code != http.StatusCreated || seenIntent != 1 || len(store.open) != 0 || len(entries) != 1 {
		t.Fatalf("success: %d, intents during the handler %d, open %d, entries %+v", w.Code, seenIntent, len(store.open), entries)
	}
	e := entries[0]
	if e.OrganizationID != org || e.ActorID != user || e.Action != "vrfs.create" || e.ResourceID != "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" ||
		e.RequestID != w.Header().Get("X-Request-ID") || e.Changes["route"] != "POST /api/v1/vrfs" || e.Changes["status"] != http.StatusCreated ||
		fmt.Sprint(e.Changes["fields"]) != "[name rd]" {
		t.Errorf("entry: %+v", e)
	}
	if store.completeCtx == nil || store.completeCtx.Done() != nil {
		t.Errorf("the completion runs on a context the client cannot cancel")
	}

	// Refused or audited by a domain recorder: the intent goes, no entry.
	for _, tc := range []struct {
		status   int
		recorded bool
	}{{http.StatusBadRequest, false}, {http.StatusConflict, false}, {http.StatusMultipleChoices, false}, {http.StatusCreated, true}} {
		if serve(AuditIntent, handler(tc.status, tc.recorded)); len(store.open) != 0 || len(reset()) != 0 {
			t.Errorf("status %d recorded %v: open intents %d", tc.status, tc.recorded, len(store.open))
		}
	}

	// The completion fails: the intent stays and the scheduler writes the
	// entry with outcome unknown, exactly once.
	store.failComplete = true
	serve(AuditIntent, handler(http.StatusCreated, false))
	store.failComplete = false
	if len(store.open) != 1 || len(reset()) != 0 {
		t.Fatalf("failed completion: open %d", len(store.open))
	}
	var kept audit.RouteIntent
	for _, in := range store.open {
		kept = in
	}
	if kept.ActorID != user || kept.Action != "vrfs.create" || kept.Route != "POST /api/v1/vrfs" || kept.RequestID == "" ||
		strings.Join(kept.Fields, ",") != "name,rd" {
		t.Errorf("kept intent: %+v", kept)
	}
	store.sweep()
	// A late completion of the swept intent writes no second entry.
	late := kept
	if err := store.Complete(context.Background(), &late, &audit.Entry{Action: "vrfs.create"}); err != nil {
		t.Fatal(err)
	}
	if entries = reset(); len(entries) != 1 || entries[0].Changes["outcome"] != audit.OutcomeUnknown || len(store.open) != 0 {
		t.Errorf("after the sweep and a late completion: %+v", entries)
	}

	// A route with a domain recorder: no intent; the route entry only when
	// the recorder wrote none.
	store.failIntend = true
	if w = serve(AuditDomain, handler(http.StatusCreated, true)); w.Code != http.StatusCreated || seenIntent != 0 || len(reset()) != 0 {
		t.Errorf("domain route, recorded: %d", w.Code)
	}
	if w = serve(AuditDomain, handler(http.StatusCreated, false)); w.Code != http.StatusCreated || len(reset()) != 1 {
		t.Errorf("domain route, not recorded: %d", w.Code)
	}
	if w = serve(AuditNone, handler(http.StatusCreated, false)); w.Code != http.StatusCreated || len(reset()) != 0 {
		t.Errorf("exempt route: %d", w.Code)
	}
}

// snapshotStore is an intentStore that reads snapshots of one in-memory
// row per id.
type snapshotStore struct {
	*intentStore
	rows               map[string]map[string]any
	reads              []string
	failRead, failNext bool
}

func (s *snapshotStore) RouteSnapshot(ctx context.Context, orgID string, spec *audit.RouteSnapshot, keys []any) (map[string]any, string, error) {
	if _, scoped := database.TenantScopeFromContext(ctx); !scoped {
		return nil, "", errors.New("no tenant scope")
	}
	id := fmt.Sprint(keys[0])
	s.reads = append(s.reads, id)
	if s.failRead {
		return nil, "", errors.New("snapshot down")
	}
	if s.failNext {
		s.failRead = true
	}
	row := s.rows[id]
	if row == nil {
		return nil, "", nil
	}
	copied := map[string]any{}
	for k, v := range row {
		copied[k] = v
	}
	return copied, spec.Table, nil
}

// TestAuditRouteStoresOldAndNewValues covers the snapshots of the route
// audit (AUD-07, WP-505): a route of the register reads its row in the
// request's tenant scope before the handler and after a successful
// response and stores {field, old, new} per changed column; a create reads
// the row of the response's id, a delete keeps the old values. A failed
// read before the handler refuses the request (503, intent discarded); a
// failed read afterwards marks the values unavailable. Without a tenant
// scope or for a route with a domain recorder nothing is read.
func TestAuditRouteStoresOldAndNewValues(t *testing.T) {
	const org, user = "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-aaaaaaaaaaaa"
	const vlan = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	store := &snapshotStore{intentStore: &intentStore{open: map[string]audit.RouteIntent{}}}
	ran, readsBefore := 0, 0
	serve := func(method, pattern, path string, mode AuditMode, scoped bool, h func(w http.ResponseWriter)) *httptest.ResponseRecorder {
		ran, readsBefore, store.reads = 0, 0, nil
		r := chi.NewRouter()
		r.With(AuditRoute(method, pattern, store, mode, "")).Method(method, pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			ran++
			readsBefore = len(store.reads)
			h(w)
		}))
		req := httptest.NewRequest(method, path, strings.NewReader(`{"name":"b"}`))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPrincipal(req.Context(), identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser})
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: user})
		if scoped {
			scope := database.OrgWideScope(org, user)
			ctx = database.ContextWithTenantScope(ctx, &scope)
		}
		w := httptest.NewRecorder()
		RequestID(r).ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	take := func() []audit.Entry {
		out := store.entries
		store.entries = nil
		return out
	}
	rename := func(w http.ResponseWriter) {
		store.rows[vlan]["name"] = "b"
		w.WriteHeader(http.StatusOK)
	}
	values := func(e audit.Entry) string {
		raw, _ := json.Marshal(e.Changes["values"])
		return string(raw)
	}

	// Update: the old value is read before the handler, the new one after.
	store.rows = map[string]map[string]any{vlan: {"id": vlan, "name": "a", "vlan_id": 7.0}}
	w := serve(http.MethodPatch, "/api/v1/vlans/{id}", "/api/v1/vlans/"+vlan, AuditIntent, true, rename)
	entries := take()
	if w.Code != http.StatusOK || readsBefore != 1 || len(store.reads) != 2 || len(entries) != 1 ||
		values(entries[0]) != `[{"field":"name","new":"b","old":"a"}]` {
		t.Fatalf("update: %d, reads before the handler %d, reads %v, entries %+v", w.Code, readsBefore, store.reads, entries)
	}

	// Create: no read before the handler; the row of the response's id.
	serve(http.MethodPost, "/api/v1/vlans", "/api/v1/vlans", AuditIntent, true, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + vlan + `"}`))
	})
	if entries = take(); readsBefore != 0 || len(entries) != 1 ||
		values(entries[0]) != `[{"field":"id","new":"`+vlan+`","old":null},{"field":"name","new":"b","old":null},{"field":"vlan_id","new":7,"old":null}]` {
		t.Errorf("create: reads before %d, entries %+v", readsBefore, entries)
	}

	// Delete: the old values.
	w = serve(http.MethodDelete, "/api/v1/vlans/{id}", "/api/v1/vlans/"+vlan, AuditIntent, true, func(w http.ResponseWriter) {
		delete(store.rows, vlan)
		w.WriteHeader(http.StatusNoContent)
	})
	if entries = take(); w.Code != http.StatusNoContent || len(entries) != 1 ||
		values(entries[0]) != `[{"field":"id","new":null,"old":"`+vlan+`"},{"field":"name","new":null,"old":"b"},{"field":"vlan_id","new":null,"old":7}]` {
		t.Errorf("delete: %d %+v", w.Code, entries)
	}

	// The read before the handler fails: 503, the handler never runs, the
	// intent is gone.
	store.rows = map[string]map[string]any{vlan: {"id": vlan, "name": "a"}}
	store.failRead = true
	w = serve(http.MethodPatch, "/api/v1/vlans/{id}", "/api/v1/vlans/"+vlan, AuditIntent, true, rename)
	if w.Code != http.StatusServiceUnavailable || ran != 0 || len(store.open) != 0 || len(take()) != 0 {
		t.Errorf("failed read before the handler: %d, handler ran %d, open intents %d", w.Code, ran, len(store.open))
	}
	// The read after the response fails: the entry says so.
	store.failRead, store.failNext = false, true
	serve(http.MethodPatch, "/api/v1/vlans/{id}", "/api/v1/vlans/"+vlan, AuditIntent, true, rename)
	store.failRead, store.failNext = false, false
	if entries = take(); len(entries) != 1 || entries[0].Changes["values_unavailable"] != true || entries[0].Changes["values"] != nil {
		t.Errorf("failed read after the response: %+v", entries)
	}

	// No tenant scope, a route with a domain recorder, a route without a
	// table: no read.
	for _, tc := range []struct {
		method, pattern, path string
		mode                  AuditMode
		scoped                bool
	}{
		{http.MethodPatch, "/api/v1/vlans/{id}", "/api/v1/vlans/" + vlan, AuditIntent, false},
		{http.MethodPatch, "/api/v1/vlans/{id}", "/api/v1/vlans/" + vlan, AuditDomain, true},
		{http.MethodPost, "/api/v1/search/reindex", "/api/v1/search/reindex", AuditIntent, true},
	} {
		serve(tc.method, tc.pattern, tc.path, tc.mode, tc.scoped, rename)
		if entries = take(); len(store.reads) != 0 || len(entries) != 1 || entries[0].Changes["values"] != nil {
			t.Errorf("%s %s mode %d scoped %v: reads %v, entries %+v", tc.method, tc.pattern, tc.mode, tc.scoped, store.reads, entries)
		}
	}
}
