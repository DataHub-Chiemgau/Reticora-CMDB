package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
	if a.Type != IPTypeStatic {
		t.Errorf("expected default type static, got %q", a.Type)
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

	// invalid type
	if w := do(mux, "POST", "/api/v1/ip-addresses", `{"address":"10.0.0.6","type":"active"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("bad type: expected 400, got %d", w.Code)
	}

	if w := do(mux, "GET", "/api/v1/ip-addresses/"+a.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/ip-addresses/"+a.ID, `{"type":"reserved"}`, "org-1"); w.Code != http.StatusOK {
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
	if ni.IfType != "ethernet" || ni.AdminStatus != "up" || ni.OperStatus != "unknown" {
		t.Errorf("unexpected defaults: %+v", ni)
	}

	// missing name
	if missing := do(mux, "POST", "/api/v1/cis/ci-1/interfaces", `{}`, "org-1"); missing.Code != http.StatusBadRequest {
		t.Errorf("missing name: expected 400, got %d", missing.Code)
	}
	// invalid type
	if bad := do(mux, "POST", "/api/v1/cis/ci-1/interfaces", `{"name":"x","if_type":"bogus"}`, "org-1"); bad.Code != http.StatusBadRequest {
		t.Errorf("bad type: expected 400, got %d", bad.Code)
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

// decodeMap decodes a JSON object response.
func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

// TestVRFResourceCRUD covers the vrfs resource of NET-05/API-05 (WP-125):
// list with the default VRF global first, create, read, rename, delete;
// a taken name is 409, the default VRF and a VRF with subnets cannot be
// deleted (422), another organization sees nothing.
func TestVRFResourceCRUD(t *testing.T) {
	_, mux := newMux()

	w := do(mux, http.MethodGet, "/api/v1/vrfs", "", "org-1")
	list := decodeMap(t, w)
	data, _ := list["data"].([]any)
	if w.Code != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["name"] != "global" ||
		data[0].(map[string]any)["is_default"] != true {
		t.Fatalf("initial list: %d %s, want the default VRF global", w.Code, w.Body.String())
	}
	global := data[0].(map[string]any)["id"].(string)

	w = do(mux, http.MethodPost, "/api/v1/vrfs", `{"name":"customer-a","description":"Kunde A"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	created := decodeMap(t, w)
	id := created["id"].(string)
	if created["is_default"] != false || created["description"] != "Kunde A" {
		t.Errorf("created = %v", created)
	}
	for name, tc := range map[string]struct {
		method, path, body string
		status             int
	}{
		"empty name":        {http.MethodPost, "/api/v1/vrfs", `{"name":"  "}`, http.StatusBadRequest},
		"unknown field":     {http.MethodPost, "/api/v1/vrfs", `{"name":"x","is_default":true}`, http.StatusBadRequest},
		"taken name":        {http.MethodPost, "/api/v1/vrfs", `{"name":"customer-a"}`, http.StatusConflict},
		"rename onto taken": {http.MethodPatch, "/api/v1/vrfs/" + id, `{"name":"global"}`, http.StatusConflict},
		"delete default":    {http.MethodDelete, "/api/v1/vrfs/" + global, "", http.StatusUnprocessableEntity},
		"other org reads":   {http.MethodGet, "/api/v1/vrfs/" + id, "", http.StatusNotFound},
	} {
		org := "org-1"
		if name == "other org reads" {
			org = "org-2"
		}
		if got := do(mux, tc.method, tc.path, tc.body, org); got.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body.String(), tc.status)
		}
	}

	if w = do(mux, http.MethodPatch, "/api/v1/vrfs/"+id, `{"name":"customer-b"}`, "org-1"); w.Code != http.StatusOK ||
		decodeMap(t, w)["name"] != "customer-b" {
		t.Errorf("rename: %d %s", w.Code, w.Body.String())
	}
	if w = do(mux, http.MethodGet, "/api/v1/vrfs/"+id, "", "org-1"); w.Code != http.StatusOK || decodeMap(t, w)["name"] != "customer-b" {
		t.Errorf("read after rename: %d %s", w.Code, w.Body.String())
	}

	// A VRF with subnets stays until they are gone.
	w = do(mux, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.125.0.0/24","vrf_id":"`+id+`"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("subnet in the VRF: %d %s", w.Code, w.Body.String())
	}
	subnet := decodeMap(t, w)["id"].(string)
	if w = do(mux, http.MethodDelete, "/api/v1/vrfs/"+id, "", "org-1"); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("delete VRF with subnets: %d, want 422", w.Code)
	}
	do(mux, http.MethodDelete, "/api/v1/subnets/"+subnet, "", "org-1")
	if w = do(mux, http.MethodDelete, "/api/v1/vrfs/"+id, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	if w = do(mux, http.MethodGet, "/api/v1/vrfs/"+id, "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("read after delete: %d, want 404", w.Code)
	}
}

// TestVLANResourceCRUD covers the vlans resource of NET-05/NET-08/API-05
// (WP-125): numbers 1–4094, unique per site (or per organization without
// site), list filtered by site, rename, renumber and delete.
func TestVLANResourceCRUD(t *testing.T) {
	_, mux := newMux()

	w := do(mux, http.MethodPost, "/api/v1/vlans", `{"site_id":"site-a","vlan_id":10,"name":"users"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	id := decodeMap(t, w)["id"].(string)
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"same number at another site": {`{"site_id":"site-b","vlan_id":10,"name":"users"}`, http.StatusCreated},
		"same number without site":    {`{"vlan_id":10,"name":"users"}`, http.StatusCreated},
		"duplicate at the site":       {`{"site_id":"site-a","vlan_id":10,"name":"again"}`, http.StatusConflict},
		"number 0":                    {`{"vlan_id":0,"name":"x"}`, http.StatusBadRequest},
		"number 4095":                 {`{"vlan_id":4095,"name":"x"}`, http.StatusBadRequest},
		"missing name":                {`{"vlan_id":20}`, http.StatusBadRequest},
	} {
		if got := do(mux, http.MethodPost, "/api/v1/vlans", tc.body, "org-1"); got.Code != tc.status {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body.String(), tc.status)
		}
	}

	w = do(mux, http.MethodGet, "/api/v1/vlans?site_id=site-a", "", "org-1")
	if data, _ := decodeMap(t, w)["data"].([]any); w.Code != http.StatusOK || len(data) != 1 {
		t.Errorf("list of site-a: %d %s", w.Code, w.Body.String())
	}
	if w = do(mux, http.MethodGet, "/api/v1/vlans", "", "org-2"); !bytes.Contains(w.Body.Bytes(), []byte(`"total":0`)) {
		t.Errorf("other organization lists: %s", w.Body.String())
	}

	if w = do(mux, http.MethodPatch, "/api/v1/vlans/"+id, `{"vlan_id":11,"name":"staff"}`, "org-1"); w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	got := decodeMap(t, do(mux, http.MethodGet, "/api/v1/vlans/"+id, "", "org-1"))
	if got["vlan_id"] != 11.0 || got["name"] != "staff" || got["site_id"] != "site-a" {
		t.Errorf("after patch: %v", got)
	}
	if w = do(mux, http.MethodPatch, "/api/v1/vlans/"+id, `{"vlan_id":5000}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("renumber to 5000: %d, want 400", w.Code)
	}
	if w = do(mux, http.MethodDelete, "/api/v1/vlans/"+id, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if w = do(mux, http.MethodGet, "/api/v1/vlans/"+id, "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("read after delete: %d, want 404", w.Code)
	}
}

// TestSubnetReservations covers the IP reservations of a subnet (NET-09,
// WP-299) without a database: validation, unknown subnet, double
// reservation, an address on an interface, list and release.
func TestSubnetReservations(t *testing.T) {
	repo, mux := newMux()
	ctx := context.Background()
	s := &Subnet{OrganizationID: "org-1", CIDR: "10.9.0.0/24"}
	if err := repo.CreateSubnet(ctx, s); err != nil {
		t.Fatal(err)
	}
	other := &Subnet{OrganizationID: "org-1", CIDR: "10.8.0.0/24"}
	if err := repo.CreateSubnet(ctx, other); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/subnets/" + s.ID + "/reservations"

	for name, body := range map[string]string{
		"missing address": `{}`,
		"invalid address": `{"address":"10.9.0.300"}`,
		"with prefix":     `{"address":"10.9.0.5/24"}`,
		"outside subnet":  `{"address":"10.8.0.5"}`,
		"bad time":        `{"address":"10.9.0.5","reserved_until":"tomorrow"}`,
	} {
		if w := do(mux, http.MethodPost, base, body, "org-1"); w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", name, w.Code, w.Body.String())
		}
	}
	if w := do(mux, http.MethodPost, "/api/v1/subnets/unknown/reservations", `{"address":"10.9.0.5"}`, "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("unknown subnet: expected 404, got %d", w.Code)
	}
	if w := do(mux, http.MethodPost, base, `{"address":"10.9.0.5"}`, "org-2"); w.Code != http.StatusNotFound {
		t.Errorf("subnet of another organization: expected 404, got %d", w.Code)
	}

	w := do(mux, http.MethodPost, base, `{"address":"10.9.0.5","description":"printer","reserved_until":"2030-01-01T00:00:00+01:00"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var res IPAddress
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Type != IPTypeReserved || res.SubnetID != s.ID || res.InterfaceID != "" || res.VRFID != s.VRFID || res.Description != "printer" {
		t.Errorf("unexpected reservation %+v", res)
	}
	if res.ReservedUntil == nil || res.ReservedUntil.Format(time.RFC3339) != "2029-12-31T23:00:00Z" {
		t.Errorf("reserved_until = %v, want 2029-12-31T23:00:00Z", res.ReservedUntil)
	}
	if got := do(mux, http.MethodPost, base, `{"address":"10.9.0.5"}`, "org-1"); got.Code != http.StatusConflict {
		t.Errorf("double reservation: expected 409, got %d", got.Code)
	}

	// An address on an interface in the VRF of the subnet.
	nic := &NetworkInterface{OrganizationID: "org-1", CIID: "ci-1", Name: "eth0"}
	if err := repo.CreateInterface(ctx, nic); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateIPAddress(ctx, &IPAddress{OrganizationID: "org-1", SubnetID: s.ID, InterfaceID: nic.ID,
		Address: "10.9.0.6/24", Type: IPTypeDiscovered}); err != nil {
		t.Fatal(err)
	}
	w = do(mux, http.MethodPost, base, `{"address":"10.9.0.6"}`, "org-1")
	if w.Code != http.StatusConflict {
		t.Fatalf("address on an interface: expected 409, got %d", w.Code)
	}
	if detail, _ := decodeMap(t, w)["detail"].(string); !strings.Contains(detail, "10.9.0.6") || !strings.Contains(detail, nic.ID) {
		t.Errorf("detail %q names neither the address nor the interface", detail)
	}

	// A static address of the subnet is no reservation.
	static := &IPAddress{OrganizationID: "org-1", SubnetID: s.ID, Address: "10.9.0.7", Type: IPTypeStatic}
	if err := repo.CreateIPAddress(ctx, static); err != nil {
		t.Fatal(err)
	}
	if got := do(mux, http.MethodPost, base, `{"address":"10.9.0.8"}`, "org-1"); got.Code != http.StatusCreated {
		t.Fatalf("second reservation: expected 201, got %d", got.Code)
	}
	w = do(mux, http.MethodGet, base+"?limit=1", "", "org-1")
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", w.Code)
	}
	var page struct {
		Data       []IPAddress `json:"data"`
		Total      int         `json:"total"`
		NextCursor string      `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Data) != 1 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, want 1 of 2 with next_cursor", page)
	}
	if got := do(mux, http.MethodGet, base+"?limit=1&cursor="+page.NextCursor, "", "org-1"); got.Code != http.StatusOK {
		t.Errorf("second page: expected 200, got %d", got.Code)
	}
	if got := do(mux, http.MethodGet, base+"?cursor=forged", "", "org-1"); got.Code != http.StatusBadRequest {
		t.Errorf("forged cursor: expected 400, got %d", got.Code)
	}
	if got := do(mux, http.MethodGet, "/api/v1/subnets/unknown/reservations", "", "org-1"); got.Code != http.StatusNotFound {
		t.Errorf("list of unknown subnet: expected 404, got %d", got.Code)
	}

	// Release: not for a static address, not through another subnet.
	if got := do(mux, http.MethodDelete, base+"/"+static.ID, "", "org-1"); got.Code != http.StatusNotFound {
		t.Errorf("delete static: expected 404, got %d", got.Code)
	}
	if got := do(mux, http.MethodDelete, "/api/v1/subnets/"+other.ID+"/reservations/"+res.ID, "", "org-1"); got.Code != http.StatusNotFound {
		t.Errorf("delete through another subnet: expected 404, got %d", got.Code)
	}
	if got := do(mux, http.MethodDelete, base+"/"+res.ID, "", "org-1"); got.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", got.Code)
	}
	if got := do(mux, http.MethodDelete, base+"/"+res.ID, "", "org-1"); got.Code != http.StatusNotFound {
		t.Errorf("second delete: expected 404, got %d", got.Code)
	}
}

// TestSubnetGatewayInsideSubnet covers AK2 to AK4 of WP-902 (IPM-04) on the
// memory repository: a gateway outside the prefix is 422 gateway at POST and
// PATCH, also against a new prefix; an invalid gateway stays 400; setting
// the gateway marks an address of the subnet, changing it moves the mark,
// clearing it removes the mark and keeps the addresses; the gateway
// address cannot leave its subnet (422 subnet_id) or be deleted (409
// is_gateway); an unknown state is 422 state.
func TestSubnetGatewayInsideSubnet(t *testing.T) {
	repo, mux := newMux()
	page := api.PaginationParams{Limit: 50}
	w := do(mux, "POST", "/api/v1/subnets", `{"cidr":"10.9.0.0/24","gateway":"10.9.1.1"}`, "org-1")
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"gateway"`) {
		t.Errorf("POST gateway outside: %d %s, want 422 gateway", w.Code, w.Body.String())
	}
	if w = do(mux, "POST", "/api/v1/subnets", `{"cidr":"10.9.0.0/24","gateway":"10.9.0.300"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("POST invalid gateway: %d, want 400", w.Code)
	}
	w = do(mux, "POST", "/api/v1/subnets", `{"cidr":"10.9.0.0/24","gateway":"10.9.0.1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	var s Subnet
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	marked := func() []IPAddress {
		gw := true
		items, _, _ := repo.ListIPAddresses(context.Background(), "org-1", IPFilter{SubnetID: s.ID, IsGateway: &gw}, page)
		return items
	}
	if m := marked(); len(m) != 1 || m[0].Address != "10.9.0.1" || m[0].Type != IPTypeStatic || m[0].State != IPStateUnknown {
		t.Errorf("gateway address after POST: %+v", m)
	}
	if w = do(mux, "PATCH", "/api/v1/subnets/"+s.ID, `{"gateway":"10.9.2.1"}`, "org-1"); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"gateway"`) {
		t.Errorf("PATCH gateway outside: %d %s, want 422 gateway", w.Code, w.Body.String())
	}
	if w = do(mux, "PATCH", "/api/v1/subnets/"+s.ID, `{"gateway":"bad"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH invalid gateway: %d, want 400", w.Code)
	}
	// The gateway 10.9.0.1 lies outside the new prefix 10.9.0.128/25.
	if w = do(mux, "PATCH", "/api/v1/subnets/"+s.ID, `{"cidr":"10.9.0.128/25"}`, "org-1"); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"gateway"`) {
		t.Errorf("PATCH cidr without the gateway: %d %s, want 422 gateway", w.Code, w.Body.String())
	}
	if w = do(mux, "PATCH", "/api/v1/subnets/"+s.ID, `{"gateway":"10.9.0.254"}`, "org-1"); w.Code != http.StatusOK {
		t.Fatalf("PATCH gateway: %d %s", w.Code, w.Body.String())
	}
	if m := marked(); len(m) != 1 || m[0].Address != "10.9.0.254" {
		t.Errorf("gateway mark after the change: %+v", m)
	}
	// The gateway address keeps its subnet and stays (422 subnet_id, 409
	// is_gateway) until the subnet's gateway changes.
	gwID := marked()[0].ID
	if w = do(mux, "PATCH", "/api/v1/ips/"+gwID, `{"subnet_id":""}`, "org-1"); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"subnet_id"`) {
		t.Errorf("PATCH gateway address out of its subnet: %d %s, want 422 subnet_id", w.Code, w.Body.String())
	}
	if w = do(mux, "DELETE", "/api/v1/ips/"+gwID, "", "org-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"is_gateway"`) {
		t.Errorf("DELETE gateway address: %d %s, want 409 is_gateway", w.Code, w.Body.String())
	}
	if w = do(mux, "PATCH", "/api/v1/subnets/"+s.ID, `{"gateway":""}`, "org-1"); w.Code != http.StatusOK {
		t.Fatalf("PATCH clear gateway: %d %s", w.Code, w.Body.String())
	}
	if m := marked(); len(m) != 0 {
		t.Errorf("gateway mark after clearing: %+v", m)
	}
	all, _, _ := repo.ListIPAddresses(context.Background(), "org-1", IPFilter{SubnetID: s.ID}, page)
	if len(all) != 2 {
		t.Fatalf("addresses after clearing the gateway: %+v, want both former gateways", all)
	}
	if w = do(mux, "PATCH", "/api/v1/ips/"+all[0].ID, `{"state":"up"}`, "org-1"); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("PATCH state up: %d, want 422", w.Code)
	}
	if w = do(mux, "PATCH", "/api/v1/ips/"+all[0].ID, `{"state":"offline"}`, "org-1"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"state":"offline"`) || !strings.Contains(w.Body.String(), "state_changed_at") {
		t.Errorf("PATCH state offline: %d %s", w.Code, w.Body.String())
	}
	if w = do(mux, "PATCH", "/api/v1/ips/"+all[0].ID, `{"is_gateway":true}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH is_gateway: %d, want 400 (only the subnet sets it)", w.Code)
	}
	if w = do(mux, "GET", "/api/v1/ips?state=nope", "", "org-1"); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("GET ?state=nope: %d, want 422", w.Code)
	}
}

// TestStateFromPHPIPAMTag pins the mapping of the phpIPAM tags (IPM-04).
func TestStateFromPHPIPAMTag(t *testing.T) {
	for tag, want := range map[string][2]string{
		"Used":     {IPStateActive, ""},
		"Offline":  {IPStateOffline, ""},
		"Reserved": {IPStateUnknown, IPTypeReserved},
		"DHCP":     {IPStateUnknown, IPTypeDHCP},
	} {
		state, ipType, ok := StateFromPHPIPAMTag(tag)
		if !ok || state != want[0] || ipType != want[1] {
			t.Errorf("%s = (%q, %q, %v), want (%q, %q, true)", tag, state, ipType, ok, want[0], want[1])
		}
	}
	if _, _, ok := StateFromPHPIPAMTag("Gone"); ok {
		t.Error("unknown tag mapped")
	}
}
