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
