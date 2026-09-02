package relationship

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestHandler_CreateAndList(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"connected_to"}`
	req := httptest.NewRequest("POST", "/api/v1/relationships", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Relationship
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.RelType != "connected_to" {
		t.Errorf("expected connected_to, got %s", created.RelType)
	}

	// List relationships for ci-1
	req = httptest.NewRequest("GET", "/api/v1/cis/ci-1/relationships", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Relationship `json:"data"`
		Total int            `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestHandler_InvalidRelType(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"invalid_type"}`
	req := httptest.NewRequest("POST", "/api/v1/relationships", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	rel := &Relationship{
		OrganizationID: "org-1",
		SourceCIID:     "ci-1",
		TargetCIID:     "ci-2",
		RelType:        "depends_on",
		Attributes:     map[string]any{},
	}
	repo.Create(context.Background(), rel)

	req := httptest.NewRequest("DELETE", "/api/v1/relationships/"+rel.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

// TestListAllRelationships covers GET /api/v1/relationships, which lists every
// relationship in the organization rather than those of a single CI.
func TestListAllRelationships(t *testing.T) {
	repo := NewMemoryRepository()
	for _, rel := range []*Relationship{
		{OrganizationID: "org-1", SourceCIID: "ci-1", TargetCIID: "ci-2", RelType: "depends_on"},
		{OrganizationID: "org-1", SourceCIID: "ci-3", TargetCIID: "ci-4", RelType: "hosts"},
		{OrganizationID: "org-2", SourceCIID: "ci-5", TargetCIID: "ci-6", RelType: "hosts"},
	} {
		if err := repo.Create(context.Background(), rel); err != nil {
			t.Fatalf("create relationship: %v", err)
		}
	}

	router := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(router)

	req := tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/relationships", nil))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ListResponse[Relationship]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("expected the 2 relationships of org-1, got %d", resp.Total)
	}
}

// createRel posts a relationship and returns it.
func createRel(t *testing.T, mux http.Handler, body string) Relationship {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/relationships", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rel Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &rel); err != nil {
		t.Fatal(err)
	}
	return rel
}

func patchRel(t *testing.T, mux http.Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PATCH", "/api/v1/relationships/"+id, bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestHandler_UpdateVerifiesAndEdits covers spec §13, which requires manual
// relationships to be editable and verifiable, not just creatable and
// deletable.
func TestHandler_UpdateVerifiesAndEdits(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	rel := createRel(t, mux, `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"depends_on","source":"discovery"}`)
	if rel.VerificationState != "" && rel.VerificationState != "unverified" {
		t.Fatalf("discovered relationship should start unverified, got %q", rel.VerificationState)
	}

	conf := 0.9
	body, _ := json.Marshal(UpdateRequest{
		VerificationState: strPtr("verified"),
		Notes:             strPtr("checked on site"),
		Confidence:        &conf,
		SourceSystem:      strPtr("netbox"),
		Attributes:        map[string]any{"port": "eth0"},
	})
	w := patchRel(t, mux, rel.ID, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updated Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.VerificationState != "verified" {
		t.Fatalf("expected verified, got %q", updated.VerificationState)
	}
	if updated.Notes != "checked on site" {
		t.Fatalf("expected notes persisted, got %q", updated.Notes)
	}
	if updated.Confidence == nil || *updated.Confidence != 0.9 {
		t.Fatalf("expected confidence 0.9, got %v", updated.Confidence)
	}
	if updated.SourceSystem != "netbox" {
		t.Fatalf("expected source_system persisted, got %q", updated.SourceSystem)
	}
	if updated.Attributes["port"] != "eth0" {
		t.Fatalf("expected attributes merged, got %#v", updated.Attributes)
	}
	// The edge itself must not have moved.
	if updated.SourceCIID != "ci-1" || updated.TargetCIID != "ci-2" || updated.RelType != "depends_on" {
		t.Fatalf("edge endpoints/type must be immutable, got %+v", updated)
	}
}

func TestHandler_UpdateRejectsInvalidInput(t *testing.T) {
	repo := NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	rel := createRel(t, mux, `{"source_ci_id":"ci-1","target_ci_id":"ci-2","rel_type":"depends_on"}`)

	if w := patchRel(t, mux, rel.ID, `{"verification_state":"bogus"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid verification_state, got %d", w.Code)
	}
	if w := patchRel(t, mux, rel.ID, `{"confidence":1.5}`); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range confidence, got %d", w.Code)
	}
}

// TestHandler_UpdateIsTenantScoped ensures a relationship belonging to another
// organization cannot be edited through a guessed identifier.
func TestHandler_UpdateIsTenantScoped(t *testing.T) {
	repo := NewMemoryRepository()
	other := &Relationship{OrganizationID: "org-2", SourceCIID: "x", TargetCIID: "y", RelType: "depends_on"}
	if err := repo.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	if w := patchRel(t, mux, other.ID, `{"verification_state":"verified"}`); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant update, got %d", w.Code)
	}
	if other.VerificationState == "verified" {
		t.Fatal("cross-tenant update must not mutate the row")
	}
}

func strPtr(s string) *string { return &s }
