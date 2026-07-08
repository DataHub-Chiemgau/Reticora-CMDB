package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestTenantMiddlewareFromHeader(t *testing.T) {
	handler := TenantMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantInfo := tenant.FromContext(r.Context())
		if tenantInfo.OrganizationID != "org-dev" {
			t.Fatalf("expected org-dev, got %s", tenantInfo.OrganizationID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("X-Organization-ID", "org-dev")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestTenantMiddlewareFromJWT(t *testing.T) {
	handler := TenantMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantInfo := tenant.FromContext(r.Context())
		if tenantInfo.OrganizationID != "org-jwt" {
			t.Fatalf("expected org-jwt, got %s", tenantInfo.OrganizationID)
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

func TestAuthMiddlewareSkipsLoginRoute(t *testing.T) {
	handler := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
}

func TestTenantMiddlewareFromSessionStyleJWT(t *testing.T) {
	handler := TenantMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func testRawToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
