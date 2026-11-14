package ipam_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
)

// autoEnv is the scenario of the automatic subnet of addresses (IPM-14,
// WP-912): an org-wide principal of organization A.
//
// The tests run only against PostgreSQL (TEST_DATABASE_URL, see
// scopetest.Seed).
type autoEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	api ifaceAPI
}

func newAutoEnv(t *testing.T) *autoEnv {
	t.Helper()
	f := scopetest.Seed(t, "9e")
	return &autoEnv{t: t, f: f, api: newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA)}
}

// post creates through the API and returns the answer, failing unless 201.
func (e *autoEnv) post(path, body string) map[string]any {
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

func (e *autoEnv) id(path, body string) string {
	e.t.Helper()
	return e.post(path, body)["id"].(string)
}

// subnetOf reads the subnet of an address as stored.
func (e *autoEnv) subnetOf(address string) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(subnet_id::text, '') FROM ip_address WHERE id = $1`,
		address).Scan(&out); err != nil {
		e.t.Fatalf("subnet of %s: %v", address, err)
	}
	return out
}

func (e *autoEnv) wantSubnet(what, address, want string) {
	e.t.Helper()
	if got := e.subnetOf(address); got != want {
		e.t.Errorf("%s: subnet %q, want %q", what, got, want)
	}
}

// TestAddressGetsLongestPrefix covers AK1 and AK2: 10.1.1.5 written without
// subnet lands in the /24, not in the /16 and not in a narrower pool; the
// answer carries the subnet.
func TestAddressGetsLongestPrefix(t *testing.T) {
	e := newAutoEnv(t)
	e.id("/api/v1/subnets", `{"cidr":"10.1.0.0/16"}`)
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.1.1.0/24"}`)
	e.id("/api/v1/subnets", `{"cidr":"10.1.1.0/28","is_pool":true}`)
	a := e.post("/api/v1/ips", `{"address":"10.1.1.5"}`)
	if a["subnet_id"] != s24 {
		t.Errorf("answer subnet_id = %v, want the /24 %s", a["subnet_id"], s24)
	}
	e.wantSubnet("10.1.1.5", a["id"].(string), s24)
	// Outside every subnet: none.
	e.wantSubnet("10.2.0.1", e.id("/api/v1/ips", `{"address":"10.2.0.1"}`), "")
}

// TestNewSubnetTakesAddresses covers AK4: a new /24 takes the addresses of
// the /16 inside it and those without subnet; the others stay. A subnet
// allocated from a pool (allocate-subnet, WP-904) takes the addresses
// without subnet in its prefix as well.
func TestNewSubnetTakesAddresses(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.3.0.0/16"}`)
	inside := e.id("/api/v1/ips", `{"address":"10.3.1.5"}`)
	explicit := e.id("/api/v1/ips", `{"address":"10.3.1.6","subnet_id":"`+s16+`"}`)
	outside := e.id("/api/v1/ips", `{"address":"10.3.2.5"}`)
	e.wantSubnet("before", inside, s16)
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.3.1.0/24"}`)
	e.wantSubnet("address of the /16 inside the new /24", inside, s24)
	e.wantSubnet("address named into the /16 inside the new /24", explicit, s24)
	e.wantSubnet("address of the /16 outside the new /24", outside, s16)

	// allocate-subnet from a pool: the pool holds no addresses, so an
	// address in its prefix has none until the allocated subnet takes it.
	pool := e.id("/api/v1/subnets", `{"cidr":"10.4.0.0/16","is_pool":true}`)
	loose := e.id("/api/v1/ips", `{"address":"10.4.0.9"}`)
	e.wantSubnet("address in a pool", loose, "")
	w := e.api.call(http.MethodPost, "/api/v1/subnets/"+pool+"/allocate-subnet", `{"prefix_length":24}`)
	var allocated ipam.Subnet
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &allocated) != nil {
		t.Fatalf("allocate-subnet = %d %s", w.Code, w.Body.String())
	}
	if allocated.CIDR != "10.4.0.0/24" {
		t.Fatalf("allocated %s, want the first /24 of the pool", allocated.CIDR)
	}
	e.wantSubnet("address in the allocated subnet", loose, allocated.ID)
}

// TestDeletedSubnetHandsAddressesToParent covers AK4: after the deletion
// of the /24 its addresses lie in the /16, after the deletion of the /16
// in none.
func TestDeletedSubnetHandsAddressesToParent(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.5.0.0/16"}`)
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.5.1.0/24"}`)
	addr := e.id("/api/v1/ips", `{"address":"10.5.1.5"}`)
	e.wantSubnet("before", addr, s24)
	for _, step := range []struct{ subnet, want string }{{s24, s16}, {s16, ""}} {
		if w := e.api.call(http.MethodDelete, "/api/v1/subnets/"+step.subnet, ""); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE subnet = %d %s", w.Code, w.Body.String())
		}
		e.wantSubnet("after the deletion", addr, step.want)
	}
}

// TestVRFBoundary covers AK1 to AK4: an address of VRF B never lands in a
// subnet of VRF A, neither on write nor when a subnet of VRF A is created.
func TestVRFBoundary(t *testing.T) {
	e := newAutoEnv(t)
	vrfA := e.id("/api/v1/vrfs", `{"name":"wp912-a"}`)
	vrfB := e.id("/api/v1/vrfs", `{"name":"wp912-b"}`)
	inA := e.id("/api/v1/subnets", `{"cidr":"10.6.0.0/24","vrf_id":"`+vrfA+`"}`)
	addrB := e.post("/api/v1/ips", `{"address":"10.6.0.5","vrf_id":"`+vrfB+`"}`)
	if addrB["vrf_id"] != vrfB || addrB["subnet_id"] != nil {
		t.Errorf("address of VRF B = %v, want VRF B without subnet", addrB)
	}
	addrA := e.id("/api/v1/ips", `{"address":"10.6.0.6","vrf_id":"`+vrfA+`"}`)
	e.wantSubnet("address of VRF A", addrA, inA)
	e.id("/api/v1/subnets", `{"cidr":"10.6.0.0/25","vrf_id":"`+vrfA+`"}`)
	e.wantSubnet("address of VRF B after a subnet of VRF A", addrB["id"].(string), "")
	inB := e.id("/api/v1/subnets", `{"cidr":"10.6.0.0/24","vrf_id":"`+vrfB+`"}`)
	e.wantSubnet("address of VRF B after a subnet of VRF B", addrB["id"].(string), inB)
}

// TestExplicitSubnetWins covers AK2: a named subnet is kept although a
// narrower one contains the address; an address outside the named subnet
// stays 400.
func TestExplicitSubnetWins(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.7.0.0/16"}`)
	e.id("/api/v1/subnets", `{"cidr":"10.7.1.0/24"}`)
	e.wantSubnet("named /16", e.id("/api/v1/ips", `{"address":"10.7.1.5","subnet_id":"`+s16+`"}`), s16)
	if w := e.api.call(http.MethodPost, "/api/v1/ips", `{"address":"10.8.0.1","subnet_id":"`+s16+`"}`); w.Code != http.StatusBadRequest {
		t.Errorf("address outside the named subnet = %d %s, want 400", w.Code, w.Body.String())
	}
}

// TestDiscoveredAddressIsAssigned covers AK6: the discovery sync gives a
// new address its longest prefix, and an address it observes again
// without subnet gets one.
func TestDiscoveredAddressIsAssigned(t *testing.T) {
	e := newAutoEnv(t)
	ctx := e.f.OrgCtx(e.f.OrgA)
	ciID := e.f.CI(t, e.f.OrgA, "", "wp912-sync")
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.9.1.0/24"}`)
	repo := ipam.NewPGRepository(e.f.App)
	sync := func() {
		t.Helper()
		if _, err := repo.SyncDiscoveredInterfaces(ctx, e.f.OrgA, ciID,
			[]ipam.ObservedInterface{{Name: "eth0", IPs: []string{"10.9.1.7", "10.9.2.7"}}}); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	read := func(host string) string {
		t.Helper()
		var id string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ip_address
			WHERE organization_id = $1 AND host(address) = $2`, e.f.OrgA, host).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.wantSubnet("discovered 10.9.1.7", read("10.9.1.7"), s24)
	// A subnet that appears without being created through the write path
	// (no takeover) is found when the address is observed again.
	other := read("10.9.2.7")
	e.wantSubnet("discovered 10.9.2.7", other, "")
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `ALTER TABLE subnet DISABLE TRIGGER trg_subnet_place_insert`); err != nil {
		t.Fatal(err)
	}
	var s2 string
	err := e.f.Admin.QueryRow(bg, `INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.9.2.0/24') RETURNING id::text`,
		e.f.OrgA).Scan(&s2)
	if _, enableErr := e.f.Admin.Exec(bg, `ALTER TABLE subnet ENABLE TRIGGER trg_subnet_place_insert`); enableErr != nil {
		t.Fatal(enableErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	e.wantSubnet("before the second observation", other, "")
	sync()
	e.wantSubnet("observed again", other, s2)
}

// TestReassignmentWritesHistory covers AK5: every reassignment writes one
// ipam_change row per address with source allocation, field subnet_id and
// the old and the new subnet.
func TestReassignmentWritesHistory(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.10.0.0/16"}`)
	a1 := e.id("/api/v1/ips", `{"address":"10.10.1.1"}`)
	a2 := e.id("/api/v1/ips", `{"address":"10.10.1.2"}`)
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.10.1.0/24"}`)
	history := func(address, from, to string) int {
		t.Helper()
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ipam_change
			WHERE object_kind = 'ip_address' AND object_id = $1 AND change_type = 'update' AND field_name = 'subnet_id'
			  AND source = 'allocation' AND COALESCE(old_value #>> '{}', '') = $2 AND COALESCE(new_value #>> '{}', '') = $3`,
			address, from, to).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, a := range []string{a1, a2} {
		if n := history(a, s16, s24); n != 1 {
			t.Errorf("history of the takeover of %s: %d rows, want 1", a, n)
		}
	}
	if w := e.api.call(http.MethodDelete, "/api/v1/subnets/"+s24, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", w.Code)
	}
	for _, a := range []string{a1, a2} {
		if n := history(a, s24, s16); n != 1 {
			t.Errorf("history of the release of %s: %d rows, want 1", a, n)
		}
	}
	// The writes of the caller stay manual.
	var manual int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ipam_change
		WHERE object_id = $1 AND change_type = 'create' AND source = 'manual'`, a1).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual != 1 {
		t.Errorf("create of the address: %d manual rows, want 1", manual)
	}
}

// TestScopedWriterOutOfScopeSubnet covers AK2: a principal of client 1
// whose address would land in the subnet of client 2 gets 404, as for a
// named foreign subnet, and nothing is written.
func TestScopedWriterOutOfScopeSubnet(t *testing.T) {
	e := newAutoEnv(t)
	e.id("/api/v1/subnets", `{"cidr":"10.11.0.0/24","client_id":"`+e.f.Client2+`"}`)
	client1 := newIfaceAPI(e.f, e.f.ClientCtx(e.f.Client1), e.f.OrgA)
	if w := client1.call(http.MethodPost, "/api/v1/ips", `{"address":"10.11.0.5"}`); w.Code != http.StatusNotFound {
		t.Errorf("client 1 into the subnet of client 2 = %d %s, want 404", w.Code, w.Body.String())
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ip_address WHERE organization_id = $1 AND host(address) = '10.11.0.5'`,
		e.f.OrgA).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d addresses written", n)
	}
	// Outside every subnet nothing changes: an org-wide address is beyond a
	// client principal (403 as before), one in its own client's subnet is
	// written.
	if w := client1.call(http.MethodPost, "/api/v1/ips", `{"address":"10.12.0.5"}`); w.Code != http.StatusForbidden {
		t.Errorf("client 1 outside every subnet = %d %s, want 403", w.Code, w.Body.String())
	}
	own := e.id("/api/v1/subnets", `{"cidr":"10.13.0.0/24","client_id":"`+e.f.Client1+`"}`)
	w := client1.call(http.MethodPost, "/api/v1/ips", `{"address":"10.13.0.5"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("client 1 into its own subnet = %d %s", w.Code, w.Body.String())
	}
	var created ipam.IPAddress
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.SubnetID != own {
		t.Errorf("client 1 address = %+v, want subnet %s", created, own)
	}
}

// changes counts the allocation rows of ipam_change that moved address
// from subnet from to subnet to ("" for none).
func (e *autoEnv) changes(address, from, to string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ipam_change
		WHERE object_kind = 'ip_address' AND object_id = $1 AND change_type = 'update' AND field_name = 'subnet_id'
		  AND source = 'allocation' AND COALESCE(old_value #>> '{}', '') = $2 AND COALESCE(new_value #>> '{}', '') = $3`,
		address, from, to).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *autoEnv) patch(path, body string) {
	e.t.Helper()
	if w := e.api.call(http.MethodPatch, path, body); w.Code != http.StatusOK {
		e.t.Fatalf("PATCH %s %s = %d %s", path, body, w.Code, w.Body.String())
	}
}

// TestChangedSubnetReassigns covers AK4 and AK5 for a changed subnet: a
// widened prefix takes the addresses of its parent inside it, a shrunk one
// hands those now outside to their longest prefix (the parent, else none),
// each recorded as allocation; a subnet moved into another VRF takes the
// loose addresses of that VRF inside it and none of the old VRF.
func TestChangedSubnetReassigns(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.11.0.0/16"}`)
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.11.0.0/24"}`)
	near := e.id("/api/v1/ips", `{"address":"10.11.1.5"}`)
	e.wantSubnet("address outside the /24", near, s16)

	e.patch("/api/v1/subnets/"+s24, `{"cidr":"10.11.0.0/23"}`)
	e.wantSubnet("address inside the widened /23", near, s24)
	if n := e.changes(near, s16, s24); n != 1 {
		t.Errorf("history of the takeover by the widened prefix: %d rows, want 1", n)
	}

	e.patch("/api/v1/subnets/"+s24, `{"cidr":"10.11.0.0/24"}`)
	e.wantSubnet("address outside the shrunk /24", near, s16)
	if n := e.changes(near, s24, s16); n != 1 {
		t.Errorf("history of the release by the shrunk prefix: %d rows, want 1", n)
	}
	// Without a subnet around it the address has none afterwards.
	lone := e.id("/api/v1/subnets", `{"cidr":"10.12.0.0/23"}`)
	edge := e.id("/api/v1/ips", `{"address":"10.12.1.7"}`)
	e.wantSubnet("address of the lone /23", edge, lone)
	e.patch("/api/v1/subnets/"+lone, `{"cidr":"10.12.0.0/24"}`)
	e.wantSubnet("address outside the shrunk lone subnet", edge, "")

	// Into another VRF: the loose address of VRF B is taken, the address
	// of the default VRF is not.
	vrfB := e.id("/api/v1/vrfs", `{"name":"wp912-move"}`)
	inB := e.id("/api/v1/ips", `{"address":"10.13.0.5","vrf_id":"`+vrfB+`"}`)
	inDefault := e.id("/api/v1/ips", `{"address":"10.13.0.6"}`)
	moved := e.id("/api/v1/subnets", `{"cidr":"10.13.0.0/24"}`)
	e.wantSubnet("default VRF address before the move", inDefault, moved)
	other := e.id("/api/v1/ips", `{"address":"10.13.0.7"}`)
	e.patch("/api/v1/subnets/"+moved, `{"vrf_id":"`+vrfB+`"}`)
	e.wantSubnet("address of VRF B after the move", inB, moved)
	if n := e.changes(inB, "", moved); n != 1 {
		t.Errorf("history of the takeover by the moved subnet: %d rows, want 1", n)
	}
	var vrf string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT vrf_id::text FROM ip_address WHERE id = $1`, other).Scan(&vrf); err != nil {
		t.Fatal(err)
	}
	if vrf != vrfB {
		t.Errorf("own address of the moved subnet in VRF %s, want it to follow into %s", vrf, vrfB)
	}
}

// TestTakeoverUnderPool covers AK4 with a pool between: 10.20.0.0/16
// (subnet) > pool 10.20.4.0/22; an address in the pool's range lies in the
// /16, its longest prefix. A /24 allocated from the pool takes it, as the
// narrowest subnet without pool around the /24 is the /16.
func TestTakeoverUnderPool(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.20.0.0/16"}`)
	pool := e.id("/api/v1/subnets", `{"cidr":"10.20.4.0/22","is_pool":true}`)
	addr := e.id("/api/v1/ips", `{"address":"10.20.4.9"}`)
	e.wantSubnet("address under the pool", addr, s16)
	w := e.api.call(http.MethodPost, "/api/v1/subnets/"+pool+"/allocate-subnet", `{"prefix_length":24}`)
	var allocated ipam.Subnet
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &allocated) != nil {
		t.Fatalf("allocate-subnet = %d %s", w.Code, w.Body.String())
	}
	if allocated.CIDR != "10.20.4.0/24" {
		t.Fatalf("allocated %s, want the first /24 of the pool", allocated.CIDR)
	}
	e.wantSubnet("address in the allocated /24", addr, allocated.ID)
}

// TestSectionChangeKeepsAddresses covers AK4: a subnet that only moves
// into another section reassigns nothing; an address named into the
// parent keeps it and no allocation row is written.
func TestSectionChangeKeepsAddresses(t *testing.T) {
	e := newAutoEnv(t)
	s16 := e.id("/api/v1/subnets", `{"cidr":"10.21.0.0/16"}`)
	s24 := e.id("/api/v1/subnets", `{"cidr":"10.21.1.0/24"}`)
	named := e.id("/api/v1/ips", `{"address":"10.21.1.5","subnet_id":"`+s16+`"}`)
	section := e.id("/api/v1/ipam-sections", `{"name":"wp912-section"}`)
	e.patch("/api/v1/subnets/"+s24, `{"section_id":"`+section+`"}`)
	e.wantSubnet("address named into the /16", named, s16)
	if n := e.changes(named, s16, s24); n != 0 {
		t.Errorf("section change wrote %d allocation rows, want 0", n)
	}
}
