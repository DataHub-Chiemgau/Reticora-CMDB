package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAddressStateAndGatewayOnPostgreSQL covers IPM-04 (WP-902, E-211)
// against PostgreSQL:
//   - setting the gateway of a subnet creates the address (static, no
//     interface, is_gateway), changing it moves the mark, clearing it
//     removes the mark and keeps the address; a gateway on an existing
//     address of the VRF marks that address;
//   - a new prefix that keeps the gateway inside passes, one that leaves
//     addresses outside is 422 cidr;
//   - PATCH state writes exactly one ipam.address.state_changed into the
//     outbox with id, subnet, client, from and to; the same state again
//     writes none and keeps state_changed_at;
//   - the address lists filter by state and is_gateway.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAddressStateAndGatewayOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "ca")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(orgCtx)
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
	gateways := func(subnet string) string {
		t.Helper()
		return str(`SELECT COALESCE(string_agg(host(address), ',' ORDER BY address), '') FROM ip_address WHERE subnet_id = $1 AND is_gateway`, subnet)
	}

	w := call(http.MethodPost, "/api/v1/subnets", `{"cidr":"10.202.0.0/24","client_id":"`+f.Client1+`","gateway":"10.202.0.1"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /subnets: %d %s", w.Code, w.Body.String())
	}
	var subnet ipam.Subnet
	if err := json.Unmarshal(w.Body.Bytes(), &subnet); err != nil {
		t.Fatal(err)
	}
	if got := gateways(subnet.ID); got != "10.202.0.1" {
		t.Errorf("gateway addresses after POST = %q, want 10.202.0.1", got)
	}
	if got := str(`SELECT type || '/' || (interface_id IS NULL)::text || '/' || COALESCE(client_id::text, '') FROM ip_address
		WHERE subnet_id = $1 AND is_gateway`, subnet.ID); got != "static/true/"+f.Client1 {
		t.Errorf("gateway address = %s, want static without interface of the subnet's client", got)
	}

	// A new prefix that still holds the stored gateway passes (the gateway
	// is read as its address, not as the host prefix 10.202.0.1/32), and
	// the subnet reports the gateway as an address.
	for _, cidr := range []string{"10.202.0.0/23", "10.202.0.0/24"} {
		if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"cidr":"`+cidr+`"}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH cidr %s with the gateway inside: %d %s, want 200", cidr, w.Code, w.Body.String())
		}
		var got ipam.Subnet
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Gateway != "10.202.0.1" {
			t.Errorf("subnet after PATCH cidr %s: gateway %q (%v), want 10.202.0.1", cidr, got.Gateway, err)
		}
	}

	// Changing the gateway moves the mark; the old address stays.
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"gateway":"10.202.0.254"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH gateway: %d %s", w.Code, w.Body.String())
	}
	if got := gateways(subnet.ID); got != "10.202.0.254" {
		t.Errorf("gateway after the change = %q", got)
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1`, subnet.ID); n != "2" {
		t.Errorf("addresses after the change = %s, want 2", n)
	}
	// A gateway on an existing address marks it.
	w = call(http.MethodPost, "/api/v1/ips", `{"address":"10.202.0.10","subnet_id":"`+subnet.ID+`","type":"dhcp"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /ips: %d %s", w.Code, w.Body.String())
	}
	var existing ipam.IPAddress
	if err := json.Unmarshal(w.Body.Bytes(), &existing); err != nil {
		t.Fatal(err)
	}
	if existing.State != ipam.IPStateUnknown || existing.IsGateway {
		t.Errorf("new address state %q gateway %v, want unknown and no gateway", existing.State, existing.IsGateway)
	}
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"gateway":"10.202.0.10"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH gateway onto an address: %d %s", w.Code, w.Body.String())
	}
	if got := str(`SELECT id::text FROM ip_address WHERE subnet_id = $1 AND is_gateway`, subnet.ID); got != existing.ID {
		t.Errorf("marked address %s, want the existing one %s", got, existing.ID)
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1`, subnet.ID); n != "3" {
		t.Errorf("addresses after marking an existing one = %s, want 3", n)
	}
	// Clearing removes the mark, the address stays.
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"gateway":""}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH clear gateway: %d %s", w.Code, w.Body.String())
	}
	if got := gateways(subnet.ID); got != "" {
		t.Errorf("gateway after clearing = %q, want none", got)
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1`, subnet.ID); n != "3" {
		t.Errorf("addresses after clearing = %s, want 3", n)
	}
	// A new prefix that leaves addresses outside hands them to their
	// longest prefix, here none (IPM-14, WP-912); the old prefix takes
	// them back. A gateway outside is 422 gateway.
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"cidr":"10.202.0.128/25"}`); w.Code != http.StatusOK {
		t.Errorf("PATCH cidr leaving addresses outside: %d %s, want 200", w.Code, w.Body.String())
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1`, subnet.ID); n != "1" {
		t.Errorf("addresses inside the shrunk prefix = %s, want 1 (10.202.0.254)", n)
	}
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"cidr":"10.202.0.0/24"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH cidr back: %d %s", w.Code, w.Body.String())
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1`, subnet.ID); n != "3" {
		t.Errorf("addresses after the prefix is back = %s, want 3", n)
	}
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"gateway":"10.203.0.1"}`); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"gateway"`) {
		t.Errorf("PATCH gateway outside: %d %s, want 422 gateway", w.Code, w.Body.String())
	}

	// PATCH state: one event per change, none for the same state.
	outbox := func() string {
		t.Helper()
		return str(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3`,
			f.OrgA, events.IPAMAddressStateChanged, existing.ID)
	}
	if w = call(http.MethodPatch, "/api/v1/ips/"+existing.ID, `{"state":"active"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH state: %d %s", w.Code, w.Body.String())
	}
	var changed ipam.IPAddress
	if err := json.Unmarshal(w.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.State != ipam.IPStateActive || changed.StateChangedAt == nil {
		t.Errorf("after PATCH state: %+v", changed)
	}
	if n := outbox(); n != "1" {
		t.Errorf("events after a state change = %s, want 1", n)
	}
	var data events.AddressStateChangedData
	if err := json.Unmarshal([]byte(str(`SELECT data::text FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3`,
		f.OrgA, events.IPAMAddressStateChanged, existing.ID)), &data); err != nil {
		t.Fatal(err)
	}
	if data.ID != existing.ID || data.SubnetID != subnet.ID || data.ClientID != f.Client1 || data.From != "unknown" || data.To != "active" {
		t.Errorf("event data = %+v", data)
	}
	if w = call(http.MethodPatch, "/api/v1/ips/"+existing.ID, `{"state":"active","description":"same"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH same state: %d %s", w.Code, w.Body.String())
	}
	var same ipam.IPAddress
	if err := json.Unmarshal(w.Body.Bytes(), &same); err != nil {
		t.Fatal(err)
	}
	if n := outbox(); n != "1" {
		t.Errorf("events after the same state = %s, want still 1", n)
	}
	if same.StateChangedAt == nil || !same.StateChangedAt.Equal(*changed.StateChangedAt) {
		t.Errorf("state_changed_at moved without a change: %v → %v", changed.StateChangedAt, same.StateChangedAt)
	}
	if w = call(http.MethodPatch, "/api/v1/ips/"+existing.ID, `{"state":"gone"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("PATCH invalid state: %d, want 422", w.Code)
	}

	// Filters state and is_gateway, on both lists.
	if w = call(http.MethodPatch, "/api/v1/subnets/"+subnet.ID, `{"gateway":"10.202.0.1"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH gateway back: %d %s", w.Code, w.Body.String())
	}
	count := func(path string) (n int, ids []string) {
		t.Helper()
		w := call(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var list struct {
			Data []ipam.IPAddress `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		for _, a := range list.Data {
			ids = append(ids, a.ID)
		}
		return len(list.Data), ids
	}
	if n, ids := count("/api/v1/subnets/" + subnet.ID + "/addresses?state=active"); n != 1 || ids[0] != existing.ID {
		t.Errorf("?state=active = %v, want the changed address", ids)
	}
	if n, _ := count("/api/v1/subnets/" + subnet.ID + "/addresses?state=unknown"); n != 2 {
		t.Errorf("?state=unknown = %d, want 2", n)
	}
	if n, _ := count("/api/v1/ips?subnet_id=" + subnet.ID + "&is_gateway=true"); n != 1 {
		t.Errorf("?is_gateway=true = %d, want 1", n)
	}
	if n, _ := count("/api/v1/ips?subnet_id=" + subnet.ID + "&is_gateway=false&state=unknown"); n != 1 {
		t.Errorf("?is_gateway=false&state=unknown = %d, want 1", n)
	}
}

// TestGatewayMarkFollowsSubnetOnPostgreSQL covers the consistency of the
// gateway mark of IPM-04 (WP-902) against PostgreSQL:
//   - the gateway address cannot leave its subnet (PATCH subnet_id to
//     another subnet or none: 422 subnet_id) or be deleted (409
//     is_gateway);
//   - deleting the subnet takes the mark with the subnet (ON DELETE SET
//     NULL and trg_ip_address_gateway_leaves_subnet): no address without
//     subnet carries is_gateway;
//   - a gateway outside the prefix after an update is refused in the
//     database (subnet_gateway_in_cidr, 422 gateway), also when the
//     handler's check is bypassed, e.g. by a concurrent PATCH;
//   - a client principal may set the gateway of its subnet although the
//     current gateway address belongs to another client (interface of a
//     client-2 CI): the mark moves, no duplicate address arises.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestGatewayMarkFollowsSubnetOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "ce")
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
	create := func(body string) ipam.Subnet {
		t.Helper()
		w := call(orgCtx, http.MethodPost, "/api/v1/subnets", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /subnets %s: %d %s", body, w.Code, w.Body.String())
		}
		var sn ipam.Subnet
		if err := json.Unmarshal(w.Body.Bytes(), &sn); err != nil {
			t.Fatal(err)
		}
		return sn
	}

	// The gateway address keeps its subnet and stays.
	a := create(`{"cidr":"10.206.0.0/24","client_id":"` + f.Client1 + `","gateway":"10.206.0.1"}`)
	b := create(`{"cidr":"10.206.0.0/25","client_id":"` + f.Client1 + `"}`)
	gw := str(`SELECT id::text FROM ip_address WHERE subnet_id = $1 AND is_gateway`, a.ID)
	for _, body := range []string{`{"subnet_id":"` + b.ID + `"}`, `{"subnet_id":""}`} {
		if w := call(orgCtx, http.MethodPatch, "/api/v1/ips/"+gw, body); w.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(w.Body.String(), `"subnet_id"`) {
			t.Errorf("PATCH gateway address %s: %d %s, want 422 subnet_id", body, w.Code, w.Body.String())
		}
	}
	if w := call(orgCtx, http.MethodDelete, "/api/v1/ips/"+gw, ""); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"is_gateway"`) {
		t.Errorf("DELETE gateway address: %d %s, want 409 is_gateway", w.Code, w.Body.String())
	}
	if got := str(`SELECT subnet_id::text || '/' || is_gateway::text FROM ip_address WHERE id = $1`, gw); got != a.ID+"/true" {
		t.Errorf("gateway address after the refusals = %s", got)
	}
	// Deleting the subnet takes the mark along.
	if w := call(orgCtx, http.MethodDelete, "/api/v1/subnets/"+a.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE subnet: %d %s", w.Code, w.Body.String())
	}
	// The address goes to its longest prefix without the subnet, the /25
	// (IPM-14, WP-912), without the mark.
	if got := str(`SELECT COALESCE(subnet_id::text, 'none') || '/' || is_gateway::text FROM ip_address WHERE id = $1`, gw); got != b.ID+"/false" {
		t.Errorf("former gateway address after deleting its subnet = %s, want %s/false", got, b.ID)
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE organization_id = $1 AND is_gateway AND subnet_id IS NULL`, f.OrgA); n != "0" {
		t.Errorf("gateway addresses without subnet = %s, want 0", n)
	}

	// The database refuses a gateway outside the prefix after an update
	// (here a subnet whose gateway has no address row, so only that rule
	// applies), whatever the handler checked before.
	c := str(`INSERT INTO subnet (organization_id, cidr, gateway) VALUES ($1, '10.207.0.0/24', '10.207.0.1') RETURNING id::text`, f.OrgA)
	narrow := "10.207.0.128/25"
	_, err := repo.UpdateSubnet(orgCtx, f.OrgA, c, &ipam.UpdateSubnetRequest{CIDR: &narrow})
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "gateway" || fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("prefix leaving the gateway outside: %v, want 422 gateway", err)
	}

	// A gateway address of another client: the client principal of the
	// subnet moves the mark all the same.
	d := create(`{"cidr":"10.208.0.0/24","client_id":"` + f.Client1 + `","gateway":"10.208.0.1"}`)
	ci2 := f.CI(t, f.OrgA, f.Client2, "cg-foreign-ci")
	nic := str(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`, f.OrgA, ci2)
	foreign := str(`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address) VALUES ($1, $2, $3, '10.208.0.20')
		RETURNING id::text`, f.OrgA, d.ID, nic)
	if w := call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+d.ID, `{"gateway":"10.208.0.20"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH gateway onto the foreign address: %d %s", w.Code, w.Body.String())
	}
	marked := func() string {
		t.Helper()
		return str(`SELECT COALESCE(string_agg(id::text, ',' ORDER BY id), '') FROM ip_address WHERE subnet_id = $1 AND is_gateway`, d.ID)
	}
	if got := marked(); got != foreign {
		t.Fatalf("marked address = %s, want the foreign one %s", got, foreign)
	}
	if w := call(client1Ctx, http.MethodPatch, "/api/v1/subnets/"+d.ID, `{"gateway":"10.208.0.20"}`); w.Code != http.StatusOK {
		t.Errorf("client 1 PATCH of the same gateway: %d %s, want 200", w.Code, w.Body.String())
	}
	if got := marked(); got != foreign {
		t.Errorf("marked address after the same gateway = %s, want still %s", got, foreign)
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1 AND address_host = '10.208.0.20'`, d.ID); n != "1" {
		t.Errorf("addresses 10.208.0.20 = %s, want 1 (no duplicate)", n)
	}
	if w := call(client1Ctx, http.MethodPatch, "/api/v1/subnets/"+d.ID, `{"gateway":"10.208.0.30"}`); w.Code != http.StatusOK {
		t.Fatalf("client 1 PATCH of a new gateway: %d %s, want 200", w.Code, w.Body.String())
	}
	if got := str(`SELECT host(address) FROM ip_address WHERE subnet_id = $1 AND is_gateway`, d.ID); got != "10.208.0.30" {
		t.Errorf("gateway after the client's change = %s, want 10.208.0.30", got)
	}
	if got := str(`SELECT is_gateway::text FROM ip_address WHERE id = $1`, foreign); got != "false" {
		t.Errorf("foreign address still marked: %s", got)
	}
}

// TestMigration351BackfillsAddressState runs the state backfill of
// migration 000299 inside a rolled-back transaction: an address seen
// yesterday becomes active with state_changed_at = last_seen_at, one seen
// 30 days ago stays unknown; updated_at stays.
func TestMigration351BackfillsAddressState(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "ca")
	bg := context.Background()
	raw, err := os.ReadFile("../../migrations/000299_ip_address_state.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "ALTER TABLE ip_address DISABLE TRIGGER trg_ip_address_updated_at;")
	end := strings.Index(text, "CREATE UNIQUE INDEX ip_address_one_gateway")
	if start < 0 || end < start {
		t.Fatal("backfill of 000299 not found")
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	var recent, old string
	if err = tx.QueryRow(bg, `INSERT INTO ip_address (organization_id, address, last_seen_at, updated_at)
		VALUES ($1, '10.204.0.1', now() - interval '1 day', '2020-01-01Z') RETURNING id::text`, f.OrgA).Scan(&recent); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(bg, `INSERT INTO ip_address (organization_id, address, last_seen_at, updated_at)
		VALUES ($1, '10.204.0.2', now() - interval '30 days', '2020-01-01Z') RETURNING id::text`, f.OrgA).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, text[start:end]); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for id, want := range map[string]string{recent: "active/true", old: "unknown/false"} {
		var got string
		if err = tx.QueryRow(bg, `SELECT state || '/' || (state_changed_at IS NOT DISTINCT FROM last_seen_at AND state_changed_at IS NOT NULL)::text
			FROM ip_address WHERE id = $1 AND updated_at = '2020-01-01Z'`, id).Scan(&got); err != nil {
			t.Fatalf("read %s (updated_at touched?): %v", id, err)
		}
		if got != want {
			t.Errorf("address %s after the backfill = %s, want %s", id, got, want)
		}
	}
}

// TestMigration351BackfillsGatewayMark runs the gateway backfill of
// migration 000299 inside a rolled-back transaction: a subnet whose gateway
// has no address gets a static address without interface with is_gateway,
// a subnet whose gateway address exists without subnet gets that address
// marked and moved into it (updated_at stays); a second run changes
// nothing.
func TestMigration351BackfillsGatewayMark(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "cb")
	bg := context.Background()
	raw, err := os.ReadFile("../../migrations/000299_ip_address_state.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "-- Backfill of the gateway mark")
	if start < 0 {
		t.Fatal("gateway backfill of 000299 not found")
	}
	backfill := text[start:]
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if qerr := tx.QueryRow(bg, q, args...).Scan(&out); qerr != nil {
			t.Fatalf("%s: %v", q, qerr)
		}
		return out
	}
	bare := str(`INSERT INTO subnet (organization_id, cidr, gateway) VALUES ($1, '10.209.0.0/24', '10.209.0.1') RETURNING id::text`, f.OrgA)
	withAddr := str(`INSERT INTO subnet (organization_id, cidr, gateway) VALUES ($1, '10.209.1.0/24', '10.209.1.1') RETURNING id::text`, f.OrgA)
	loose := str(`INSERT INTO ip_address (organization_id, address, updated_at) VALUES ($1, '10.209.1.1', '2020-01-01Z') RETURNING id::text`, f.OrgA)
	for run := 1; run <= 2; run++ {
		if _, err = tx.Exec(bg, backfill); err != nil {
			t.Fatalf("backfill run %d: %v", run, err)
		}
		if got := str(`SELECT string_agg(type || '/' || (interface_id IS NULL)::text || '/' || host(address), ',') FROM ip_address
			WHERE subnet_id = $1 AND is_gateway`, bare); got != "static/true/10.209.0.1" {
			t.Errorf("run %d: gateway address of the bare subnet = %s, want static/true/10.209.0.1", run, got)
		}
		if got := str(`SELECT string_agg(id::text, ',') FROM ip_address WHERE subnet_id = $1 AND is_gateway`, withAddr); got != loose {
			t.Errorf("run %d: gateway address of the subnet = %s, want the existing %s", run, got, loose)
		}
		if got := str(`SELECT (updated_at = '2020-01-01Z')::text FROM ip_address WHERE id = $1`, loose); got != "true" {
			t.Errorf("run %d: updated_at of the marked address touched", run)
		}
		if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id IN ($1, $2)`, bare, withAddr); n != "2" {
			t.Errorf("run %d: addresses of both subnets = %s, want 2", run, n)
		}
	}
}
