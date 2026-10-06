package entitlement

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

func TestHandlerGrantListAndCheck(t *testing.T) {
	h := NewHandler(NewService(NewMemoryRepository(), Options{Enforce: true}))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	// The tenant API is read-only (ENT-04): writing is refused.
	req := tenantCtx(httptest.NewRequest(http.MethodPost, "/api/v1/entitlements", bytes.NewBufferString(`{"feature_key":"ticketing","plan":"pro"}`)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("tenant write: %d, want 405", w.Code)
	}
	if _, err := h.service.Grant(req.Context(), Entitlement{OrganizationID: "org-1", FeatureKey: "ticketing", Plan: PlanPro, Enabled: true}); err != nil {
		t.Fatal(err)
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
	var found bool
	for _, item := range listResp.Data {
		if item.FeatureKey == "ticketing" && item.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the granted ticketing entitlement in the list: %+v", listResp)
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

func TestMiddlewareBlocksUnentitledModule(t *testing.T) {
	svc := NewService(NewMemoryRepository(), Options{Enforce: true})
	handler := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/tickets", nil))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unentitled module, got %d", w.Code)
	}

	req = tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected core routes to stay reachable, got %d", w.Code)
	}

	if _, err := svc.Grant(req.Context(), Entitlement{
		OrganizationID: "org-1",
		FeatureKey:     FeatureTicketing,
		Plan:           PlanStandard,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	req = tenantCtx(httptest.NewRequest(http.MethodGet, "/api/v1/tickets", nil))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 after granting the entitlement, got %d", w.Code)
	}
}

func TestMiddlewareRequiresTenant(t *testing.T) {
	svc := NewService(NewMemoryRepository(), Options{Enforce: true})
	handler := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/tickets", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without tenant context, got %d", w.Code)
	}
}
