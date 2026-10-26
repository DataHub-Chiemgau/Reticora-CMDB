package middleware

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// fakeAPIKeys is an in-memory APIKeyAuthenticator for tests.
type fakeAPIKeys struct {
	keys map[string]*identity.APIKeyInfo
}

func (f fakeAPIKeys) Validate(_ context.Context, rawKey string) (*identity.APIKeyInfo, error) {
	if info, ok := f.keys[rawKey]; ok {
		return info, nil
	}
	return nil, errors.New("not found")
}

// TestAPIKeyAuthenticationPopulatesPrincipal proves that a service token
// authenticates through X-API-Key and produces an api_key principal whose
// organization scopes the tenant context.
func TestAPIKeyAuthenticationPopulatesPrincipal(t *testing.T) {
	keys := fakeAPIKeys{keys: map[string]*identity.APIKeyInfo{
		"rk_live_abc_secret": {
			ID:             "key-1",
			OrganizationID: "org-api",
			Scopes:         []identity.Permission{identity.PermCIRead},
		},
	}}

	handler := Chain(
		AuthMiddlewareWithAPIKeys(nil, keys),
		TenantMiddleware,
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("expected a principal in context")
			return
		}
		if principal.Type != identity.PrincipalTypeAPIKey {
			t.Errorf("expected api_key principal type, got %s", principal.Type)
		}
		if !principal.Has(identity.PermCIRead) {
			t.Error("expected the API key scopes on the principal")
		}
		if got := tenant.FromContext(r.Context()).OrganizationID; got != "org-api" {
			t.Errorf("expected org-api tenant, got %s", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("X-API-Key", "rk_live_abc_secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAPIKeyAuthenticationRejectsInvalidKeys proves unknown keys are 401.
func TestAPIKeyAuthenticationRejectsInvalidKeys(t *testing.T) {
	handler := Chain(
		AuthMiddlewareWithAPIKeys(nil, fakeAPIKeys{}),
		TenantMiddleware,
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("an invalid API key must not reach the handler")
	}))

	for _, key := range []string{"rk_live_unknown_secret", "bearer-token", ""} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for key %q, got %d", key, w.Code)
		}
	}
}

// knownKeyStore holds one stored key; ownersDown cannot read the owner's
// rights (database outage after the key was found).
type knownKeyStore struct{ key *identity.StoredAPIKey }

func (s knownKeyStore) LookupByPrefix(_ context.Context, prefix string) (*identity.StoredAPIKey, error) {
	if s.key != nil && s.key.KeyPrefix == prefix {
		return s.key, nil
	}
	return nil, nil
}

func (knownKeyStore) MarkUsed(context.Context, string, string) error { return nil }

type ownersDown struct{}

func (ownersDown) AccessGrants(context.Context, string, string) ([]identity.Grant, error) {
	return nil, errors.New("failed to connect to `user=reticora_runtime database=reticora`: dial tcp 10.0.0.5:5432: connect: connection refused")
}

// TestAPIKeyStoreOutageIsServiceUnavailable covers WP-449 (OPS-06, F-136):
// while the database is unreachable, a request with a well-formed API key is
// refused with 503 and Retry-After, not with 401 "invalid API key": the key
// is not known to be invalid, the client may send the request again, and
// the refusal does not count against the pre-auth budget of its IP (before
// WP-449 every request of an outage was a failed authentication, and after
// 20 of them the client's IP was locked out with 429). A malformed key and
// an unknown key stay 401. The 503 names no internals.
func TestAPIKeyStoreOutageIsServiceUnavailable(t *testing.T) {
	// The PostgreSQL key store on a database nobody listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()
	pool, err := pgxpool.New(context.Background(), "postgres://reticora_runtime:secret@"+closed+"/reticora?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	generated, err := identity.GenerateAPIKey(identity.APIKeyEnvironmentLive)
	if err != nil {
		t.Fatal(err)
	}

	reached := false
	chain := func(keys APIKeyAuthenticator) http.Handler {
		return Chain(
			PreAuthRateLimiter(2, cache.NewMemoryStore()),
			AuthMiddlewareWithAPIKeys(nil, keys),
			TenantMiddleware,
		)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusCreated)
		}))
	}
	call := func(h http.Handler, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{}`))
		req.RemoteAddr = "203.0.113.49:40000"
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	refused := func(w *httptest.ResponseRecorder, what string) {
		t.Helper()
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d %s, want 503", what, w.Code, w.Body.String())
		}
		if w.Header().Get("Retry-After") == "" {
			t.Errorf("%s: 503 without Retry-After", what)
		}
		for _, leak := range []string{"connect", "reticora_runtime", "127.0.0.1", "10.0.0.5", "dial", "database"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("%s: 503 names %q: %s", what, leak, w.Body.String())
			}
		}
	}

	outage := chain(identity.NewAPIKeyServiceWithStore(identity.NewPGAPIKeyStore(pool)))
	// Five requests from one IP with a pre-auth budget of two: none is a
	// failed authentication.
	for i := range 5 {
		refused(call(outage, generated.Plaintext), fmt.Sprintf("key store unreachable, request %d", i+1))
	}
	if w := call(outage, "rk_live_malformed"); w.Code != http.StatusUnauthorized {
		t.Errorf("malformed key during the outage: %d, want 401", w.Code)
	}

	// The key was found, its owner's rights cannot be read.
	stored := &identity.StoredAPIKey{ID: "k-449", OrganizationID: "org-449", KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
		Environment: generated.Environment, Permissions: []identity.Permission{identity.PermCIWrite}, CreatedBy: "user-449"}
	refused(call(chain(identity.NewAPIKeyServiceWithStore(knownKeyStore{key: stored}).WithOwnerAccess(ownersDown{})), generated.Plaintext),
		"owner rights unreadable")

	// A reachable store that does not know the key: 401 as before.
	other, err := identity.GenerateAPIKey(identity.APIKeyEnvironmentLive)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(chain(identity.NewAPIKeyServiceWithStore(knownKeyStore{key: stored})), other.Plaintext); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown key: %d, want 401", w.Code)
	}
	if reached {
		t.Error("a request whose key could not be checked reached the handler")
	}
}

// TestHeaderSpoofingDoesNotChangeTenant proves that X-Organization-ID and
// X-User-ID headers cannot move an authenticated principal into another
// tenant or change its identity.
func TestHeaderSpoofingDoesNotChangeTenant(t *testing.T) {
	handler := Chain(
		AuthMiddleware,
		TenantMiddleware,
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantInfo := tenant.FromContext(r.Context())
		if tenantInfo.OrganizationID != "org-token" {
			t.Errorf("tenant was spoofed by header: got %s", tenantInfo.OrganizationID)
		}
		if tenantInfo.UserID != "user-token" {
			t.Errorf("user was spoofed by header: got %s", tenantInfo.UserID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(t, Claims{
		Subject:        "user-token",
		OrganizationID: "org-token",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	req.Header.Set("X-Organization-ID", "org-victim")
	req.Header.Set("X-User-ID", "user-victim")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

// TestRateLimitKeyedByPrincipal proves the bucket key comes from the
// authenticated principal: rotating X-User-ID no longer resets the budget,
// and different principals get independent buckets.
func TestRateLimitKeyedByPrincipal(t *testing.T) {
	const rpm = 3

	handler := Chain(
		AuthMiddleware,
		TenantMiddleware,
		RateLimiter(rpm),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	token := testToken(t, Claims{
		Subject:        "user-1",
		OrganizationID: "org-1",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	})

	limited := false
	for i := 0; i < rpm*3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		// Rotating the spoofable header must not buy extra budget.
		req.Header.Set("X-User-ID", "rotating-"+strings.Repeat("x", i+1))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
		if w.Code != http.StatusNoContent {
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	if !limited {
		t.Fatal("expected the principal-scoped rate limit to trigger despite X-User-ID rotation")
	}
}

// TestRateLimitKeysUnauthenticatedByIP proves that public endpoints (no
// principal) are throttled per client IP rather than silently allowed.
func TestRateLimitKeysUnauthenticatedByIP(t *testing.T) {
	const rpm = 2

	handler := RateLimiter(rpm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	limited := false
	for i := 0; i < rpm*3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", nil)
		req.RemoteAddr = "203.0.113.10:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("expected unauthenticated requests to be rate limited by IP")
	}
}

// TestIdempotencyScopedByTenant proves two tenants sharing an
// Idempotency-Key never see each other's responses, and that a replay returns
// the original body.
func TestIdempotencyScopedByTenant(t *testing.T) {
	store := cache.NewMemoryStore()

	var calls int
	handler := Chain(
		AuthMiddleware,
		TenantMiddleware,
		IdempotencyWithStore(store),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"tenant":"` + tenant.FromContext(r.Context()).OrganizationID + `"}`))
	}))

	tokenFor := func(org string) string {
		return testToken(t, Claims{Subject: "u", OrganizationID: org, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	}
	post := func(org, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(`{"name":"server"}`))
		req.Header.Set("Authorization", "Bearer "+tokenFor(org))
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// First request executes the handler.
	first := post("org-a", "key-1")
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), "org-a") {
		t.Fatalf("unexpected first response %d %s", first.Code, first.Body.String())
	}

	// Replay for the same tenant must return the cached body, not re-execute.
	replay := post("org-a", "key-1")
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("expected the cached body on replay, got %d %s", replay.Code, replay.Body.String())
	}
	if replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("expected the Idempotency-Replayed marker on a replay")
	}

	// The same key for another tenant must execute independently.
	other := post("org-b", "key-1")
	if other.Code != http.StatusCreated || !strings.Contains(other.Body.String(), "org-b") {
		t.Fatalf("cross-tenant key collision poisoned the response: %d %s", other.Code, other.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 handler executions, got %d", calls)
	}
}
