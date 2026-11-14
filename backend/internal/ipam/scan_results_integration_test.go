package ipam_test

// WP-1063 (IPM-11, E-211): the system job ipam.scan.apply applies the
// states of the scan probes to the addresses against PostgreSQL as
// reticora_app. The probe states are written directly into probe_state.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -p 1 -run 'TestScan|TestCollectorUnreachableYieldsUnknownNotOffline' ./internal/ipam/

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// applyEnv is a scanEnv with the job on a repository with the outbox.
type applyEnv struct {
	*scanEnv
	applier *ipam.ScanApplier
}

func newApplyEnv(t *testing.T) *applyEnv {
	t.Helper()
	e := newScanEnv(t)
	repo := ipam.NewPGRepository(e.f.App).WithEvents(events.NewOutbox())
	repo.SetScanProbes(probe.ScanProbes{Repo: e.repo})
	return &applyEnv{scanEnv: e, applier: ipam.NewScanApplier(e.f.App, repo)}
}

// scanned creates a subnet of client 1 with the addresses and switches the
// scan on with group g1 (and the extra fields of body).
func (e *applyEnv) scanned(cidr, extra string, addrs ...string) string {
	e.t.Helper()
	id := e.subnet(cidr, e.f.Client1, "", addrs...)
	e.mustPatch(id, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"`+extra+`}`)
	return id
}

// probeOf is the probe of the target address of subnet.
func (e *applyEnv) probeOf(subnet, address string) string {
	e.t.Helper()
	return e.scalar(`SELECT probe_id::text FROM subnet_scan_target WHERE subnet_id = $1 AND address = $2::inet`, subnet, address)
}

// state sets the probe state of the target; since and seen are SQL
// intervals before the database's now() (the database clock, not the
// host's).
func (e *applyEnv) state(subnet, address, state, reason, since, seen string) {
	e.t.Helper()
	p := e.probeOf(subnet, address)
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO probe_state (probe_id, organization_id, state, reason, since, last_result_at)
		VALUES ($1, $2, $3, $4, now() - $5::interval, now() - $6::interval)
		ON CONFLICT (probe_id) DO UPDATE SET state = EXCLUDED.state, reason = EXCLUDED.reason, since = EXCLUDED.since,
		    last_result_at = EXCLUDED.last_result_at`, p, e.f.OrgA, state, reason, since, seen); err != nil {
		e.t.Fatalf("probe state: %v", err)
	}
}

func (e *applyEnv) run() ipam.ScanApplyResult {
	e.t.Helper()
	res, err := e.applier.RunOrg(context.Background(), e.f.OrgA)
	if err != nil {
		e.t.Fatalf("apply: %v", err)
	}
	return res
}

// address is the state and the age of last_seen_at in seconds (-1 without)
// of the address.
func (e *applyEnv) address(address string) (string, int) {
	e.t.Helper()
	var state string
	var age int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(state, ''),
			COALESCE(extract(epoch FROM now() - last_seen_at)::int, -1)
		  FROM ip_address WHERE organization_id = $1 AND address_host = $2`, e.f.OrgA, address).Scan(&state, &age); err != nil {
		e.t.Fatalf("address %s: %v", address, err)
	}
	return state, age
}

func (e *applyEnv) events(address string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM event_outbox o JOIN ip_address a ON a.id::text = o.entity_id
		WHERE o.organization_id = $1 AND o.type = $2 AND a.address_host = $3`, e.f.OrgA, events.IPAMAddressStateChanged, address)
}

func (e *applyEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// TestScanUpSetsActiveAndLastSeen: up and degraded set active and
// last_seen_at to the last result; an older result keeps a newer
// last_seen_at (greatest).
func TestScanUpSetsActiveAndLastSeen(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.1.0/24", "", "10.63.1.1", "10.63.1.2")
	e.state(net, "10.63.1.1", "up", "", "5 minutes", "20 seconds")
	e.state(net, "10.63.1.2", "degraded", "loss", "5 minutes", "40 seconds")
	res := e.run()
	if res.Skipped || !res.Scanned || res.Changed["active"] != 2 {
		t.Fatalf("result %+v, want two addresses active", res)
	}
	for addr, wantAge := range map[string]int{"10.63.1.1": 20, "10.63.1.2": 40} {
		state, age := e.address(addr)
		if state != "active" || age < wantAge-2 || age > wantAge+5 {
			t.Errorf("%s: state %q, last_seen_at %d s ago; want active, %d s", addr, state, age, wantAge)
		}
	}
	// An older result does not move last_seen_at back.
	e.state(net, "10.63.1.1", "up", "", "5 minutes", "10 minutes")
	e.run()
	if _, age := e.address("10.63.1.1"); age > 30 {
		t.Errorf("last_seen_at moved back to %d s ago", age)
	}
	if n := e.count(`SELECT count(*) FROM subnet WHERE id = $1 AND scan_applied_at IS NOT NULL`, net); n != 1 {
		t.Errorf("scan_applied_at not set")
	}
	code, scan := e.call(http.MethodGet, "/api/v1/subnets/"+net+"/scan", "")
	states, _ := scan["target_states"].(map[string]any)
	if code != http.StatusOK || scan["last_applied_at"] == nil || num(states["active"]) != 2 || num(scan["new_hosts"]) != 0 {
		t.Errorf("GET scan = %d %v, want last_applied_at and two active targets", code, scan)
	}
}

// TestScanDownBecomesOfflineAfterGrace: down keeps the state within
// offline_after_s since the last answer, afterwards offline.
func TestScanDownBecomesOfflineAfterGrace(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.2.0/24", `,"offline_after_s":600`, "10.63.2.1")
	e.exec(`UPDATE ip_address SET state = 'active', last_seen_at = now() - interval '5 minutes' WHERE subnet_id = $1`, net)
	e.state(net, "10.63.2.1", "down", "timeout", "4 minutes", "10 seconds")
	e.run()
	if state, _ := e.address("10.63.2.1"); state != "active" {
		t.Errorf("within the grace: state %q, want active", state)
	}
	e.exec(`UPDATE ip_address SET last_seen_at = now() - interval '11 minutes' WHERE subnet_id = $1`, net)
	res := e.run()
	if state, _ := e.address("10.63.2.1"); state != "offline" || res.Changed["offline"] != 1 {
		t.Errorf("after the grace: state %q (%+v), want offline", state, res)
	}
	// Without last_seen_at the grace runs from since of the down state.
	net2 := e.scanned("10.63.3.0/24", `,"offline_after_s":600`, "10.63.3.1")
	e.state(net2, "10.63.3.1", "down", "timeout", "20 minutes", "10 seconds")
	e.run()
	if state, _ := e.address("10.63.3.1"); state != "offline" {
		t.Errorf("down since 20 minutes without last_seen_at: state %q, want offline", state)
	}
}

// TestCollectorUnreachableYieldsUnknownNotOffline: a lost collector group
// (collector_unreachable) is a network down: unknown, never offline, also
// after twice the grace.
func TestCollectorUnreachableYieldsUnknownNotOffline(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.4.0/24", `,"offline_after_s":600`, "10.63.4.1", "10.63.4.2")
	e.exec(`UPDATE ip_address SET state = 'active', last_seen_at = now() - interval '25 minutes' WHERE subnet_id = $1`, net)
	e.state(net, "10.63.4.1", "unknown", "collector_unreachable", "21 minutes", "25 minutes")
	e.state(net, "10.63.4.2", "unknown", "parent_down", "21 minutes", "25 minutes")
	for range 2 {
		e.run()
	}
	for _, addr := range []string{"10.63.4.1", "10.63.4.2"} {
		if state, _ := e.address(addr); state != "unknown" {
			t.Errorf("%s: state %q, want unknown (not offline)", addr, state)
		}
	}
}

// TestScanUnknownPausesOfflineGrace: the time a probe was unknown does not
// count to the grace (AK2): after twice the grace unknown, a probe down
// since now keeps the address unknown within offline_after_s of the down
// state and makes it offline only afterwards.
func TestScanUnknownPausesOfflineGrace(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.12.0/24", `,"offline_after_s":600`, "10.63.12.1")
	e.exec(`UPDATE ip_address SET state = 'active', last_seen_at = now() - interval '25 minutes' WHERE subnet_id = $1`, net)
	e.state(net, "10.63.12.1", "unknown", "collector_unreachable", "21 minutes", "25 minutes")
	e.run()
	if state, _ := e.address("10.63.12.1"); state != "unknown" {
		t.Fatalf("collector lost: state %q, want unknown", state)
	}
	// The group is back, the host does not answer: down since now.
	e.state(net, "10.63.12.1", "down", "timeout", "0 seconds", "25 minutes")
	e.run()
	if state, _ := e.address("10.63.12.1"); state != "unknown" {
		t.Errorf("down for seconds after the outage: state %q, want unknown (the outage does not count)", state)
	}
	e.state(net, "10.63.12.1", "down", "timeout", "4 minutes", "29 minutes")
	e.run()
	if state, _ := e.address("10.63.12.1"); state != "unknown" {
		t.Errorf("down for 4 of 10 minutes: state %q, want unknown", state)
	}
	e.state(net, "10.63.12.1", "down", "timeout", "11 minutes", "36 minutes")
	if res := e.run(); res.Changed["offline"] != 1 {
		t.Errorf("down beyond the grace: %+v, want offline", res)
	}
	if state, _ := e.address("10.63.12.1"); state != "offline" {
		t.Errorf("down beyond the grace: state %q, want offline", state)
	}
}

// TestScanApplySkipsUnlicensedOrganization: Run handles only the
// organizations Licensed admits (features ipam and monitoring, AK1); the
// addresses of another stay as they are and scan_applied_at stays unset.
func TestScanApplySkipsUnlicensedOrganization(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.13.0/24", "", "10.63.13.1")
	e.exec(`UPDATE ip_address SET state = 'offline', last_seen_at = NULL WHERE subnet_id = $1`, net)
	e.state(net, "10.63.13.1", "up", "", "1 minute", "10 seconds")
	var asked []string
	var mu sync.Mutex
	e.applier.Licensed = func(_ context.Context, orgID string) bool {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, orgID)
		return false
	}
	if err := e.applier.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !slices.Contains(asked, e.f.OrgA) {
		t.Fatalf("Licensed not asked for the organization (asked %v)", asked)
	}
	if state, age := e.address("10.63.13.1"); state != "offline" || age >= 0 {
		t.Errorf("unlicensed: state %q, last_seen_at %d s ago; want unchanged", state, age)
	}
	if n := e.count(`SELECT count(*) FROM subnet WHERE id = $1 AND scan_applied_at IS NOT NULL`, net); n != 0 {
		t.Errorf("unlicensed: scan_applied_at set")
	}
	// Licensed for this organization only: applied.
	e.applier.Licensed = func(_ context.Context, orgID string) bool { return orgID == e.f.OrgA }
	if err := e.applier.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if state, _ := e.address("10.63.13.1"); state != "active" {
		t.Errorf("licensed: state %q, want active", state)
	}
}

// TestScanStateChangeEventOnlyOnChange: a change writes one event; a
// second run without a change writes neither an event nor a row; a change
// of last_seen_at alone writes no event.
func TestScanStateChangeEventOnlyOnChange(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.5.0/24", "", "10.63.5.1")
	e.state(net, "10.63.5.1", "up", "", "1 minute", "30 seconds")
	e.run()
	if n := e.events("10.63.5.1"); n != 1 {
		t.Fatalf("events after the change: %d, want 1", n)
	}
	before := e.scalar(`SELECT updated_at::text FROM ip_address WHERE subnet_id = $1`, net)
	history := e.count(`SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_kind = 'ip_address'`, e.f.OrgA)
	res := e.run()
	if n := e.events("10.63.5.1"); n != 1 || len(res.Changed) != 0 {
		t.Errorf("second run: %d events (%+v), want still 1", n, res)
	}
	if after := e.scalar(`SELECT updated_at::text FROM ip_address WHERE subnet_id = $1`, net); after != before {
		t.Errorf("second run wrote the row: updated_at %s -> %s", before, after)
	}
	e.state(net, "10.63.5.1", "up", "", "1 minute", "1 second")
	e.run()
	if n := e.events("10.63.5.1"); n != 1 {
		t.Errorf("last_seen_at only: %d events, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_kind = 'ip_address'`, e.f.OrgA); n != history {
		t.Errorf("history rows %d -> %d without a change of state", history, n)
	}
}

// TestScanHistorySourceScanWithoutLastSeen: the change of state is in the
// history of the address with source scan; last_seen_at and the bookkeeping
// of the subnet are not.
func TestScanHistorySourceScanWithoutLastSeen(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.6.0/24", "", "10.63.6.1")
	e.state(net, "10.63.6.1", "up", "", "1 minute", "5 seconds")
	e.run()
	id := e.scalar(`SELECT id::text FROM ip_address WHERE subnet_id = $1`, net)
	code, out := e.call(http.MethodGet, "/api/v1/ips/"+id+"/history", "")
	rows, _ := out["data"].([]any)
	var state bool
	for _, r := range rows {
		row := r.(map[string]any)
		switch row["field_name"] {
		case "state":
			state = row["source"] == "scan" && row["new_value"] == "active"
		case "last_seen_at", "state_changed_at":
			if row["field_name"] == "last_seen_at" {
				t.Errorf("history has last_seen_at: %v", row)
			}
		}
	}
	if code != http.StatusOK || !state {
		t.Errorf("history = %d %v, want the state change with source scan", code, out)
	}
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE object_id = $1 AND field_name IN ('scan_applied_at', 'scan_synced_at')`, net); n != 0 {
		t.Errorf("%d history rows of the scan bookkeeping, want none", n)
	}
}

// TestScanNewHostsWithOption: with discover_new_hosts an answering target
// without an address becomes an address of type discovered, origin scan,
// state active.
func TestScanNewHostsWithOption(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.7.0/29", `,"discover_new_hosts":true`, "10.63.7.1")
	e.state(net, "10.63.7.3", "up", "", "1 minute", "5 seconds")
	e.state(net, "10.63.7.4", "down", "timeout", "1 minute", "5 seconds")
	res := e.run()
	if res.NewHosts != 1 {
		t.Fatalf("new hosts %d, want 1 (%+v)", res.NewHosts, res)
	}
	var typ, origin, state, subnet string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT type, origin, state, subnet_id::text FROM ip_address
		WHERE organization_id = $1 AND address_host = '10.63.7.3'`, e.f.OrgA).Scan(&typ, &origin, &state, &subnet); err != nil {
		t.Fatal(err)
	}
	if typ != "discovered" || origin != "scan" || state != "active" || subnet != net {
		t.Errorf("new host: %s %s %s %s, want discovered scan active in the subnet", typ, origin, state, subnet)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.63.7.4'`, e.f.OrgA); n != 0 {
		t.Errorf("a down target became an address")
	}
	if n := e.events("10.63.7.3"); n != 1 {
		t.Errorf("events of the new host: %d, want 1", n)
	}
	// The target stays and is a known address from now on.
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1 AND address = '10.63.7.3'`, net); n != 1 {
		t.Errorf("target of the new host: %d rows, want 1", n)
	}
	if res := e.run(); res.NewHosts != 0 {
		t.Errorf("second run created %d hosts", res.NewHosts)
	}
	_, scan := e.call(http.MethodGet, "/api/v1/subnets/"+net+"/scan", "")
	if num(scan["new_hosts"]) != 1 {
		t.Errorf("GET scan new_hosts %v, want 1", scan["new_hosts"])
	}
}

// TestScanNoNewHostsWithoutOption: without discover_new_hosts no address
// is created, also for a target whose address was deleted meanwhile.
func TestScanNoNewHostsWithoutOption(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.8.0/29", "", "10.63.8.1", "10.63.8.2")
	e.state(net, "10.63.8.2", "up", "", "1 minute", "5 seconds")
	e.exec(`DELETE FROM ip_address WHERE organization_id = $1 AND address_host = '10.63.8.2'`, e.f.OrgA)
	res := e.run()
	if res.NewHosts != 0 {
		t.Errorf("new hosts %d without discover_new_hosts", res.NewHosts)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.63.8.2'`, e.f.OrgA); n != 0 {
		t.Errorf("the deleted address came back")
	}
	// The resync of the run removed the stale target.
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1`, net); n != 1 || res.Resynced != 1 {
		t.Errorf("targets %d after the resync (%+v), want 1", n, res)
	}
}

// TestScanNewHostRaceWithDiscovery: a discovery sync inserts the same
// address while the run creates it: no error, one row.
func TestScanNewHostRaceWithDiscovery(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.9.0/29", `,"discover_new_hosts":true`)
	e.state(net, "10.63.9.5", "up", "", "1 minute", "5 seconds")
	ctx := context.Background()
	tx, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO ip_address (organization_id, subnet_id, address, type, origin)
		VALUES ($1, $2, '10.63.9.5', 'discovered', 'discovery')`, e.f.OrgA, net); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var res ipam.ScanApplyResult
	var runErr error
	wg.Go(func() { res, runErr = e.applier.RunOrg(ctx, e.f.OrgA) })
	time.Sleep(300 * time.Millisecond) // the run waits on the unique index
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address_host = '10.63.9.5'`, e.f.OrgA); n != 1 {
		t.Errorf("%d rows of the address, want 1", n)
	}
	if res.NewHosts != 0 {
		t.Errorf("the run counted %d new hosts, the discovery row won", res.NewHosts)
	}
	if origin := e.scalar(`SELECT origin FROM ip_address WHERE organization_id = $1 AND address_host = '10.63.9.5'`, e.f.OrgA); origin != "discovery" {
		t.Errorf("origin %s, want discovery", origin)
	}
}

// TestScanResyncAfterAddressAdded: an address created after the scan was
// switched on gets its probe in the next run.
func TestScanResyncAfterAddressAdded(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.10.0/24", "", "10.63.10.1")
	e.mustID("/api/v1/ips", `{"address":"10.63.10.9","subnet_id":"`+net+`"}`)
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1`, net); n != 1 {
		t.Fatalf("targets before the run: %d, want 1", n)
	}
	res := e.run()
	if res.Resynced != 1 {
		t.Errorf("resynced %d subnets, want 1", res.Resynced)
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target t JOIN probe p ON p.id = t.probe_id
		WHERE t.subnet_id = $1 AND t.address = '10.63.10.9' AND p.managed_by = 'ipam_scan'`, net); n != 1 {
		t.Errorf("no managed probe for the new address")
	}
	// A run without a change synchronizes nothing.
	if res := e.run(); res.Resynced != 0 {
		t.Errorf("second run resynced %d subnets", res.Resynced)
	}
}

// TestScanResyncAfterAddressFromOlderTransaction: an address committed
// after a run by a transaction that began before it (created_at older than
// scan_synced_at) still gets its probe: the criterion is the missing
// target, not the timestamp. A deleted address loses its target.
func TestScanResyncAfterAddressFromOlderTransaction(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.63.14.0/24", "", "10.63.14.1")
	e.run()
	e.exec(`INSERT INTO ip_address (organization_id, subnet_id, address, type, origin, created_at)
		VALUES ($1, $2, '10.63.14.9', 'static', 'manual', now() - interval '1 hour')`, e.f.OrgA, net)
	if res := e.run(); res.Resynced != 1 {
		t.Errorf("resynced %d subnets, want 1", res.Resynced)
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1 AND address = '10.63.14.9'`, net); n != 1 {
		t.Errorf("the address of the older transaction has no target")
	}
	e.exec(`DELETE FROM ip_address WHERE subnet_id = $1 AND address_host = '10.63.14.9'`, net)
	e.exec(`UPDATE subnet SET scan_synced_at = now() + interval '1 hour' WHERE id = $1`, net)
	if res := e.run(); res.Resynced != 1 {
		t.Errorf("after the delete resynced %d subnets, want 1", res.Resynced)
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1`, net); n != 1 {
		t.Errorf("targets after the delete: %d, want 1", n)
	}
	if res := e.run(); res.Resynced != 0 {
		t.Errorf("a run without a change resynced %d subnets", res.Resynced)
	}
}

// TestScanResyncSkipsFailingSubnet: a subnet whose synchronization fails
// (here its collector group belongs to another client) is skipped under
// its savepoint and marked; a second subnet with a new address gets its
// probe all the same, and the failed one moves to the end of the order.
func TestScanResyncSkipsFailingSubnet(t *testing.T) {
	e := newApplyEnv(t)
	bad := e.scanned("10.63.15.0/24", "", "10.63.15.1")
	good := e.scanned("10.63.16.0/24", "", "10.63.16.1")
	e.exec(`UPDATE subnet SET scan_synced_at = now() - interval '1 day' WHERE id = $1`, bad)
	e.exec(`UPDATE subnet SET collector_group_id = $2 WHERE id = $1`, bad, e.g2)
	e.exec(`INSERT INTO ip_address (organization_id, subnet_id, address, type, origin) VALUES ($1, $2, '10.63.15.9', 'static', 'manual')`, e.f.OrgA, bad)
	e.exec(`INSERT INTO ip_address (organization_id, subnet_id, address, type, origin) VALUES ($1, $2, '10.63.16.9', 'static', 'manual')`, e.f.OrgA, good)
	res := e.run()
	if res.Resynced != 1 || res.ResyncFailed != 1 {
		t.Errorf("result %+v, want one subnet resynced and one failed", res)
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1 AND address = '10.63.16.9'`, good); n != 1 {
		t.Errorf("the second subnet got no target for its new address")
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1 AND address = '10.63.15.9'`, bad); n != 0 {
		t.Errorf("the failed subnet got a target")
	}
	if n := e.count(`SELECT count(*) FROM subnet WHERE id = $1 AND scan_sync_failed_at IS NOT NULL
		AND updated_at < scan_sync_failed_at`, bad); n != 1 {
		t.Errorf("the failed subnet is not marked (or its updated_at moved)")
	}
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE object_id = $1 AND field_name = 'scan_sync_failed_at'`, bad); n != 0 {
		t.Errorf("scan_sync_failed_at recorded in the history")
	}
}

// TestScanApplyStatementBudget: 5,000 targets with at most six statements
// of the run (the tenant frame not counted).
func TestScanApplyStatementBudget(t *testing.T) {
	e := newApplyEnv(t)
	net := e.scanned("10.64.0.0/18", "")
	e.exec(`INSERT INTO ip_address (organization_id, subnet_id, address, type, origin)
		SELECT $1, $2, '10.64.0.0'::inet + n, 'static', 'manual' FROM generate_series(1, 5000) AS n`, e.f.OrgA, net)
	if err := database.WithTenant(context.Background(), e.f.App, ptr(database.OrgWideScope(e.f.OrgA, "")),
		func(ctx context.Context, tx pgx.Tx) error {
			return e.ipam.SyncScanTargets(ctx, tx, e.f.OrgA, []string{net})
		}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	e.exec(`INSERT INTO probe_state (probe_id, organization_id, state, reason, since, last_result_at)
		SELECT t.probe_id, t.organization_id, CASE WHEN (host(t.address)::inet - '10.64.0.0'::inet) % 2 = 0 THEN 'up' ELSE 'down' END,
		       '', now() - interval '2 hours', now() - interval '2 hours'
		  FROM subnet_scan_target t WHERE t.subnet_id = $1`, net)
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1`, net); n != 5000 {
		t.Fatalf("targets %d, want 5000", n)
	}
	repo := ipam.NewPGRepository(e.f.App).WithEvents(events.NewOutbox())
	trace := scopetest.NewQueryTrace(t, e.f.App)
	scope := database.OrgWideScope(e.f.OrgA, "")
	frame := trace.Run(t, func() error {
		return database.WithTenant(context.Background(), trace.Pool, &scope, func(context.Context, pgx.Tx) error { return nil })
	})
	var res ipam.ScanApplyResult
	stmts := trace.Run(t, func() error {
		return database.WithTenant(context.Background(), trace.Pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			res, err = repo.ApplyScanResults(ctx, tx, e.f.OrgA)
			return err
		})
	})
	if res.Changed["active"] != 2500 || res.Changed["offline"] != 2500 {
		t.Fatalf("result %+v, want 2500 active and 2500 offline", res)
	}
	if n := len(stmts) - len(frame); n > 6 {
		var sqls []string
		for _, s := range stmts {
			sqls = append(sqls, strings.Join(strings.Fields(s.SQL), " ")[:min(80, len(strings.Join(strings.Fields(s.SQL), " ")))])
		}
		t.Errorf("%d statements for 5000 targets, budget 6: %q", n, sqls)
	}
	if n := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = $2`, e.f.OrgA, events.IPAMAddressStateChanged); n != 5000 {
		t.Errorf("%d events, want 5000", n)
	}
}

func ptr[T any](v T) *T { return &v }

// TestScanApplyRunsOncePerTick: of two parallel runs of one organization
// one holds the lock, the other skips it.
func TestScanApplyRunsOncePerTick(t *testing.T) {
	e := newApplyEnv(t)
	e.scanned("10.63.11.0/29", "", "10.63.11.1")
	ctx := context.Background()
	scope := database.OrgWideScope(e.f.OrgA, "")
	repo := ipam.NewPGRepository(e.f.App)
	hold := make(chan struct{})
	held := make(chan ipam.ScanApplyResult, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = database.WithTenant(ctx, e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			res, err := repo.ApplyScanResults(ctx, tx, e.f.OrgA)
			held <- res
			<-hold
			return err
		})
	})
	first := <-held
	second, err := e.applier.RunOrg(ctx, e.f.OrgA)
	close(hold)
	wg.Wait()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first.Skipped || !first.Scanned {
		t.Errorf("first run %+v, want it to hold the lock", first)
	}
	if !second.Skipped {
		t.Errorf("second run %+v, want it skipped", second)
	}
	if third := e.run(); third.Skipped {
		t.Errorf("after the first run ended the lock is free, got %+v", third)
	}
}
