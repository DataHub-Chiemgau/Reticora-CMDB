package ipam

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

func TestSubnetCRUDAndCIDRValidation(t *testing.T) {
	_, mux := newMux()

	// invalid cidr
	if w := do(mux, "POST", "/api/v1/subnets", `{"cidr":"not-a-cidr"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad cidr: expected 400, got %d", w.Code)
	}
	// missing cidr
	if w := do(mux, "POST", "/api/v1/subnets", `{"name":"x"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing cidr: expected 400, got %d", w.Code)
	}
	// invalid gateway
	if w := do(mux, "POST", "/api/v1/subnets", `{"cidr":"10.0.0.0/24","gateway":"999.1.1.1"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad gateway: expected 400, got %d", w.Code)
	}

	w := do(mux, "POST", "/api/v1/subnets", `{"cidr":"10.0.0.0/24","name":"lan","gateway":"10.0.0.1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var s Subnet
	json.Unmarshal(w.Body.Bytes(), &s)
	if s.CIDR != "10.0.0.0/24" {
		t.Errorf("expected normalized cidr, got %q", s.CIDR)
	}

	if w := do(mux, "GET", "/api/v1/subnets/"+s.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/subnets/"+s.ID, `{"name":"lan2"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/subnets/"+s.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestIPAddressSubnetMembership(t *testing.T) {
	repo, mux := newMux()
	s := &Subnet{OrganizationID: "org-1", CIDR: "10.0.0.0/24"}
	repo.CreateSubnet(context.Background(), s)

	// invalid ip
	if w := do(mux, "POST", "/api/v1/ip-addresses", `{"address":"nope"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad ip: expected 400, got %d", w.Code)
	}
	// ip not in subnet
	if w := do(mux, "POST", "/api/v1/ip-addresses", `{"address":"192.168.1.5","subnet_id":"`+s.ID+`"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("ip not in subnet: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	// ip in subnet OK
	w := do(mux, "POST", "/api/v1/ip-addresses", `{"address":"10.0.0.5","subnet_id":"`+s.ID+`"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("valid ip: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var a IPAddress
	json.Unmarshal(w.Body.Bytes(), &a)
	if a.Status != "active" {
		t.Errorf("expected default status active, got %q", a.Status)
	}

	// list subnet addresses
	w = do(mux, "GET", "/api/v1/subnets/"+s.ID+"/addresses", "", "org-1")
	var lr struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &lr)
	if lr.Total != 1 {
		t.Errorf("subnet addresses: expected 1, got %d", lr.Total)
	}

	// addresses for missing subnet -> 404
	if w := do(mux, "GET", "/api/v1/subnets/nope/addresses", "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("missing subnet addresses: expected 404, got %d", w.Code)
	}

	// invalid status
	if w := do(mux, "POST", "/api/v1/ip-addresses", `{"address":"10.0.0.6","status":"bogus"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad status: expected 400, got %d", w.Code)
	}

	if w := do(mux, "GET", "/api/v1/ip-addresses/"+a.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/ip-addresses/"+a.ID, `{"status":"reserved"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/ip-addresses/"+a.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestInterfaceCRUD(t *testing.T) {
	_, mux := newMux()

	w := do(mux, "POST", "/api/v1/cis/ci-1/interfaces", `{"name":"eth0"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var ni NetworkInterface
	json.Unmarshal(w.Body.Bytes(), &ni)
	if ni.InterfaceType != "ethernet" || ni.AdminStatus != "up" || ni.OperStatus != "unknown" {
		t.Errorf("unexpected defaults: %+v", ni)
	}

	// missing name
	if w := do(mux, "POST", "/api/v1/cis/ci-1/interfaces", `{}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing name: expected 400, got %d", w.Code)
	}
	// invalid type
	if w := do(mux, "POST", "/api/v1/cis/ci-1/interfaces", `{"name":"x","interface_type":"bogus"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad type: expected 400, got %d", w.Code)
	}

	// list for ci
	w = do(mux, "GET", "/api/v1/cis/ci-1/interfaces", "", "org-1")
	var lr struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &lr)
	if lr.Total != 1 {
		t.Errorf("list for ci: expected 1, got %d", lr.Total)
	}

	if w := do(mux, "GET", "/api/v1/network-interfaces/"+ni.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/network-interfaces/"+ni.ID, `{"oper_status":"up"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}
	// tenant isolation
	if w := do(mux, "GET", "/api/v1/network-interfaces/"+ni.ID, "", "org-2"); w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant: expected 404, got %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/v1/network-interfaces/"+ni.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
}

func TestIPAMUnauthorized(t *testing.T) {
	_, mux := newMux()
	r := httptest.NewRequest("GET", "/api/v1/subnets", nil).WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
