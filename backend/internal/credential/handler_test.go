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
		getReq := withTenant(httptest.NewRequest(http.MethodGet, path, nil), "org-2")
		getReq.Header.Set("X-Organization-ID", "org-1")
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusNotFound {
			t.Errorf("GET %s as org-2: expected 404, got %d", path, getRec.Code)
		}
	}

	req = withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/credentials", nil), "org-2")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	// The list is a page {data, next_cursor}; an empty one is "data": []
	// (API-02, F-214).
	var listed struct {
		Data []Credential `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listed.Data == nil || len(listed.Data) != 0 {
		t.Errorf("expected org-2 to see data: [], got %s", rec.Body.String())
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

// TestHandlerCreateCredentialKinds covers WP-379 (E-68, DIS-04, COL-02): the
// NAS account is a credential kind of its own (201); an unknown or missing
// kind is answered with 422 naming the field and stores nothing.
func TestHandlerCreateCredentialKinds(t *testing.T) {
	router, repo := testRouter(t)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/credentials", bytes.NewBufferString(body)), "org-1"))
		return rec
	}

	rec := post(`{"name":"nas-admin","kind":"nas","secret":{"username":"dsm-admin","password":"dsm-s3cret"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("kind nas: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Kind != KindNAS || strings.Contains(rec.Body.String(), "dsm-s3cret") {
		t.Errorf("created credential = %s, want kind nas without the secret", rec.Body.String())
	}

	for _, body := range []string{
		`{"name":"n","kind":"telnet","secret":{"password":"p"}}`,
		// Protocol names are no kinds: "ssh" is ssh_password or ssh_key.
		`{"name":"n","kind":"ssh","secret":{"password":"p"}}`,
		`{"name":"n","kind":"NAS","secret":{"password":"p"}}`,
		`{"name":"n","secret":{"password":"p"}}`,
	} {
		rec := post(body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: expected 422, got %d: %s", body, rec.Code, rec.Body.String())
			continue
		}
		var problem struct {
			Field string `json:"field"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil || problem.Field != "kind" {
			t.Errorf("%s: problem %s, want field kind", body, rec.Body.String())
		}
	}

	stored, err := repo.List(context.Background(), "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Kind != KindNAS {
		t.Errorf("stored credentials = %+v, want only the nas credential", stored)
	}
}

// createCredential creates a credential of org-1 through the API.
func createCredential(t *testing.T, router *chi.Mux, body string) Credential {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/credentials", bytes.NewBufferString(body)), "org-1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var cred Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &cred); err != nil {
		t.Fatal(err)
	}
	return cred
}

// TestHandlerReplaceSecretReturnsNoSecret covers CRD-05 (WP-889): PUT
// /credentials/{id}/secret answers the metadata with secret_version 2 and
// rotated_at, neither the old nor the new value; an empty secret is 422, a
// foreign or unknown id 404.
func TestHandlerReplaceSecretReturnsNoSecret(t *testing.T) {
	router, _ := testRouter(t)
	cred := createCredential(t, router, `{"name":"sw","kind":"ssh_password","secret":{"username":"admin","password":"old-value-889"}}`)
	if cred.SecretVersion != 1 || cred.RotatedAt != nil {
		t.Fatalf("created %+v, want secret_version 1 without rotated_at", cred)
	}
	put := func(org, id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPut, "/api/v1/credentials/"+id+"/secret", bytes.NewBufferString(body)), org))
		return rec
	}
	rec := put("org-1", cred.ID, `{"secret":{"username":"admin","password":"new-value-889"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "old-value-889") || strings.Contains(body, "new-value-889") ||
		strings.Contains(body, `"password"`) || strings.Contains(body, `"secret"`) {
		t.Errorf("the answer carries the secret: %s", body)
	}
	var replaced Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.SecretVersion != 2 || replaced.RotatedAt == nil || replaced.ID != cred.ID {
		t.Errorf("replaced %+v, want secret_version 2 with rotated_at", replaced)
	}
	if rec = put("org-1", cred.ID, `{"secret":{}}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"secret"`) {
		t.Errorf("empty secret: %d %s", rec.Code, rec.Body.String())
	}
	if rec = put("org-2", cred.ID, `{"secret":{"password":"x"}}`); rec.Code != http.StatusNotFound {
		t.Errorf("foreign organization: %d", rec.Code)
	}
	if rec = put("org-1", "00000000-0000-4000-8000-000000000000", `{"secret":{"password":"x"}}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}
}

// TestHandlerPatchRejectsImmutableFields covers WP-889: PATCH changes name
// and max_age_days (null removes it); kind, client_id, scope and secret are
// 422 with their field, an age limit outside 1–3650 too.
func TestHandlerPatchRejectsImmutableFields(t *testing.T) {
	router, _ := testRouter(t)
	cred := createCredential(t, router, `{"name":"sw","kind":"ssh_password","secret":{"password":"p"}}`)
	patch := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPatch, "/api/v1/credentials/"+cred.ID, bytes.NewBufferString(body)), "org-1"))
		return rec
	}
	for body, field := range map[string]string{
		`{"kind":"snmp_v2c"}`:         "kind",
		`{"client_id":"c-1"}`:         "client_id",
		`{"scope":"x"}`:               "scope",
		`{"secret":{"password":"x"}}`: "secret",
		`{"max_age_days":0}`:          "max_age_days",
		`{"max_age_days":3651}`:       "max_age_days",
		`{"name":""}`:                 "name",
	} {
		if rec := patch(body); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 on %s", body, rec.Code, rec.Body.String(), field)
		}
	}
	rec := patch(`{"name":"core-switch","max_age_days":90}`)
	var got Credential
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Name != "core-switch" ||
		got.MaxAgeDays == nil || *got.MaxAgeDays != 90 || got.SecretVersion != 1 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	rec = patch(`{"max_age_days":null}`)
	got = Credential{}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.MaxAgeDays != nil || got.Name != "core-switch" {
		t.Errorf("remove the age limit: %d %s", rec.Code, rec.Body.String())
	}
}

// TestHandlerProbeKindsSecretFieldErrors covers WP-967 AK2 on HTTP level:
// POST /credentials and PUT /credentials/{id}/secret answer a secret
// without the members of a probe kind with 422 and field secret.<member>;
// the body never carries a value of the secret.
func TestHandlerProbeKindsSecretFieldErrors(t *testing.T) {
	router, _ := testRouter(t)
	const value = "probe-secret-value-967"
	fieldOf := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		var problem struct {
			Field string `json:"field"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatalf("problem %s: %v", rec.Body.String(), err)
		}
		return problem.Field
	}
	for _, tc := range []struct{ body, field string }{
		{`{"name":"h","kind":"http","secret":{"password":"` + value + `"}}`, "secret.username"},
		{`{"name":"a","kind":"acs","secret":{"username":"u","password":"` + value + `","token":"` + value + `"}}`, "secret.token"},
		{`{"name":"g","kind":"gnmi","secret":{"client_cert":"` + value + `","client_key":"` + value + `"}}`, "secret.client_cert"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/v1/credentials", bytes.NewBufferString(tc.body)), "org-1"))
		if rec.Code != http.StatusUnprocessableEntity || fieldOf(rec) != tc.field {
			t.Errorf("POST %s: %d %s, want 422 %s", tc.body, rec.Code, rec.Body.String(), tc.field)
		}
		if strings.Contains(rec.Body.String(), value) {
			t.Errorf("POST %s: the answer carries the secret: %s", tc.body, rec.Body.String())
		}
	}

	cred := createCredential(t, router, `{"name":"gnmi-967","kind":"gnmi","secret":{"username":"u","password":"p"}}`)
	for _, tc := range []struct{ body, field string }{
		{`{"secret":{"username":"u","password":"p","client_cert":"` + value + `","client_key":"` + value + `"}}`, "secret.client_cert"},
		{`{"secret":{"username":"u","password":"p","ca":"` + value + `"}}`, "secret.ca"},
		{`{"secret":{"client_cert":"` + value + `"}}`, "secret.client_key"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPut, "/api/v1/credentials/"+cred.ID+"/secret", bytes.NewBufferString(tc.body)), "org-1"))
		if rec.Code != http.StatusUnprocessableEntity || fieldOf(rec) != tc.field {
			t.Errorf("PUT %s: %d %s, want 422 %s", tc.body, rec.Code, rec.Body.String(), tc.field)
		}
		if strings.Contains(rec.Body.String(), value) {
			t.Errorf("PUT %s: the answer carries the secret: %s", tc.body, rec.Body.String())
		}
	}
}
