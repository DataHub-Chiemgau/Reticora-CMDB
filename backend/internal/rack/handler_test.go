package rack

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

func do(mux chi.Router, method, path, body, org string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if org != "" {
		r = r.WithContext(tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: org}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func newMux() (*MemoryRepository, chi.Router) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return repo, mux
}

func TestRackCRUD(t *testing.T) {
	_, mux := newMux()

	w := do(mux, "POST", "/api/v1/racks", `{"room_id":"room-1","name":"R1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rk Rack
	json.Unmarshal(w.Body.Bytes(), &rk)
	if rk.HeightU != 42 || rk.WidthMM != 600 || rk.DepthMM != 1000 {
		t.Errorf("unexpected defaults: %+v", rk)
	}

	// missing fields
	if w := do(mux, "POST", "/api/v1/racks", `{"name":"x"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing room_id: expected 400, got %d", w.Code)
	}
	// invalid height
	if w := do(mux, "POST", "/api/v1/racks", `{"room_id":"r","name":"x","height_u":99}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad height: expected 400, got %d", w.Code)
	}

	if w := do(mux, "GET", "/api/v1/racks/"+rk.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/racks/"+rk.ID, `{"name":"R1b"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/racks/"+rk.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestRackTenantIsolation(t *testing.T) {
	repo, mux := newMux()
	rk := &Rack{OrganizationID: "org-1", RoomID: "room-1", Name: "R1", HeightU: 42, WidthMM: 600, DepthMM: 1000}
	repo.CreateRack(rk)
	if w := do(mux, "GET", "/api/v1/racks/"+rk.ID, "", "org-2"); w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant: expected 404, got %d", w.Code)
	}
}

func TestMountFitAndOverlap(t *testing.T) {
	repo, mux := newMux()
	rk := &Rack{OrganizationID: "org-1", RoomID: "room-1", Name: "R1", HeightU: 10, WidthMM: 600, DepthMM: 1000}
	repo.CreateRack(rk)
	base := "/api/v1/racks/" + rk.ID + "/mounts"

	// exceeds rack height
	if w := do(mux, "POST", base, `{"ci_id":"ci-1","position_u":9,"height_u":5}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("out-of-range: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	// valid mount 1-2U
	w := do(mux, "POST", base, `{"ci_id":"ci-1","position_u":1,"height_u":2}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("mount1: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var m1 RackMount
	json.Unmarshal(w.Body.Bytes(), &m1)
	if m1.Face != "front" {
		t.Errorf("expected default face front, got %q", m1.Face)
	}
	// overlapping mount 2-3U front
	if w := do(mux, "POST", base, `{"ci_id":"ci-2","position_u":2,"height_u":2}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("overlap: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	// non-overlapping on rear face is fine
	if w := do(mux, "POST", base, `{"ci_id":"ci-3","position_u":1,"height_u":2,"face":"rear"}`, "org-1"); w.Code != http.StatusCreated {
		t.Errorf("rear face: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	// duplicate ci
	if w := do(mux, "POST", base, `{"ci_id":"ci-1","position_u":5,"height_u":1}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("dup ci: expected 400, got %d", w.Code)
	}

	// list mounts
	w = do(mux, "GET", base, "", "org-1")
	var lr struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &lr)
	if lr.Total != 2 {
		t.Errorf("list mounts: expected 2, got %d", lr.Total)
	}

	// list mounts of missing rack -> 404
	if w := do(mux, "GET", "/api/v1/racks/nope/mounts", "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("missing rack mounts: expected 404, got %d", w.Code)
	}

	// update mount to overlap -> 400
	if w := do(mux, "PATCH", "/api/v1/rack-mounts/"+m1.ID, `{"position_u":1,"height_u":11}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("patch overlap: expected 400, got %d", w.Code)
	}
	// valid update
	if w := do(mux, "PATCH", "/api/v1/rack-mounts/"+m1.ID, `{"position_u":8}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	// delete
	if w := do(mux, "DELETE", "/api/v1/rack-mounts/"+m1.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete mount: expected 204, got %d", w.Code)
	}
}

func TestCableCRUD(t *testing.T) {
	_, mux := newMux()
	w := do(mux, "POST", "/api/v1/cables", `{"label":"C1","cable_type":"fiber_sm"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c Cable
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.Status != "connected" {
		t.Errorf("expected default status connected, got %q", c.Status)
	}
	// invalid cable_type
	if w := do(mux, "POST", "/api/v1/cables", `{"cable_type":"bogus"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad type: expected 400, got %d", w.Code)
	}
	if w := do(mux, "GET", "/api/v1/cables/"+c.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/cables/"+c.ID, `{"status":"planned"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/cables/"+c.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestRackUnauthorized(t *testing.T) {
	_, mux := newMux()
	r := httptest.NewRequest("GET", "/api/v1/racks", nil).WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
