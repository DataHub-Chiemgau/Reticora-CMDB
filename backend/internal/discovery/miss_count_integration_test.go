package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

// TestInterfacesMissedThreeTimesAreRemoved covers WP-090 (REC-07, AUD-03,
// NET-01) against PostgreSQL: a sighting without other changes writes no
// ci_change and brings an unknown CI back to active; reported interfaces and
// addresses are kept in IPAM with miss_count 0; an interface or address not
// reported by three consecutive observations is removed with an entry in the
// CI's history; a record without interfaces does not count as a miss.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInterfacesMissedThreeTimesAreRemoved(t *testing.T) {
	f := scopetest.Seed(t, "82")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithInterfaces(ipam.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	record := func(source string, interfaces ...plugins.DeviceInterface) plugins.DeviceRecord {
		rec := plugins.RecordFromResult(source, &plugins.Result{CIType: "server", Name: "miss-82", IP: "192.0.2.82", Serial: "MISS-82"},
			time.Now().Add(-time.Second))
		rec.Interfaces = interfaces
		return rec
	}
	eth0 := plugins.DeviceInterface{Name: "eth0", MAC: "00:1b:21:00:00:82", IfIndex: 1, MTU: 1500,
		IPs: []plugins.DeviceIP{{Address: "192.0.2.82", PrefixLength: 24}}}
	eth1 := plugins.DeviceInterface{Name: "eth1", IfIndex: 2, IPs: []plugins.DeviceIP{{Address: "198.51.100.82", PrefixLength: 24}}}

	if resp := ingestRecords(t, mux, ctx, record("snmp", eth0, eth1)); resp.Created != 1 {
		t.Fatalf("first record: %+v", resp)
	}
	var ciID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'MISS-82'`, f.OrgA).Scan(&ciID); err != nil {
		t.Fatal(err)
	}
	state := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		rows, err := f.Admin.Query(bg, `SELECT name, miss_count FROM network_interface WHERE ci_id = $1
			UNION ALL SELECT host(a.address), a.miss_count FROM ip_address a JOIN network_interface n ON n.id = a.interface_id WHERE n.ci_id = $1`, ciID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var misses int
			if err = rows.Scan(&name, &misses); err != nil {
				t.Fatal(err)
			}
			out[name] = misses
		}
		return out
	}
	changes := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1`, ciID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := state(); len(got) != 4 || got["eth0"] != 0 || got["eth1"] != 0 || got["192.0.2.82"] != 0 || got["198.51.100.82"] != 0 {
		t.Fatalf("after the first record: %v", got)
	}
	var mtu, ifIndex int
	if err := f.Admin.QueryRow(bg, `SELECT mtu, if_index FROM network_interface WHERE ci_id = $1 AND name = 'eth0'`, ciID).Scan(&mtu, &ifIndex); err != nil ||
		mtu != 1500 || ifIndex != 1 {
		t.Errorf("eth0: mtu %d if_index %d, %v", mtu, ifIndex, err)
	}

	// A plain sighting: last_seen_at moves, no history; unknown → active.
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET status = 'unknown', last_seen_at = now() - interval '1 hour' WHERE id = $1`, ciID); err != nil {
		t.Fatal(err)
	}
	history := changes()
	ingestRecords(t, mux, ctx, record("sweep"))
	var status string
	var fresh bool
	if err := f.Admin.QueryRow(bg, `SELECT status, last_seen_at > now() - interval '1 minute' FROM ci WHERE id = $1`, ciID).Scan(&status, &fresh); err != nil {
		t.Fatal(err)
	}
	if status != "active" || !fresh {
		t.Errorf("after the sighting: status %q, last_seen_at fresh %v", status, fresh)
	}
	if n := changes(); n != history+1 {
		t.Errorf("ci_change rows for the sighting: %d, want only the status change", n-history)
	}
	history = changes()
	ingestRecords(t, mux, ctx, record("sweep"))
	if n := changes(); n != history {
		t.Errorf("a sighting without changes wrote %d ci_change rows", n-history)
	}
	// The sweep reported no interfaces: nothing was missed.
	if got := state(); got["eth1"] != 0 {
		t.Errorf("record without interfaces counted a miss: %v", got)
	}

	// eth1 and its address are missed by three observations.
	for miss := 1; miss <= 3; miss++ {
		ingestRecords(t, mux, ctx, record("snmp", eth0))
		got := state()
		if got["eth0"] != 0 || got["192.0.2.82"] != 0 {
			t.Fatalf("observation %d: reported interface counted a miss: %v", miss, got)
		}
		if miss < 3 && (got["eth1"] != miss || got["198.51.100.82"] != miss) {
			t.Errorf("observation %d: %v", miss, got)
		}
		if miss == 3 {
			if _, ok := got["eth1"]; ok || len(got) != 2 {
				t.Errorf("after three misses: %v", got)
			}
		}
	}
	var removed int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name IN ('interfaces', 'ip_addresses')
		AND old_value ?| ARRAY['eth1', '198.51.100.82']`, ciID).Scan(&removed); err != nil || removed != 2 {
		t.Errorf("history of the removal: %d, %v", removed, err)
	}
	// Reported again, eth1 comes back with miss_count 0.
	ingestRecords(t, mux, ctx, record("snmp", eth0, eth1))
	if got := state(); got["eth1"] != 0 || len(got) != 4 {
		t.Errorf("eth1 reported again: %v", got)
	}
}

// TestInterfacesMissedKeepManualPortEdges covers WP-424 (REL-05, REC-07;
// F-042) on the ingest path of SNMP device records with interfaces and LLDP
// neighbours (WP-416) against PostgreSQL: a manual edge at port Gi1/0/1 of
// the switch survives three records without Gi1/0/1, and so does the port
// (miss_count 3); a discovery edge at Gi1/0/1 is removed and recorded in the
// switch's history; the edge the records derive from the LLDP neighbour at
// Gi1/0/2 stays.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInterfacesMissedKeepManualPortEdges(t *testing.T) {
	f := scopetest.Seed(t, "83")
	bg := context.Background()
	const peerMAC = "00:1b:21:00:04:24"
	peer, patched, stale := f.CI(t, f.OrgA, "", "wp424-peer"), f.CI(t, f.OrgA, "", "wp424-patched"), f.CI(t, f.OrgA, "", "wp424-stale")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, peer, peerMAC); err != nil {
		t.Fatal(err)
	}
	rels := relationship.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()), rels).
		WithInterfaces(ipam.NewPGRepository(f.App)).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	record := func(ports ...string) plugins.DeviceRecord {
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "wp424-sw", IP: "192.0.2.124", Serial: "WP424SW"},
			time.Now().Add(-time.Second))
		for i, p := range ports {
			rec.Interfaces = append(rec.Interfaces, plugins.DeviceInterface{Name: p, IfIndex: i + 1})
		}
		rec.Neighbors = []plugins.DeviceNeighbor{{Protocol: "lldp", LocalInterface: "Gi1/0/2", RemoteMAC: peerMAC, RemoteInterface: "Te1/1/1"}}
		return rec
	}
	if resp := ingestRecords(t, mux, ctx, record("Gi1/0/1", "Gi1/0/2")); resp.Created != 1 {
		t.Fatalf("first record: %+v", resp)
	}
	var sw, gi1 string
	if err := f.Admin.QueryRow(bg, `SELECT c.id::text, n.id::text FROM ci c JOIN network_interface n ON n.ci_id = c.id AND n.name = 'Gi1/0/1'
		WHERE c.organization_id = $1 AND c.serial_number = 'WP424SW'`, f.OrgA).Scan(&sw, &gi1); err != nil {
		t.Fatal(err)
	}
	manual := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: sw, TargetCIID: patched, RelType: "connected_to",
		Source: "manual", SourceInterfaceID: gi1}
	if err := rels.Create(ctx, manual); err != nil {
		t.Fatalf("manual edge at Gi1/0/1: %v", err)
	}
	var discovered string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source, source_interface_id)
		VALUES ($1, $2, $3, 'connected_to', 'discovery', $4) RETURNING id::text`, f.OrgA, sw, stale, gi1).Scan(&discovered); err != nil {
		t.Fatal(err)
	}

	for miss := 1; miss <= ipam.MaxMissedObservations; miss++ {
		ingestRecords(t, mux, ctx, record("Gi1/0/2"))
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id = $1 AND source = 'manual' AND source_interface_id = $2`, manual.ID, gi1); n != 1 {
		t.Errorf("manual edge at Gi1/0/1 after %d records without the port: rows=%d, want 1", ipam.MaxMissedObservations, n)
	}
	if n := count(`SELECT count(*) FROM network_interface WHERE id = $1 AND miss_count = $2`, gi1, ipam.MaxMissedObservations); n != 1 {
		t.Errorf("Gi1/0/1 with miss_count %d: rows=%d, want 1", ipam.MaxMissedObservations, n)
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE id = $1`, discovered); n != 0 {
		t.Error("discovery edge at the missed Gi1/0/1 still present")
	}
	if n := count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'relationship_change' AND old_value->>'id' = $2
		AND new_value IS NULL`, sw, discovered); n != 1 {
		t.Errorf("history of the removed discovery edge: %d rows, want 1", n)
	}
	if n := count(`SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND source = 'discovery'`, sw, peer); n != 1 {
		t.Errorf("LLDP edge to the neighbour at Gi1/0/2: rows=%d, want 1", n)
	}
}
