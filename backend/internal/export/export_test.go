package export

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	scope := database.OrgWideScope("org-1", "user-1")
	return r.WithContext(database.ContextWithTenantScope(ctx, &scope))
}

func TestExportJSON(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(context.Background(), &ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Attributes:     map[string]any{},
	})

	h := NewHandler(repo)
	mux := chi.NewRouter()
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
	repo.Create(context.Background(), &ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Manufacturer:   "Dell",
		Attributes:     map[string]any{},
	})

	h := NewHandler(repo)
	mux := chi.NewRouter()
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

func TestExportDATEV(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(context.Background(), &ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Manufacturer:   "Dell",
		Model:          "R740",
		SerialNumber:   "SN-1",
		Attributes: map[string]any{
			"purchase_date": "2024-01-15",
			"location":      "HQ",
			"cost_center":   "IT-100",
		},
	})

	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=datev", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Inventarnummer;Bezeichnung;Hersteller") {
		t.Fatalf("expected DATEV header, got %s", body)
	}
	if !strings.Contains(body, "HQ") || !strings.Contains(body, "IT-100") {
		t.Fatalf("expected DATEV body to contain mapped fields, got %s", body)
	}
}

func TestExportStreamsBeyondOneBatch(t *testing.T) {
	repo := ci.NewMemoryRepository()
	const count = 12
	for i := 0; i < count; i++ {
		err := repo.Create(context.Background(), &ci.Item{
			OrganizationID: "org-1",
			CITypeID:       "type-server",
			Name:           fmt.Sprintf("srv-%02d", i),
			Status:         "active",
			Attributes:     map[string]any{},
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	h := NewHandler(repo)
	h.batchSize = 5 // force several streaming batches
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	for _, format := range []string{"json", "csv", "datev"} {
		t.Run(format, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/export/cis?format="+format, nil)
			req = tenantCtx(req)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			body := w.Body.String()
			for i := 0; i < count; i++ {
				if !strings.Contains(body, fmt.Sprintf("srv-%02d", i)) {
					t.Fatalf("streamed %s export is missing srv-%02d", format, i)
				}
			}

			if format == "json" {
				var resp struct {
					Data  []ci.Item `json:"data"`
					Total int       `json:"total"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("streamed JSON is not valid: %v (%s)", err, body)
				}
				if resp.Total != count || len(resp.Data) != count {
					t.Fatalf("expected %d rows, got total=%d len=%d", count, resp.Total, len(resp.Data))
				}
			}
		})
	}
}

func TestExportRejectsUnknownFormat(t *testing.T) {
	h := NewHandler(ci.NewMemoryRepository())
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=xml", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}
