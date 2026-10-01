package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
