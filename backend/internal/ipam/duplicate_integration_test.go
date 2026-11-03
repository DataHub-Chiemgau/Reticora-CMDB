package ipam_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestDuplicateIPOnRESTWrites covers WP-300 (NET-06, E-53) against
// PostgreSQL for the REST writes of IPAM:
//   - POST /api/v1/ips (static, no interface, host notation) next to an
//     address discovery stored in CIDR notation at an interface opens a
//     duplicate_ip item;
//   - PATCH /api/v1/ips/{id} moving an address to an interface of another
//     CI opens one; moving it between interfaces of one CI does not;
//   - PATCH /api/v1/subnets/{id} with vrf_id opens one for the addresses
//     the subnet takes into the VRF;
//   - a second address without interface is 409 and opens nothing;
//   - a principal restricted to a client opens the item (the check is
//     organization-wide); only principals who see all candidate CIs read it.
//
// Status codes stay those of the write.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDuplicateIPOnRESTWrites(t *testing.T) {
	f := scopetest.Seed(t, "9d")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	svc := reviews.NewService(reviews.NewPGRepository(f.App))
	mux := chi.NewRouter()
	ipam.NewHandler(repo).WithReviews(svc).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w
	}
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	defaultVRF := str(`SELECT default_vrf_of($1)::text`, f.OrgA)
	nic := func(ciID, name string) string {
		return str(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, $3) RETURNING id::text`, f.OrgA, ciID, name)
	}
	observed := func(ifID, address, vrf string) string {
		return str(`INSERT INTO ip_address (organization_id, interface_id, address, type, vrf_id) VALUES ($1, $2, $3::inet, 'discovered', $4)
			RETURNING id::text`, f.OrgA, ifID, address, vrf)
	}
	items := func(ctx context.Context, objectKey string) []reviews.Item {
		t.Helper()
		out, _, err := svc.List(ctx, f.OrgA, reviews.Filter{Kind: reviews.KindDuplicateIP, ObjectKey: objectKey},
			api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := func(name, objectKey string, holders int, cis ...string) {
		t.Helper()
		got := items(orgCtx, objectKey)
		if len(got) != 1 || got[0].Status != reviews.StatusOpen {
			t.Fatalf("%s: items %+v, want one open item %s", name, got, objectKey)
		}
		h, _ := got[0].Payload["holders"].([]any)
		slices.Sort(cis)
		if len(h) != holders || !slices.Equal(got[0].CandidateCIIDs, cis) {
			t.Errorf("%s: %d holders, candidates %v, want %d holders and %v", name, len(h), got[0].CandidateCIIDs, holders, cis)
		}
	}
	x, y := f.CI(t, f.OrgA, "", "dup-x"), f.CI(t, f.OrgA, "", "dup-y")
	xEth0, xEth1, yEth0 := nic(x, "eth0"), nic(x, "eth1"), nic(y, "eth0")

	// POST /ips beside an observed address in CIDR notation.
	counted := testutil.ToFloat64(ipam.DuplicateIPReviews)
	observed(xEth0, "10.94.0.5/24", defaultVRF)
	if w := call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.94.0.5","type":"static"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ips: %d %s", w.Code, w.Body.String())
	}
	want("POST /ips", "ip:"+defaultVRF+"/10.94.0.5", 2, x)
	if n := testutil.ToFloat64(ipam.DuplicateIPReviews) - counted; n != 1 {
		t.Errorf("POST /ips: counter +%v, want 1", n)
	}
	// The alias path checks as well; the open item is reused.
	observed(yEth0, "10.94.0.5/24", defaultVRF)
	if w := call(orgCtx, http.MethodPost, "/api/v1/ip-addresses", `{"address":"10.94.0.5","type":"dhcp","interface_id":"`+xEth1+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ip-addresses: %d %s", w.Code, w.Body.String())
	}
	if got := items(orgCtx, "ip:"+defaultVRF+"/10.94.0.5"); len(got) != 1 {
		t.Errorf("POST /ip-addresses: %d items, want the open one reused", len(got))
	}

	// PATCH /ips/{id}: between interfaces of one CI no item, to another CI
	// one.
	observed(xEth0, "10.94.0.6/24", defaultVRF)
	moved := observed(xEth1, "10.94.0.6/24", defaultVRF)
	if w := call(orgCtx, http.MethodPatch, "/api/v1/ips/"+moved, `{"type":"static"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH type: %d %s", w.Code, w.Body.String())
	}
	if got := items(orgCtx, "ip:"+defaultVRF+"/10.94.0.6"); len(got) != 0 {
		t.Errorf("two interfaces of one CI: %d items, want none", len(got))
	}
	if w := call(orgCtx, http.MethodPatch, "/api/v1/ips/"+moved, `{"interface_id":"`+yEth0+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH interface_id: %d %s", w.Code, w.Body.String())
	}
	want("PATCH /ips", "ip:"+defaultVRF+"/10.94.0.6", 2, x, y)

	// PATCH /subnets/{id} with vrf_id: the subnet's addresses move into the
	// VRF of an observed address.
	vrf := str(`INSERT INTO vrf (organization_id, name) VALUES ($1, 'dup-rest') RETURNING id::text`, f.OrgA)
	observed(yEth0, "10.94.1.5/24", vrf)
	subnet := str(`INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.94.1.0/24') RETURNING id::text`, f.OrgA)
	if w := call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.94.1.5","type":"static","subnet_id":"`+subnet+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("POST /ips in the subnet: %d %s", w.Code, w.Body.String())
	}
	if got := items(orgCtx, "ip:"+vrf+"/10.94.1.5"); len(got) != 0 {
		t.Fatalf("before the move: %d items, want none", len(got))
	}
	if w := call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+subnet, `{"vrf_id":"`+vrf+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH /subnets: %d %s", w.Code, w.Body.String())
	}
	want("PATCH /subnets", "ip:"+vrf+"/10.94.1.5", 2, y)

	// A second address without interface is 409 and opens nothing.
	if w := call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.94.2.5","type":"static"}`); w.Code != http.StatusCreated {
		t.Fatalf("first address: %d %s", w.Code, w.Body.String())
	}
	if w := call(orgCtx, http.MethodPost, "/api/v1/ips", `{"address":"10.94.2.5","type":"dhcp"}`); w.Code != http.StatusConflict {
		t.Errorf("second address without interface: %d %s, want 409", w.Code, w.Body.String())
	}
	if got := items(orgCtx, "ip:"+defaultVRF+"/10.94.2.5"); len(got) != 0 {
		t.Errorf("409: %d items, want none", len(got))
	}

	// A principal of client 1 writes next to an address of a client-2 CI:
	// the item exists, but only the org-wide principal reads it.
	foreign := f.CI(t, f.OrgA, f.Client2, "dup-c2")
	observed(nic(foreign, "eth0"), "10.94.3.5/24", defaultVRF)
	own := str(`INSERT INTO subnet (organization_id, client_id, cidr) VALUES ($1, $2, '10.94.3.0/24') RETURNING id::text`,
		f.OrgA, f.Client1)
	w := call(client1Ctx, http.MethodPost, "/api/v1/ips", `{"address":"10.94.3.5","type":"static","subnet_id":"`+own+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("client-1 POST /ips: %d %s", w.Code, w.Body.String())
	}
	want("client scope", "ip:"+defaultVRF+"/10.94.3.5", 2, foreign)
	if got := items(client1Ctx, "ip:"+defaultVRF+"/10.94.3.5"); len(got) != 0 {
		t.Errorf("client-1 principal reads the item of a client-2 CI: %+v", got)
	}
}
