package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func testRouter(t *testing.T) (*chi.Mux, Repository) {
	t.Helper()
	repo := NewMemoryRepository()
	router := chi.NewRouter()
	NewHandler(NewService(repo, testEncryptor(t))).RegisterRoutes(router)
	return router, repo
}

func withTenant(r *http.Request, orgID string) *http.Request {
	return r.WithContext(tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: orgID}))
}

func TestHandlerRequiresTenantContext(t *testing.T) {
	router, _ := testRouter(t)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/credentials", ""},
		{http.MethodPost, "/api/v1/credentials", `{"name":"n","kind":"ssh_password","secret":{"password":"p"}}`},
		{http.MethodGet, "/api/v1/credentials/some-id", ""},
		{http.MethodDelete, "/api/v1/credentials/some-id", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
		// A caller-supplied organization header must not be accepted as a
		// substitute for an authenticated tenant.
		req.Header.Set("X-Organization-ID", "org-1")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: expected 401 without tenant context, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestHandlerScopesCredentialsToAuthenticatedTenant(t *testing.T) {
	router, _ := testRouter(t)

	body := `{"name":"switch-admin","kind":"ssh_password","secret":{"username":"admin","password":"s3cret"}}`
	req := withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/credentials", bytes.NewBufferString(body)), "org-1")
	// The body may not be able to place the credential in another tenant either.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created credential: %v", err)
	}
	if created.OrganizationID != "org-1" {
		t.Fatalf("expected the credential to belong to org-1, got %s", created.OrganizationID)
	}

	// A different tenant must not see or read it.
	for _, path := range []string{
		"/api/v1/credentials/" + created.ID,
	} {
		req := withTenant(httptest.NewRequest(http.MethodGet, path, nil), "org-2")
		req.Header.Set("X-Organization-ID", "org-1")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as org-2: expected 404, got %d", path, rec.Code)
		}
	}

	req = withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/credentials", nil), "org-2")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var listed []Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("expected org-2 to see no credentials, got %d", len(listed))
	}

}

func TestHandlerCreateIgnoresOrganizationInBody(t *testing.T) {
	router, _ := testRouter(t)

	body := `{"organization_id":"org-victim","name":"n","kind":"ssh_password","secret":{"password":"p"}}`
	req := withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/credentials", bytes.NewBufferString(body)), "org-1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created credential: %v", err)
	}
	if created.OrganizationID != "org-1" {
		t.Fatalf("expected org-1 to win over the request body, got %s", created.OrganizationID)
	}
}

// TestCredentialSecretsNeverLeaveTheAPI covers WP-047 (SEC-01, COL-02): there
// is no decrypt route, and no API response carries the secret; the server
// still decrypts it in memory for delivery.
func TestCredentialSecretsNeverLeaveTheAPI(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	router := chi.NewRouter()
	NewHandler(svc).RegisterRoutes(router)

	body := `{"name":"switch-admin","kind":"ssh_password","secret":{"username":"admin","password":"plain-s3cret"}}`
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/credentials", bytes.NewBufferString(body)), "org-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	responses := []string{rec.Body.String()}
	for _, path := range []string{"/api/v1/credentials", "/api/v1/credentials/" + created.ID} {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodGet, path, nil), "org-1"))
		responses = append(responses, rec.Body.String())
	}
	for _, body := range responses {
		if strings.Contains(body, "plain-s3cret") {
			t.Errorf("API response contains the secret: %s", body)
		}
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/credentials/"+created.ID+"/decrypt", nil), "org-1"))
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("decrypt route answers %d, want no route", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "plain-s3cret") {
		t.Errorf("decrypt path returned the secret")
	}

	secret, err := svc.Decrypt(context.Background(), "org-1", created.ID)
	if err != nil || secret["password"] != "plain-s3cret" {
		t.Errorf("server-side decrypt: %v, %v", secret, err)
	}
}
