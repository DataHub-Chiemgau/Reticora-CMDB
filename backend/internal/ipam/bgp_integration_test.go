package ipam_test

// WP-1068 (IPM-17, E-217): BGP peerings as target inventory against
// PostgreSQL as reticora_app.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestBGP' ./internal/ipam/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// bgpEnv is the peering scenario: an org-wide principal and a principal of
// client 1 in organization A; router of client 1 with the addresses
// 192.0.2.1 and 2001:db8::1 bound to it, router2 of client 1 with
// 192.0.2.2.
type bgpEnv struct {
	t                        *testing.T
	f                        *scopetest.Fixture
	org, client1             ifaceAPI
	router, router2          string
	local4, local6, foreign4 string
}

func newBGPEnv(t *testing.T) *bgpEnv {
	t.Helper()
	f := scopetest.Seed(t, "9e")
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Clients = database.ScopeIDs(f.Client1)
	e := &bgpEnv{t: t, f: f, org: newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA),
		client1: newIfaceAPI(f, database.ContextWithTenantScope(context.Background(), &scope), f.OrgA)}
	e.router = f.CI(t, f.OrgA, f.Client1, "bgp-router")
	e.router2 = f.CI(t, f.OrgA, f.Client1, "bgp-router2")
	e.local4 = e.id(e.org, "/api/v1/ips", `{"address":"192.0.2.1","ci_id":"`+e.router+`"}`)
	e.local6 = e.id(e.org, "/api/v1/ips", `{"address":"2001:db8::1","ci_id":"`+e.router+`"}`)
	e.foreign4 = e.id(e.org, "/api/v1/ips", `{"address":"192.0.2.2","ci_id":"`+e.router2+`"}`)
	return e
}

func (e *bgpEnv) call(a ifaceAPI, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	w := a.call(method, path, body)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func (e *bgpEnv) id(a ifaceAPI, path, body string) string {
	e.t.Helper()
	code, out := e.call(a, http.MethodPost, path, body)
	if code != http.StatusCreated {
		e.t.Fatalf("POST %s %s = %d %v", path, body, code, out)
	}
	return out["id"].(string)
}

func (e *bgpEnv) want(a ifaceAPI, method, path, body string, status int, field string) map[string]any {
	e.t.Helper()
	code, out := e.call(a, method, path, body)
	if code != status || (field != "" && out["field"] != field) {
		e.t.Errorf("%s %s %s = %d %v, want %d %s", method, path, body, code, out, status, field)
	}
	return out
}

// peering is the body of a peering of the router to remote with extra
// fields.
func (e *bgpEnv) peering(remote, extra string) string {
	body := `{"device_ci_id":"` + e.router + `","remote_asn_number":"65010","remote_address":"` + remote + `"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// TestBGPPeeringScopeFromDevice covers AK3: client and site come from the
// device and follow its client.
func TestBGPPeeringScopeFromDevice(t *testing.T) {
	e := newBGPEnv(t)
	id := e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.1", ""))
	if out := e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings/"+id, "", http.StatusOK, ""); out["client_id"] != e.f.Client1 {
		t.Errorf("client of the peering %v, want %s", out["client_id"], e.f.Client1)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, e.router, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	var client string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT client_id::text FROM bgp_peering WHERE id = $1`, id).Scan(&client); err != nil {
		t.Fatal(err)
	}
	if client != e.f.Client2 {
		t.Errorf("client after the device moved: %s, want %s", client, e.f.Client2)
	}
}

// TestBGPPeeringUnique409: a second peering of the device with the remote
// address in the VRF is 409; another VRF is allowed.
func TestBGPPeeringUnique409(t *testing.T) {
	e := newBGPEnv(t)
	e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.1", ""))
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"remote_asn_number":"65011"`), http.StatusConflict, "remote_address")
	vrf := e.id(e.org, "/api/v1/vrfs", `{"name":"bgp-other"}`)
	e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"vrf_id":"`+vrf+`"`))
	id := e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.2", ""))
	e.want(e.org, http.MethodPatch, "/api/v1/bgp-peerings/"+id, `{"remote_address":"198.51.100.1"}`, http.StatusConflict, "remote_address")
}

// TestBGPPeeringLocalAddressOtherDevice422: the local address must be bound
// to the device.
func TestBGPPeeringLocalAddressOtherDevice422(t *testing.T) {
	e := newBGPEnv(t)
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"local_address_id":"`+e.foreign4+`"`),
		http.StatusUnprocessableEntity, "local_address_id")
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"local_address_id":"`+e.f.ID()+`"`),
		http.StatusUnprocessableEntity, "local_address_id")
	out := e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"local_address_id":"`+e.local4+`"`),
		http.StatusCreated, "")
	if out["local_address"] != "192.0.2.1" {
		t.Errorf("local address %v, want 192.0.2.1", out["local_address"])
	}
	// The other validations of AK4.
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.9", `"remote_asn_number":"0"`),
		http.StatusUnprocessableEntity, "remote_asn_number")
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.0/24", ""), http.StatusUnprocessableEntity, "remote_address")
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.9", `"expected_state":"idle"`),
		http.StatusUnprocessableEntity, "expected_state")
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.9", `"vrf_id":"`+e.f.ID()+`"`),
		http.StatusUnprocessableEntity, "vrf_id")
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.9", `"local_asn_id":"`+e.f.ID()+`"`),
		http.StatusUnprocessableEntity, "local_asn_id")
	// A local ASN of another client than the device.
	other := e.id(e.org, "/api/v1/asns", `{"number":"64600","name":"bgp-c2","kind":"customer","client_id":"`+e.f.Client2+`"}`)
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.9", `"local_asn_id":"`+other+`"`),
		http.StatusUnprocessableEntity, "local_asn_id")
	own := e.id(e.org, "/api/v1/asns", `{"number":"64601","name":"bgp-own","kind":"own"}`)
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.9", `"local_asn_id":"`+own+`","remote_asn_number":"1.10"`),
		http.StatusCreated, "")
	// Deleting the local address clears the reference.
	id := e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.10", `"local_address_id":"`+e.local4+`"`))
	e.want(e.org, http.MethodDelete, "/api/v1/ips/"+e.local4, "", http.StatusNoContent, "")
	if out := e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings/"+id, "", http.StatusOK, ""); out["local_address_id"] != nil {
		t.Errorf("local address after its deletion: %v", out["local_address_id"])
	}
}

// TestBGPPeeringAddressFamily422: remote and local address share their
// family.
func TestBGPPeeringAddressFamily422(t *testing.T) {
	e := newBGPEnv(t)
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("2001:db8::2", `"local_address_id":"`+e.local4+`"`),
		http.StatusUnprocessableEntity, "remote_address")
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"local_address_id":"`+e.local6+`"`),
		http.StatusUnprocessableEntity, "remote_address")
	id := e.id(e.org, "/api/v1/bgp-peerings", e.peering("2001:db8::2", `"local_address_id":"`+e.local6+`"`))
	e.want(e.org, http.MethodPatch, "/api/v1/bgp-peerings/"+id, `{"remote_address":"198.51.100.3"}`, http.StatusUnprocessableEntity, "remote_address")
	// An IPv6 zone is no inet value: 422, not the failed cast.
	e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("fe80::1%eth0", ""), http.StatusUnprocessableEntity, "remote_address")
	e.want(e.org, http.MethodPatch, "/api/v1/bgp-peerings/"+id, `{"remote_address":"fe80::1%eth0"}`, http.StatusUnprocessableEntity, "remote_address")
}

// TestBGPPeeringDefaultVRF: without vrf_id the default VRF of the
// organization.
func TestBGPPeeringDefaultVRF(t *testing.T) {
	e := newBGPEnv(t)
	out := e.want(e.org, http.MethodPost, "/api/v1/bgp-peerings", e.peering("198.51.100.1", ""), http.StatusCreated, "")
	var def string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT default_vrf_of($1)::text`, e.f.OrgA).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if out["vrf_id"] != def || out["expected_state"] != "established" {
		t.Errorf("peering without vrf_id: %v, want the default VRF %s and established", out, def)
	}
}

// TestBGPPeeringRemoteASNResolvedOnRead covers AK5: an AS number recorded
// after the peering appears on it without a write; the asdot form is
// returned.
func TestBGPPeeringRemoteASNResolvedOnRead(t *testing.T) {
	e := newBGPEnv(t)
	id := e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"remote_asn_number":"AS1.10"`))
	out := e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings/"+id, "", http.StatusOK, "")
	if out["remote_asn"] != nil || out["remote_asn_number"] != float64(65546) || out["remote_asn_asdot"] != "1.10" {
		t.Errorf("before the AS number exists: %v", out)
	}
	asn := e.id(e.org, "/api/v1/asns", `{"number":"65546","name":"bgp-peer","kind":"peer"}`)
	out = e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings/"+id, "", http.StatusOK, "")
	ref, _ := out["remote_asn"].(map[string]any)
	if ref == nil || ref["id"] != asn || ref["name"] != "bgp-peer" || ref["kind"] != "peer" {
		t.Errorf("after the AS number was recorded: remote_asn %v", out["remote_asn"])
	}
	var updated int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM bgp_peering WHERE id = $1 AND updated_at > created_at`, id).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != 0 {
		t.Error("the peering was written when the AS number was recorded")
	}
}

// TestBGPPeeringClientPrincipal: the peering of a device of another client
// is invisible (404) and its device is 422 for a principal of client 1.
func TestBGPPeeringClientPrincipal(t *testing.T) {
	e := newBGPEnv(t)
	foreign := e.f.CI(t, e.f.OrgA, e.f.Client2, "bgp-foreign")
	hidden := e.id(e.org, "/api/v1/bgp-peerings", `{"device_ci_id":"`+foreign+`","remote_asn_number":"65010","remote_address":"198.51.100.7"}`)
	own := e.id(e.client1, "/api/v1/bgp-peerings", e.peering("198.51.100.8", ""))
	e.want(e.client1, http.MethodGet, "/api/v1/bgp-peerings/"+hidden, "", http.StatusNotFound, "")
	e.want(e.client1, http.MethodPatch, "/api/v1/bgp-peerings/"+hidden, `{"description":"x"}`, http.StatusNotFound, "")
	e.want(e.client1, http.MethodDelete, "/api/v1/bgp-peerings/"+hidden, "", http.StatusNotFound, "")
	list := e.want(e.client1, http.MethodGet, "/api/v1/bgp-peerings", "", http.StatusOK, "")
	if ids := listIDs(list); ids != own {
		t.Errorf("client 1 list: %s, want only %s", ids, own)
	}
	e.want(e.client1, http.MethodPost, "/api/v1/bgp-peerings", `{"device_ci_id":"`+foreign+`","remote_asn_number":"65010","remote_address":"198.51.100.9"}`,
		http.StatusUnprocessableEntity, "device_ci_id")
}

// TestBGPPeeringListByCI covers the filters of the list.
func TestBGPPeeringListByCI(t *testing.T) {
	e := newBGPEnv(t)
	a := e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.1", `"expected_state":"admin_down"`))
	b := e.id(e.org, "/api/v1/bgp-peerings", e.peering("198.51.100.2", `"remote_asn_number":"65020"`))
	c := e.id(e.org, "/api/v1/bgp-peerings", `{"device_ci_id":"`+e.router2+`","remote_asn_number":"65010","remote_address":"198.51.100.3"}`)
	for query, want := range map[string]string{
		"ci_id=" + e.router:              a + "," + b,
		"ci_id=" + e.router2:             c,
		"remote_asn_number=65020":        b,
		"remote_asn_number=AS65010":      sortedIDs(a, c),
		"expected_state=admin_down":      a,
		"ci_id=" + e.router + "&limit=1": a,
	} {
		list := e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings?"+query, "", http.StatusOK, "")
		got := listIDs(list)
		if strings.Contains(want, ",") && !strings.HasPrefix(query, "ci_id") {
			got = sortedIDs(strings.Split(got, ",")...)
		}
		if got != want {
			t.Errorf("%s: %s, want %s", query, got, want)
		}
	}
	e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings?expected_state=idle", "", http.StatusUnprocessableEntity, "expected_state")
	e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings?remote_asn_number=x", "", http.StatusUnprocessableEntity, "remote_asn_number")
	e.want(e.org, http.MethodGet, "/api/v1/bgp-peerings?ci_id=x", "", http.StatusUnprocessableEntity, "ci_id")
}

func sortedIDs(ids ...string) string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return strings.Join(out, ",")
}

// TestBGPIPAddressKeyCreatedOnce covers AK2 of WP-1068: with the key (id,
// organization_id) of ip_address in place the up migration creates no
// second one, and the down migration keeps it while another foreign key
// (nat_translation, WP-1071) refers to it. Run inside a rolled-back
// transaction. The plan names it TestIPAddressKeyCreatedOnce; that name
// holds the test of the NAT migration (WP-1071) in this package.
func TestBGPIPAddressKeyCreatedOnce(t *testing.T) {
	e := newBGPEnv(t)
	read := func(suffix string) string {
		t.Helper()
		files, err := filepath.Glob("../../migrations/*_bgp_peering." + suffix + ".sql")
		if err != nil || len(files) != 1 {
			t.Fatalf("migration bgp_peering %s: %v %v", suffix, files, err)
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	bg := context.Background()
	tx, err := e.f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	keys := func() int {
		t.Helper()
		var n int
		if err = tx.QueryRow(bg, `SELECT count(*) FROM pg_constraint c WHERE c.conrelid = 'ip_address'::regclass AND c.contype IN ('u', 'p')
			AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
			      WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)) = ARRAY['id', 'organization_id']`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err = tx.Exec(bg, read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := keys(); n != 1 {
		t.Errorf("after down: %d keys of ip_address, want the key nat_translation refers to", n)
	}
	if _, err = tx.Exec(bg, read("up")); err != nil {
		t.Fatalf("up: %v", err)
	}
	if n := keys(); n != 1 {
		t.Errorf("after up over the existing key: %d keys of ip_address, want 1", n)
	}
	// Without other references down drops the key, up creates it again.
	dropAddressKeyReferences(t, tx, "bgp_peering")
	if _, err = tx.Exec(bg, read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := keys(); n != 0 {
		t.Errorf("after down without reference: %d keys of ip_address, want 0", n)
	}
	if _, err = tx.Exec(bg, read("up")); err != nil {
		t.Fatalf("up: %v", err)
	}
	if n := keys(); n != 1 {
		t.Errorf("after up without key: %d keys of ip_address, want 1", n)
	}
}

// TestBGPWithoutDatabase covers AK6 without PostgreSQL: every peering
// route answers 501.
func TestBGPWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u"})
	id := "/api/v1/bgp-peerings/00000000-0000-0000-0000-000000000001"
	for _, c := range [][2]string{{http.MethodGet, "/api/v1/bgp-peerings"}, {http.MethodPost, "/api/v1/bgp-peerings"},
		{http.MethodGet, id}, {http.MethodPatch, id}, {http.MethodDelete, id}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(c[0], c[1], strings.NewReader("{}")).WithContext(ctx))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s without database: %d, want 501", c[0], c[1], w.Code)
		}
	}
}
