package credential

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		{http.MethodGet, "/api/v1/credentials/some-id/decrypt", ""},
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

	// A different tenant must not see, read or decrypt it.
	for _, path := range []string{
		"/api/v1/credentials/" + created.ID,
		"/api/v1/credentials/" + created.ID + "/decrypt",
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

	// The owning tenant can decrypt.
	req = withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/credentials/"+created.ID+"/decrypt", nil), "org-1")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the owning tenant, got %d: %s", rec.Code, rec.Body.String())
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
