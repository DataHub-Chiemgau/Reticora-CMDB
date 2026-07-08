package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

	body := `{"name":"My Hook","url":"https://example.com/hook","secret":"s3cr3t","events":["ci.created","ci.updated"]}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Subscription
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Name != "My Hook" {
		t.Errorf("expected My Hook, got %s", created.Name)
	}
	if !created.IsActive {
		t.Error("expected is_active to be true")
	}

	// List
	req = httptest.NewRequest("GET", "/api/v1/webhooks", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Subscription `json:"data"`
		Total int            `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestHandler_InvalidEvent(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"name":"Bad","url":"https://example.com","secret":"x","events":["bad.event"]}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks", bytes.NewBufferString(body))
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

	sub := &Subscription{
		OrganizationID: "org-1",
		Name:           "to-delete",
		URL:            "https://example.com",
		Secret:         "secret",
		Events:         []string{"ci.created"},
		IsActive:       true,
	}
	repo.Create(sub)

	req := httptest.NewRequest("DELETE", "/api/v1/webhooks/"+sub.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}
