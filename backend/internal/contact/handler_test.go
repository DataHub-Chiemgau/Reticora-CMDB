package contact

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func do(mux chi.Router, method, path, body, org string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if org != "" {
		r = r.WithContext(tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: org}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func setup() chi.Router {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

func TestContactCRUD(t *testing.T) {
	mux := setup()

	w := do(mux, "POST", "/api/v1/contacts", `{"display_name":"Jane","email":"j@x.io"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c Contact
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.ID == "" {
		t.Fatal("expected id")
	}

	// validation
	if w := do(mux, "POST", "/api/v1/contacts", `{}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing display_name: expected 400, got %d", w.Code)
	}

	if w := do(mux, "GET", "/api/v1/contacts/"+c.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/contacts/"+c.ID, `{"phone":"123"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/contacts/"+c.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestContactUnauthorized(t *testing.T) {
	mux := setup()
	r := httptest.NewRequest("GET", "/api/v1/contacts", nil).WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestContactTenantIsolation(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	c := &Contact{OrganizationID: "org-1", DisplayName: "Jane"}
	repo.Create(c)
	if w := do(mux, "GET", "/api/v1/contacts/"+c.ID, "", "org-2"); w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant: expected 404, got %d", w.Code)
	}
}

func TestCIContactLinkUnlink(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	c := &Contact{OrganizationID: "org-1", DisplayName: "Jane"}
	repo.Create(c)

	// invalid relationship type
	if w := do(mux, "POST", "/api/v1/cis/ci-1/contacts", `{"contact_id":"`+c.ID+`","relationship_type":"bogus"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("invalid rel: expected 400, got %d", w.Code)
	}
	// missing contact_id
	if w := do(mux, "POST", "/api/v1/cis/ci-1/contacts", `{}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing contact_id: expected 400, got %d", w.Code)
	}
	// unknown contact
	if w := do(mux, "POST", "/api/v1/cis/ci-1/contacts", `{"contact_id":"nope"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("unknown contact: expected 400, got %d", w.Code)
	}

	w := do(mux, "POST", "/api/v1/cis/ci-1/contacts", `{"contact_id":"`+c.ID+`"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("link: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var link CIContact
	json.Unmarshal(w.Body.Bytes(), &link)
	if link.RelationshipType != "responsible" {
		t.Errorf("expected default responsible, got %q", link.RelationshipType)
	}

	// list for CI
	w = do(mux, "GET", "/api/v1/cis/ci-1/contacts", "", "org-1")
	var lr struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &lr)
	if lr.Total != 1 {
		t.Errorf("list for ci: expected 1, got %d", lr.Total)
	}

	// unlink
	if w := do(mux, "DELETE", "/api/v1/ci-contacts/"+link.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("unlink: expected 204, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/ci-contacts/"+link.ID, "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("unlink again: expected 404, got %d", w.Code)
	}
}
