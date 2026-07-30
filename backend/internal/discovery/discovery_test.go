package discovery

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

func TestHandler_RegisterAndListCollectors(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, nil)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"name":"collector-site-a","version":"1.0.0"}`
	req := httptest.NewRequest("POST", "/api/v1/collectors", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var created Collector
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Name != "collector-site-a" {
		t.Errorf("expected collector-site-a, got %s", created.Name)
	}
	if created.Status != "online" {
		t.Errorf("expected online, got %s", created.Status)
	}

	// List
	req = httptest.NewRequest("GET", "/api/v1/collectors", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []Collector `json:"data"`
		Total int         `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestHandler_Heartbeat(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, nil)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	c := &Collector{OrganizationID: "org-1", Name: "test-collector", Config: map[string]any{}}
	repo.RegisterCollector(c)

	req := httptest.NewRequest("POST", "/api/v1/collectors/"+c.ID+"/heartbeat", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestHandler_BulkIngest(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, nil)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"collector_id":"col-1","items":[{"fingerprint":{"serial":"ABC123"},"raw_data":{"hostname":"srv1"},"ci_type_name":"server","name":"srv1"}]}`
	req := httptest.NewRequest("POST", "/api/v1/ingest/bulk", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp BulkIngestResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Received != 1 {
		t.Errorf("expected received=1, got %d", resp.Received)
	}
}

func TestHandler_BulkIngestEmpty(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, nil)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	body := `{"collector_id":"col-1","items":[]}`
	req := httptest.NewRequest("POST", "/api/v1/ingest/bulk", bytes.NewBufferString(body))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
