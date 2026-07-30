package tenantapi

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

func withTenant(r *http.Request, org string) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: org})
	return r.WithContext(ctx)
}

func setup() (*Handler, *MemoryRepository, chi.Router) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return h, repo, mux
}

func do(mux chi.Router, method, path, body, org string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if org != "" {
		r = withTenant(r, org)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestClientCRUD(t *testing.T) {
	_, _, mux := setup()

	w := do(mux, "POST", "/api/v1/clients", `{"name":"Acme","slug":"acme"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c Client
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.ID == "" || c.Name != "Acme" {
		t.Fatalf("unexpected client: %+v", c)
	}

	// duplicate slug -> 400
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"Acme2","slug":"acme"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("dup slug: expected 400, got %d", w.Code)
	}

	// missing fields -> 400
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"x"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing slug: expected 400, got %d", w.Code)
	}

	// get
	if w := do(mux, "GET", "/api/v1/clients/"+c.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}

	// patch
	if w := do(mux, "PATCH", "/api/v1/clients/"+c.ID, `{"name":"Renamed"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}

	// delete
	if w := do(mux, "DELETE", "/api/v1/clients/"+c.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
	if w := do(mux, "GET", "/api/v1/clients/"+c.ID, "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("get deleted: expected 404, got %d", w.Code)
	}
}

func TestClientUnauthorized(t *testing.T) {
	_, _, mux := setup()
	r := httptest.NewRequest("GET", "/api/v1/clients", nil).WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestTenantIsolation(t *testing.T) {
	_, repo, mux := setup()
	c := &Client{OrganizationID: "org-1", Name: "A", Slug: "a"}
	repo.CreateClient(c)

	// org-2 must not see org-1's client
	if w := do(mux, "GET", "/api/v1/clients/"+c.ID, "", "org-2"); w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: expected 404, got %d", w.Code)
	}
	w := do(mux, "GET", "/api/v1/clients", "", "org-2")
	var resp struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("cross-tenant list: expected 0, got %d", resp.Total)
	}
}

func TestSiteBuildingRoomHierarchy(t *testing.T) {
	_, repo, mux := setup()
	c := &Client{OrganizationID: "org-1", Name: "A", Slug: "a"}
	repo.CreateClient(c)

	// site requires valid client
	if w := do(mux, "POST", "/api/v1/sites", `{"client_id":"nope","name":"S"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("site bad client: expected 400, got %d", w.Code)
	}
	w := do(mux, "POST", "/api/v1/sites", `{"client_id":"`+c.ID+`","name":"HQ"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("site create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var s Site
	json.Unmarshal(w.Body.Bytes(), &s)

	// filter by client_id
	w = do(mux, "GET", "/api/v1/sites?client_id="+c.ID, "", "org-1")
	var sl struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &sl)
	if sl.Total != 1 {
		t.Errorf("site filter: expected 1, got %d", sl.Total)
	}

	// building
	w = do(mux, "POST", "/api/v1/buildings", `{"site_id":"`+s.ID+`","name":"B1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("building create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var b Building
	json.Unmarshal(w.Body.Bytes(), &b)

	// room
	w = do(mux, "POST", "/api/v1/rooms", `{"building_id":"`+b.ID+`","name":"R1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("room create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rm Room
	json.Unmarshal(w.Body.Bytes(), &rm)
	if rm.RoomType != "general" {
		t.Errorf("expected default room_type general, got %q", rm.RoomType)
	}

	// filter rooms by building
	w = do(mux, "GET", "/api/v1/rooms?building_id="+b.ID, "", "org-1")
	var rl struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &rl)
	if rl.Total != 1 {
		t.Errorf("room filter: expected 1, got %d", rl.Total)
	}
}
