package entitlement

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1"})
	return r.WithContext(ctx)
}

func TestHandlerGrantListAndCheck(t *testing.T) {
	h := NewHandler(NewService())
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/entitlements", bytes.NewBufferString(`{"feature_key":"ticketing","plan":"pro"}`))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/entitlements", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var listResp struct {
		Data  []Entitlement `json:"data"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Total != 1 || listResp.Data[0].FeatureKey != "ticketing" {
		t.Fatalf("unexpected list response: %+v", listResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/entitlements/check/ticketing", nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var checkResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &checkResp); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := checkResp["enabled"].(bool); !enabled {
		t.Fatalf("expected enabled=true, got %v", checkResp["enabled"])
	}
}
