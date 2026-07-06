package export

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestExportJSON(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(&ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Attributes:     map[string]any{},
	})

	h := NewHandler(repo)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=json", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []ci.Item `json:"data"`
		Total int       `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestExportCSV(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(&ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Manufacturer:   "Dell",
		Attributes:     map[string]any{},
	})

	h := NewHandler(repo)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=csv", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if ct := w.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("expected text/csv, got %s", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "srv-01") {
		t.Error("expected CSV to contain srv-01")
	}
	if !strings.Contains(body, "Dell") {
		t.Error("expected CSV to contain Dell")
	}
}
