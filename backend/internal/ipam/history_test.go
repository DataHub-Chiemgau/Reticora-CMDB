package ipam_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestIPAMHistoryWithoutPostgreSQL: without PostgreSQL the history routes
// answer 501 (IPM-08, WP-908 AK7).
func TestIPAMHistoryWithoutPostgreSQL(t *testing.T) {
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	for _, path := range []string{"/api/v1/subnets/s-1/history", "/api/v1/ips/ip-1/history", "/api/v1/ip-addresses/ip-1/history"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("GET %s = %d, want 501", path, w.Code)
		}
	}
}
