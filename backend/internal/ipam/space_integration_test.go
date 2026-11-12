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

// TestSubnetUsageAndFreeSpaceOnPostgreSQL covers IPM-03 (WP-903) against
// PostgreSQL:
//   - usage after a reservation and a gateway, free addresses skip both;
//   - a pool reports the coverage by its children, free subnets avoid them;
//   - a client principal of the subnet does not see an address of another
//     client in /addresses, but usage counts it as occupied and
//     free-addresses does not offer it (ipam_subnet_occupancy);
//   - a subnet the caller cannot see is 404; prefix_length not longer than
//     the subnet's is 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubnetUsageAndFreeSpaceOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "cc")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	get := func(ctx context.Context, path string, want int, v any) string {
		t.Helper()
		w := call(ctx, http.MethodGet, path, "")
		if w.Code != want {
			t.Fatalf("GET %s = %d %s, want %d", path, w.Code, w.Body.String(), want)
		}
		if v != nil {
			if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatal(err)
			}
		}
		return w.Body.String()
	}

	// Usage after a reservation and the gateway.
	w := call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.203.0.0/24","client_id":"`+f.Client1+`","gateway":"10.203.0.1"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /subnets: %d %s", w.Code, w.Body.String())
	}
	var subnet ipam.Subnet
	_ = json.Unmarshal(w.Body.Bytes(), &subnet)
	if w = call(orgCtx, http.MethodPost, "/api/v1/subnets/"+subnet.ID+"/reservations", `{"address":"10.203.0.5"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST reservation: %d %s", w.Code, w.Body.String())
	}
	base := "/api/v1/subnets/" + subnet.ID
	var u ipam.Usage
	get(orgCtx, base+"/usage", http.StatusOK, &u)
	if u.Total == nil || *u.Total != 254 || u.Used != 2 || *u.Free != 252 || u.UsedByType["static"] != 1 || u.UsedByType["reserved"] != 1 ||
		u.UsedByState["unknown"] != 2 || u.Percent != 0.8 || u.SubnetID != subnet.ID {
		t.Errorf("usage = %+v", u)
	}
	var free ipam.FreeAddresses
	get(orgCtx, base+"/free-addresses?limit=4", http.StatusOK, &free)
	if strings.Join(free.Addresses, ",") != "10.203.0.2,10.203.0.3,10.203.0.4,10.203.0.6" {
		t.Errorf("free addresses = %v", free.Addresses)
	}

	// A pool with children.
	w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.204.0.0/16","is_pool":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST pool: %d %s", w.Code, w.Body.String())
	}
	var pool ipam.Subnet
	_ = json.Unmarshal(w.Body.Bytes(), &pool)
	for _, c := range []string{"10.204.0.0/24", "10.204.1.0/24"} {
		if w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"`+c+`"}`); w.Code != http.StatusCreated {
			t.Fatalf("POST child: %d %s", w.Code, w.Body.String())
		}
	}
	get(orgCtx, "/api/v1/subnets/"+pool.ID+"/usage", http.StatusOK, &u)
	if !u.IsPool || u.Covered == nil || *u.Covered != 512 || *u.Total != 65536 || u.Percent != 0.8 {
		t.Errorf("pool usage = %+v", u)
	}
	var subnets ipam.FreeSubnets
	get(orgCtx, "/api/v1/subnets/"+pool.ID+"/free-subnets?prefix_length=24&limit=2", http.StatusOK, &subnets)
	if strings.Join(subnets.Subnets, ",") != "10.204.2.0/24,10.204.3.0/24" {
		t.Errorf("free /24 of the pool = %v", subnets.Subnets)
	}
	get(orgCtx, "/api/v1/subnets/"+pool.ID+"/free-addresses", http.StatusConflict, nil)
	if body := get(orgCtx, "/api/v1/subnets/"+pool.ID+"/free-subnets?prefix_length=16", http.StatusUnprocessableEntity, nil); !strings.Contains(body, "prefix_length") {
		t.Errorf("prefix_length 16 of a /16: %s", body)
	}

	// An address of client 2 in the subnet of client 1: on an interface of
	// a client-2 CI, so the address belongs to client 2.
	ci2 := f.CI(t, f.OrgA, f.Client2, "cc-foreign-ci")
	nic := str(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`, f.OrgA, ci2)
	foreign := str(`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address) VALUES ($1, $2, $3, '10.203.0.2')
		RETURNING id::text`, f.OrgA, subnet.ID, nic)
	if c := str(`SELECT COALESCE(client_id::text, '') FROM ip_address WHERE id = $1`, foreign); c != f.Client2 {
		t.Fatalf("client of the foreign address = %q, want client 2", c)
	}
	if body := get(client1Ctx, base+"/addresses?limit=200", http.StatusOK, nil); strings.Contains(body, foreign) {
		t.Error("client 1 sees the address of client 2")
	}
	get(client1Ctx, base+"/usage", http.StatusOK, &u)
	if u.Used != 3 {
		t.Errorf("client 1 usage counts %d, want 3 (the foreign address included)", u.Used)
	}
	get(client1Ctx, base+"/free-addresses?limit=2", http.StatusOK, &free)
	if strings.Join(free.Addresses, ",") != "10.203.0.3,10.203.0.4" {
		t.Errorf("client 1 free addresses = %v, want the foreign address skipped", free.Addresses)
	}

	// A subnet the caller cannot see: 404.
	hidden := str(`INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.205.0.0/24') RETURNING id::text`, f.OrgA, f.Client2)
	for _, path := range []string{"/usage", "/free-addresses", "/free-subnets?prefix_length=26"} {
		get(client1Ctx, "/api/v1/subnets/"+hidden+path, http.StatusNotFound, nil)
	}
	get(orgCtx, "/api/v1/subnets/not-a-uuid/usage", http.StatusNotFound, nil)
	// The function itself refuses an invisible subnet before it widens the
	// scope.
	tx, err := f.App.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, `SELECT set_config('app.org_id', $1, true), set_config('app.client_scope', $2, true)`, f.OrgA, f.Client1); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, `SELECT * FROM ipam_subnet_occupancy($1)`, hidden); err == nil || !strings.Contains(err.Error(), "P0002") {
		t.Errorf("occupancy of an invisible subnet: %v, want no_data_found", err)
	}
}
