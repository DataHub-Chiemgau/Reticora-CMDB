package ipam_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

// bindingEnv is the scenario of the device binding (IPM-13, WP-911, E-215):
// CI A of client 1 with interface eth0, CI B of client 1, CI C of
// organization B.
type bindingEnv struct {
	t                 *testing.T
	f                 *scopetest.Fixture
	api               ifaceAPI
	ciA, ciB, ciOther string
	nicA, nicB        string
}

func newBindingEnv(t *testing.T) *bindingEnv {
	t.Helper()
	f := scopetest.Seed(t, "8d")
	e := &bindingEnv{t: t, f: f, api: newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA)}
	e.ciA = f.CI(t, f.OrgA, f.Client1, "bind-a")
	e.ciB = f.CI(t, f.OrgA, f.Client1, "bind-b")
	e.ciOther = f.CI(t, f.OrgB, "", "bind-other")
	e.nicA = e.create("/api/v1/cis/"+e.ciA+"/interfaces", `{"name":"eth0"}`)["id"].(string)
	e.nicB = e.create("/api/v1/cis/"+e.ciB+"/interfaces", `{"name":"eth0"}`)["id"].(string)
	return e
}

func (e *bindingEnv) create(path, body string) map[string]any {
	e.t.Helper()
	w := e.api.call(http.MethodPost, path, body)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("POST %s %s = %d %s", path, body, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *bindingEnv) scalar(q string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), q, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return out
}

// refused checks a 422 with the field.
func (e *bindingEnv) refused(method, path, body, field string) {
	e.t.Helper()
	w := e.api.call(method, path, body)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
		e.t.Errorf("%s %s %s = %d %s, want 422 %s", method, path, body, w.Code, w.Body.String(), field)
	}
}

// TestAddressCIFollowsInterface covers AK1, AK2 and AK4: an address with
// interface takes the CI of the interface; a different, an unknown or a
// foreign CI and an invalid role are 422.
func TestAddressCIFollowsInterface(t *testing.T) {
	e := newBindingEnv(t)
	a := e.create("/api/v1/ips", `{"address":"10.91.0.1","interface_id":"`+e.nicA+`"}`)
	if a["ci_id"] != e.ciA || a["role"] != "primary" {
		t.Errorf("address of eth0 of CI A: ci_id %v role %v, want %s primary", a["ci_id"], a["role"], e.ciA)
	}
	e.create("/api/v1/ips", `{"address":"10.91.0.2","interface_id":"`+e.nicA+`","ci_id":"`+e.ciA+`","role":"secondary"}`)
	e.refused(http.MethodPost, "/api/v1/ips", `{"address":"10.91.0.3","interface_id":"`+e.nicA+`","ci_id":"`+e.ciB+`"}`, "ci_id")
	e.refused(http.MethodPost, "/api/v1/ips", `{"address":"10.91.0.4","ci_id":"`+e.f.ID()+`"}`, "ci_id")
	e.refused(http.MethodPost, "/api/v1/ips", `{"address":"10.91.0.5","ci_id":"`+e.ciOther+`"}`, "ci_id")
	e.refused(http.MethodPost, "/api/v1/ips", `{"address":"10.91.0.6","ci_id":"not-a-uuid"}`, "ci_id")
	e.refused(http.MethodPost, "/api/v1/ips", `{"address":"10.91.0.7","role":"owner"}`, "role")
	id := a["id"].(string)
	e.refused(http.MethodPatch, "/api/v1/ips/"+id, `{"ci_id":"`+e.ciB+`"}`, "ci_id")
	e.refused(http.MethodPatch, "/api/v1/ips/"+id, `{"role":"master"}`, "role")
	if w := e.api.call(http.MethodPatch, "/api/v1/ips/"+id, `{"role":"loopback"}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"role":"loopback"`) {
		t.Errorf("PATCH role = %d %s", w.Code, w.Body.String())
	}
	// Clearing ci_id of an address with interface takes the interface's CI again.
	if w := e.api.call(http.MethodPatch, "/api/v1/ips/"+id, `{"ci_id":""}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"ci_id":"`+e.ciA+`"`) {
		t.Errorf("PATCH ci_id empty = %d %s, want the CI of the interface", w.Code, w.Body.String())
	}
	// The filter ci_id reads ip_address.ci_id.
	w := e.api.call(http.MethodGet, "/api/v1/ips?ci_id="+e.ciA, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Errorf("GET /ips?ci_id = %d %s", w.Code, w.Body.String())
	}
}

// TestAddressCIBackfill covers AK1: the backfill of migration 000321 sets
// ci_id of every address with interface to the CI of the interface.
func TestAddressCIBackfill(t *testing.T) {
	e := newBindingEnv(t)
	bg := context.Background()
	raw, err := os.ReadFile("../../migrations/000321_ip_address_ci.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	backfill := regexp.MustCompile(`(?s)UPDATE ip_address a SET ci_id = n\.ci_id.*?;`).Find(raw)
	if backfill == nil {
		t.Fatal("the backfill statement of 000321 is missing")
	}
	// An address written as before the migration: interface, no CI.
	conn, err := e.f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var id string
	if _, err = conn.Exec(bg, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	err = conn.QueryRow(bg, `INSERT INTO ip_address (organization_id, interface_id, address, vrf_id)
		VALUES ($1, $2, '10.91.1.1', default_vrf_of($1)) RETURNING id::text`, e.f.OrgA, e.nicA).Scan(&id)
	_, _ = conn.Exec(bg, `SET session_replication_role = DEFAULT`)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.scalar(`SELECT COALESCE(ci_id::text, '') FROM ip_address WHERE id = $1`, id); got != "" {
		t.Fatalf("seeded address has ci_id %s", got)
	}
	if _, err = e.f.Admin.Exec(bg, string(backfill)); err != nil {
		t.Fatal(err)
	}
	if got := e.scalar(`SELECT COALESCE(ci_id::text, '') FROM ip_address WHERE id = $1`, id); got != e.ciA {
		t.Errorf("backfilled ci_id = %q, want %s", got, e.ciA)
	}
	if n := e.scalar(`SELECT count(*)::text FROM ip_address a JOIN network_interface n ON n.id = a.interface_id
		WHERE a.organization_id = $1 AND a.ci_id IS DISTINCT FROM n.ci_id`, e.f.OrgA); n != "0" {
		t.Errorf("%s addresses with interface and another CI", n)
	}
}

// TestInterfacelessAddressBoundToCI covers AK3 and AK4: a VIP without
// interface and subnet bound to CI A takes client and site from the CI.
func TestInterfacelessAddressBoundToCI(t *testing.T) {
	e := newBindingEnv(t)
	vip := e.create("/api/v1/ips", `{"address":"10.91.2.1","ci_id":"`+e.ciA+`","role":"vip"}`)
	if vip["ci_id"] != e.ciA || vip["role"] != "vip" || vip["interface_id"] != nil {
		t.Errorf("VIP = %v", vip)
	}
	if got := e.scalar(`SELECT COALESCE(client_id::text, '') FROM ip_address WHERE id = $1`, vip["id"]); got != e.f.Client1 {
		t.Errorf("client of the VIP = %q, want the client of CI A %s", got, e.f.Client1)
	}
	// A principal of client 2 does not see it (the CI is of client 1).
	client2 := newIfaceAPI(e.f, e.f.ClientCtx(e.f.Client2), e.f.OrgA)
	if w := client2.call(http.MethodGet, "/api/v1/ips/"+vip["id"].(string), ""); w.Code != http.StatusNotFound {
		t.Errorf("client 2 reads the VIP of CI A: %d", w.Code)
	}
	// Moving the VIP to CI B.
	if w := e.api.call(http.MethodPatch, "/api/v1/ips/"+vip["id"].(string), `{"ci_id":"`+e.ciB+`"}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"ci_id":"`+e.ciB+`"`) {
		t.Errorf("PATCH ci_id of the VIP = %d %s", w.Code, w.Body.String())
	}
}

// TestCIAddressesList covers AK5: GET /cis/{id}/addresses lists the
// addresses of the CI with interface, subnet, VRF, state, type and role;
// a CI of another organization and an unknown one are 404; a principal of
// a client sees only the addresses visible to it.
func TestCIAddressesList(t *testing.T) {
	e := newBindingEnv(t)
	sub1 := e.create("/api/v1/subnets", `{"cidr":"10.91.3.0/24","client_id":"`+e.f.Client1+`"}`)["id"].(string)
	sub2 := e.create("/api/v1/subnets", `{"cidr":"10.91.4.0/24","client_id":"`+e.f.Client2+`"}`)["id"].(string)
	onNIC := e.create("/api/v1/ips", `{"address":"10.91.3.10","interface_id":"`+e.nicA+`","subnet_id":"`+sub1+`"}`)["id"].(string)
	vip := e.create("/api/v1/ips", `{"address":"10.91.3.11","ci_id":"`+e.ciA+`","role":"vip","subnet_id":"`+sub1+`"}`)["id"].(string)
	foreignSubnet := e.create("/api/v1/ips", `{"address":"10.91.4.10","interface_id":"`+e.nicA+`","subnet_id":"`+sub2+`"}`)["id"].(string)
	e.create("/api/v1/ips", `{"address":"10.91.3.12","interface_id":"`+e.nicB+`","subnet_id":"`+sub1+`"}`)

	w := e.api.call(http.MethodGet, "/api/v1/cis/"+e.ciA+"/addresses", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /cis/{id}/addresses = %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Data  []ipam.CIAddress `json:"data"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 || len(list.Data) != 3 {
		t.Fatalf("addresses of CI A: %+v", list)
	}
	byID := map[string]ipam.CIAddress{}
	for _, a := range list.Data {
		byID[a.ID] = a
	}
	if a := byID[onNIC]; a.InterfaceID != e.nicA || a.InterfaceName != "eth0" || a.SubnetID != sub1 || a.SubnetCIDR != "10.91.3.0/24" ||
		a.VRFID == "" || a.State == "" || a.Type != "static" || a.Role != "primary" {
		t.Errorf("address on eth0 = %+v", a)
	}
	if a := byID[vip]; a.InterfaceID != "" || a.InterfaceName != "" || a.Role != "vip" || a.SubnetCIDR != "10.91.3.0/24" {
		t.Errorf("VIP = %+v", a)
	}
	for _, path := range []string{"/api/v1/cis/" + e.ciOther + "/addresses", "/api/v1/cis/" + e.f.ID() + "/addresses",
		"/api/v1/cis/not-a-uuid/addresses"} {
		if got := e.api.call(http.MethodGet, path, ""); got.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, got.Code)
		}
	}
	client1 := newIfaceAPI(e.f, e.f.ClientCtx(e.f.Client1), e.f.OrgA)
	w = client1.call(http.MethodGet, "/api/v1/cis/"+e.ciA+"/addresses", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), foreignSubnet) || !strings.Contains(w.Body.String(), vip) {
		t.Errorf("client 1 = %d %s, want the addresses without the one in the subnet of client 2", w.Code, w.Body.String())
	}
	client2 := newIfaceAPI(e.f, e.f.ClientCtx(e.f.Client2), e.f.OrgA)
	if got := client2.call(http.MethodGet, "/api/v1/cis/"+e.ciA+"/addresses", ""); got.Code != http.StatusNotFound {
		t.Errorf("client 2 lists the addresses of CI A: %d", got.Code)
	}
}

// TestCIHardDeleteNullsAddressCI covers AK1: the hard deletion of the CI
// clears ci_id of its addresses (with and without interface); the address
// stays in its organization.
func TestCIHardDeleteNullsAddressCI(t *testing.T) {
	e := newBindingEnv(t)
	vip := e.create("/api/v1/ips", `{"address":"10.91.5.1","ci_id":"`+e.ciA+`","role":"vip"}`)["id"].(string)
	onNIC := e.create("/api/v1/ips", `{"address":"10.91.5.2","interface_id":"`+e.nicA+`"}`)["id"].(string)
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM ci WHERE id = $1`, e.ciA); err != nil {
		t.Fatalf("hard delete of the CI: %v", err)
	}
	for _, id := range []string{vip, onNIC} {
		if got := e.scalar(`SELECT COALESCE(ci_id::text, '') || '|' || organization_id::text FROM ip_address WHERE id = $1`, id); got != "|"+e.f.OrgA {
			t.Errorf("address %s after the deletion of its CI: %q", id, got)
		}
	}
}

// TestCIScopeChangePropagatesToBoundAddress covers AK3: a client change of
// the CI carries over to its addresses without interface.
func TestCIScopeChangePropagatesToBoundAddress(t *testing.T) {
	e := newBindingEnv(t)
	vip := e.create("/api/v1/ips", `{"address":"10.91.6.1","ci_id":"`+e.ciA+`","role":"anycast"}`)["id"].(string)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, e.ciA, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	if got := e.scalar(`SELECT COALESCE(client_id::text, '') FROM ip_address WHERE id = $1`, vip); got != e.f.Client2 {
		t.Errorf("client of the address after the CI moved = %q, want %s", got, e.f.Client2)
	}
}

// TestInterfaceMovedToOtherCIRebindsAddresses covers AK2 for a change of
// the interface: an interface that moves to another CI of the same client
// and site (CI merge, unmerge, a direct update) takes its addresses along,
// so ci_id stays the CI of the interface (network_interface_ci_rebind);
// an address without interface keeps its CI.
func TestInterfaceMovedToOtherCIRebindsAddresses(t *testing.T) {
	e := newBindingEnv(t)
	nic := e.create("/api/v1/cis/"+e.ciA+"/interfaces", `{"name":"eth7"}`)["id"].(string)
	addr := e.create("/api/v1/ips", `{"address":"10.91.7.1","interface_id":"`+nic+`"}`)["id"].(string)
	vip := e.create("/api/v1/ips", `{"address":"10.91.7.2","ci_id":"`+e.ciA+`","role":"vip"}`)["id"].(string)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE network_interface SET ci_id = $2 WHERE id = $1`, nic, e.ciB); err != nil {
		t.Fatal(err)
	}
	if got := e.scalar(`SELECT ci_id::text FROM ip_address WHERE id = $1`, addr); got != e.ciB {
		t.Errorf("ci_id of the address after its interface moved = %s, want %s", got, e.ciB)
	}
	if got := e.scalar(`SELECT ci_id::text FROM ip_address WHERE id = $1`, vip); got != e.ciA {
		t.Errorf("ci_id of the address without interface = %s, want %s", got, e.ciA)
	}
	w := e.api.call(http.MethodGet, "/api/v1/cis/"+e.ciB+"/addresses", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), addr) {
		t.Errorf("GET /cis/{B}/addresses = %d %s, want the moved address", w.Code, w.Body.String())
	}
}
