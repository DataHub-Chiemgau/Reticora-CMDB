package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSubnetReservationsOnPostgreSQL covers NET-09 (WP-299) against
// PostgreSQL: POST /subnets/{id}/reservations stores a row of type reserved
// with the VRF, client and site of the subnet and the reserving user, the
// list holds only the reservations of the subnet and pages by cursor, the
// release removes only a reservation of the subnet, an address the VRF
// already holds is a conflict, and Discovery leaves a reservation alone.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubnetReservationsOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "9d")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	orgBCtx := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}

	site := str(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'res-site') RETURNING id::text`, f.OrgA, f.Client1)
	vrf := str(`INSERT INTO vrf (organization_id, name) VALUES ($1, 'res-vrf') RETURNING id::text`, f.OrgA)
	subnet := str(`INSERT INTO subnet (organization_id, client_id, site_id, cidr, vrf_id) VALUES ($1, $2, $3, '10.93.0.0/24', $4)
		RETURNING id::text`, f.OrgA, f.Client1, site, vrf)
	sibling := str(`INSERT INTO subnet (organization_id, client_id, cidr, vrf_id) VALUES ($1, $2, '10.93.0.0/25', default_vrf_of($1))
		RETURNING id::text`, f.OrgA, f.Client1)
	foreign := str(`INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.93.1.0/24') RETURNING id::text`, f.OrgA, f.Client2)
	base := "/api/v1/subnets/" + subnet + "/reservations"

	// Create: 201 with type reserved, the VRF, client and site of the
	// subnet, the user and reserved_until. The address reads as inet text
	// (10.93.0.5/32), as GET /ips/{id} shows it.
	w := call(orgCtx, http.MethodPost, base, `{"address":"10.93.0.5","description":"printer","reserved_until":"2030-06-01T12:00:00+02:00"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST reservation: %d %s", w.Code, w.Body.String())
	}
	var res ipam.IPAddress
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Type != ipam.IPTypeReserved || res.SubnetID != subnet || res.InterfaceID != "" || res.VRFID != vrf ||
		strings.TrimSuffix(res.Address, "/32") != "10.93.0.5" || res.Description != "printer" || res.ReservedBy != f.User {
		t.Errorf("reservation = %+v, want reserved in the subnet and its VRF, by the user", res)
	}
	if res.ReservedUntil == nil || res.ReservedUntil.UTC().Format("2006-01-02T15:04:05Z07:00") != "2030-06-01T10:00:00Z" {
		t.Errorf("reserved_until = %v, want 2030-06-01T10:00:00Z", res.ReservedUntil)
	}
	var client, siteOf, vrfOf, by, until string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), vrf_id::text,
			COALESCE(reserved_by::text, ''), COALESCE(to_char(reserved_until AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS'), '')
		FROM ip_address WHERE id = $1`, res.ID).Scan(&client, &siteOf, &vrfOf, &by, &until); err != nil {
		t.Fatal(err)
	}
	if client != f.Client1 || siteOf != site || vrfOf != vrf || by != f.User || until != "2030-06-01T10:00:00" {
		t.Errorf("row: client %s site %s vrf %s by %s until %s, want the subnet's scope, VRF, the user and the time", client, siteOf, vrfOf, by, until)
	}
	// GET /ips/{id} and the addresses of the subnet show the fields.
	for _, path := range []string{"/api/v1/ips/" + res.ID, "/api/v1/subnets/" + subnet + "/addresses"} {
		body := call(orgCtx, http.MethodGet, path, "").Body.String()
		if !strings.Contains(body, `"reserved_by":"`+f.User+`"`) || !strings.Contains(body, `"reserved_until":"2030-06-01T10:00:00Z"`) {
			t.Errorf("GET %s lacks reserved_by or reserved_until: %s", path, body)
		}
	}

	// Errors.
	for name, c := range map[string]struct {
		ctx  context.Context
		path string
		body string
		want int
	}{
		"outside the subnet":    {orgCtx, base, `{"address":"10.93.1.5"}`, http.StatusBadRequest},
		"invalid address":       {orgCtx, base, `{"address":"10.93.0.256"}`, http.StatusBadRequest},
		"missing address":       {orgCtx, base, `{}`, http.StatusBadRequest},
		"subnet of another org": {orgBCtx, base, `{"address":"10.93.0.6"}`, http.StatusNotFound},
		"invisible subnet":      {client1Ctx, "/api/v1/subnets/" + foreign + "/reservations", `{"address":"10.93.1.6"}`, http.StatusNotFound},
		"unknown subnet":        {orgCtx, "/api/v1/subnets/" + f.ID() + "/reservations", `{"address":"10.93.0.6"}`, http.StatusNotFound},
		"second reservation":    {orgCtx, base, `{"address":"10.93.0.5"}`, http.StatusConflict},
	} {
		if got := call(c.ctx, http.MethodPost, c.path, c.body); got.Code != c.want {
			t.Errorf("%s: %d %s, want %d", name, got.Code, got.Body.String(), c.want)
		}
	}

	// An address on an interface in the VRF of the subnet: 409 naming the
	// address and the interface.
	ciID := f.CI(t, f.OrgA, f.Client1, "res-ci")
	nic := str(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth7') RETURNING id::text`, f.OrgA, ciID)
	if _, err := f.Admin.Exec(bg, `INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, type)
		VALUES ($1, $2, $3, '10.93.0.7/24', 'discovered')`, f.OrgA, subnet, nic); err != nil {
		t.Fatal(err)
	}
	w = call(orgCtx, http.MethodPost, base, `{"address":"10.93.0.7"}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "10.93.0.7") || !strings.Contains(w.Body.String(), nic) ||
		!strings.Contains(w.Body.String(), "eth7") {
		t.Errorf("address on an interface: %d %s, want 409 naming 10.93.0.7 and eth7 (%s)", w.Code, w.Body.String(), nic)
	}
	// The same address in a subnet of another VRF is allowed.
	if w = call(orgCtx, http.MethodPost, "/api/v1/subnets/"+sibling+"/reservations", `{"address":"10.93.0.7"}`); w.Code != http.StatusCreated {
		t.Errorf("same address in another VRF: %d %s, want 201", w.Code, w.Body.String())
	}

	// A static address of the subnet and a reservation of the sibling
	// subnet are no reservations of the subnet.
	static := str(`INSERT INTO ip_address (organization_id, subnet_id, address, type) VALUES ($1, $2, '10.93.0.9', 'static') RETURNING id::text`,
		f.OrgA, subnet)
	for _, address := range []string{"10.93.0.10", "10.93.0.11"} {
		if w = call(orgCtx, http.MethodPost, base, `{"address":"`+address+`"}`); w.Code != http.StatusCreated {
			t.Fatalf("POST %s: %d %s", address, w.Code, w.Body.String())
		}
	}
	type page struct {
		Data       []ipam.IPAddress `json:"data"`
		Total      int              `json:"total"`
		NextCursor string           `json:"next_cursor"`
	}
	seen := map[string]bool{}
	cursor := ""
	for i := 0; i < 5; i++ {
		path := base + "?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w = call(orgCtx, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Total != 3 {
			t.Errorf("total = %d, want the 3 reservations of the subnet", p.Total)
		}
		for _, a := range p.Data {
			if a.Type != ipam.IPTypeReserved || a.SubnetID != subnet {
				t.Errorf("listed %+v, want only reservations of the subnet", a)
			}
			seen[strings.TrimSuffix(a.Address, "/32")] = true
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 3 || !seen["10.93.0.5"] || !seen["10.93.0.10"] || !seen["10.93.0.11"] {
		t.Errorf("paged reservations = %v, want 10.93.0.5, 10.93.0.10 and 10.93.0.11", seen)
	}

	// Discovery: three syncs of the CI without the reserved address leave
	// the reservation unchanged.
	sync := func() {
		t.Helper()
		if _, err := repo.SyncDiscoveredInterfaces(orgCtx, f.OrgA, ciID,
			[]ipam.ObservedInterface{{Name: "eth7", IfIndex: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	before := str(`SELECT row_to_json(a)::text FROM ip_address a WHERE id = $1`, res.ID)
	for range 3 {
		sync()
	}
	if after := str(`SELECT row_to_json(a)::text FROM ip_address a WHERE id = $1`, res.ID); after != before {
		t.Errorf("reservation changed by discovery:\n%s\n%s", before, after)
	}

	// Release: 404 for a static address and through another subnet, 204
	// for the reservation.
	if w = call(orgCtx, http.MethodDelete, base+"/"+static, ""); w.Code != http.StatusNotFound {
		t.Errorf("DELETE static: %d, want 404", w.Code)
	}
	if w = call(orgCtx, http.MethodDelete, "/api/v1/subnets/"+sibling+"/reservations/"+res.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("DELETE through another subnet: %d, want 404", w.Code)
	}
	if w = call(orgCtx, http.MethodDelete, base+"/"+res.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE reservation: %d %s, want 204", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodDelete, base+"/"+res.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE: %d, want 404", w.Code)
	}
	var left int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ip_address WHERE id IN ($1, $2)`, res.ID, static).Scan(&left); err != nil || left != 1 {
		t.Errorf("rows left: %d (%v), want the static address only", left, err)
	}
}
