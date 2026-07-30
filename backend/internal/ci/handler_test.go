package ci

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

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestHandler_CreateAndGet(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Create a CI
	body := `{"name":"sw-core-01","ci_type_id":"type-switch","manufacturer":"Cisco","model":"C9300"}`
	req := httptest.NewRequest("POST", "/api/v1/cis", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Item
	json.Unmarshal(w.Body.Bytes(), &created)

	if created.Name != "sw-core-01" {
		t.Errorf("expected name sw-core-01, got %s", created.Name)
	}
	if created.ID == "" {
		t.Error("expected non-empty ID")
	}

	// Get the created CI
	req = httptest.NewRequest("GET", "/api/v1/cis/"+created.ID, nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var fetched Item
	json.Unmarshal(w.Body.Bytes(), &fetched)
	if fetched.ID != created.ID {
		t.Errorf("expected ID %s, got %s", created.ID, fetched.ID)
	}
}

func TestHandler_List(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Create two CIs
	for _, name := range []string{"server-01", "server-02"} {
		item := &Item{
			OrganizationID: "org-1",
			CITypeID:       "type-server",
			Name:           name,
			Status:         "active",
			Attributes:     map[string]any{},
		}
		repo.Create(context.Background(), item)
	}

	req := httptest.NewRequest("GET", "/api/v1/cis?limit=10", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Item `json:"data"`
		Total int    `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("expected 2 items, got %d", resp.Total)
	}
}

func TestHandler_Update(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	item := &Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "old-name",
		Status:         "active",
		Attributes:     map[string]any{},
	}
	repo.Create(context.Background(), item)

	body := `{"name":"new-name","status":"maintenance"}`
	req := httptest.NewRequest("PATCH", "/api/v1/cis/"+item.ID, bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var updated Item
	json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.Name != "new-name" {
		t.Errorf("expected new-name, got %s", updated.Name)
	}
	if updated.Status != "maintenance" {
		t.Errorf("expected maintenance, got %s", updated.Status)
	}
}

func TestHandler_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	item := &Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "to-delete",
		Status:         "active",
		Attributes:     map[string]any{},
	}
	repo.Create(context.Background(), item)

	req := httptest.NewRequest("DELETE", "/api/v1/cis/"+item.ID, nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	// Verify deleted
	req = httptest.NewRequest("GET", "/api/v1/cis/"+item.ID, nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandler_Unauthorized(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// Request without tenant context
	req := httptest.NewRequest("GET", "/api/v1/cis", nil)
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandler_ListFilter(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(NewService(repo))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	repo.Create(context.Background(), &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv-active", Status: "active", Attributes: map[string]any{}})
	repo.Create(context.Background(), &Item{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv-maint", Status: "maintenance", Attributes: map[string]any{}})

	req := httptest.NewRequest("GET", "/api/v1/cis?status=active", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp struct {
		Data  []Item `json:"data"`
		Total int    `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1 item with status=active, got %d", resp.Total)
	}
}
