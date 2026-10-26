package discovery_test

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestReconciliationBeyondTenThousandCIs covers NFR-01 (WP-205) against
// PostgreSQL: with more than 10,000 CIs the identity search finds the newest
// CI by serial number, returns every match of a hostname shared by more CIs
// than one page holds (no upper bound, including CIs with the same
// created_at) and finds an edge of a source with more than 1,000 edges by
// index lookup.
//
// Re-discovery on the queue path (WP-434, the open evidence of WP-205): every
// CI carries a primary MAC and two interface MACs in its fingerprint. 210
// records of known devices across the inventory, identified by MACs only
// (the primary MAC and the interface MACs, or an interface MAC as primary
// MAC), go through the consumer in batches of the queue: no new CI, every CI
// confirmed, ci_identity_mac still matches every CI, and no statement of the
// run reads ci or ci_identity_mac other than through the primary key or an
// identity index (EXPLAIN as the application role), so no record scans the
// organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReconciliationBeyondTenThousandCIs(t *testing.T) {
	f := scopetest.Seed(t, "c4")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	const inventory, shared = 10500, 2345
	// MACs of device n: primary 00:1b:31:…, interfaces 00:1b:32:… and
	// 00:1b:33:…, stored in the fingerprint as the ingest stores them.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, serial_number, hostname, primary_mac,
			attributes, discovery_source, created_at)
		SELECT $1, $2, 'scale-' || n, 'active', 'SCALE-' || n,
			CASE WHEN n <= $4 THEN 'shared-host' ELSE 'scale-host-' || n END,
			('00:1b:31:' || m.h)::macaddr,
			jsonb_build_object('fingerprint', jsonb_build_object('mac_addresses',
				jsonb_build_array('00:1b:31:' || m.h, '00:1b:32:' || m.h, '00:1b:33:' || m.h))),
			'snmp',
			CASE WHEN n <= $4 THEN now() ELSE now() - make_interval(secs => $3 - n) END
		FROM generate_series(1, $3) AS n
		CROSS JOIN LATERAL (SELECT substr(lpad(to_hex(n), 6, '0'), 1, 2) || ':' || substr(lpad(to_hex(n), 6, '0'), 3, 2) || ':'
			|| substr(lpad(to_hex(n), 6, '0'), 5, 2) AS h) m`, f.OrgA, serverType, inventory, shared); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"ci", "ci_identity_mac"} {
		if _, err := f.Admin.Exec(bg, `ANALYZE `+table); err != nil {
			t.Fatal(err)
		}
	}
	repo := discovery.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)

	ids, err := repo.IdentityCandidates(ctx, f.OrgA, &discovery.IdentityQuery{SerialNumber: "SCALE-10500"})
	if err != nil || len(ids) != 1 {
		t.Fatalf("serial of the newest CI: %v, err %v", ids, err)
	}

	ids, err = repo.IdentityCandidates(ctx, f.OrgA, &discovery.IdentityQuery{Hostnames: []string{"shared-host"}})
	if err != nil {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	if len(ids) != shared || len(unique) != shared {
		t.Fatalf("shared hostname: %d candidates (%d distinct), want %d", len(ids), len(unique), shared)
	}

	// A switch with 1,200 edges: the edge to the last neighbour is found.
	var switchID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'SCALE-10000'`, f.OrgA).Scan(&switchID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		SELECT $1, $2, c.id, 'connected_to', 'discovery' FROM ci c
		WHERE c.organization_id = $1 AND c.serial_number IN (SELECT 'SCALE-' || n FROM generate_series(2501, 3700) AS n)`,
		f.OrgA, switchID); err != nil {
		t.Fatal(err)
	}
	var lastID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'SCALE-3700'`, f.OrgA).Scan(&lastID); err != nil {
		t.Fatal(err)
	}
	id, source, found, err := repo.DiscoveryEdge(ctx, f.OrgA, switchID, lastID, "connected_to")
	if err != nil || !found || id == "" || source != "discovery" {
		t.Fatalf("edge to neighbour 1,200: id %q source %q found %v err %v", id, source, found, err)
	}
	if _, _, found, err := repo.DiscoveryEdge(ctx, f.OrgA, switchID, lastID, "powered_by"); err != nil || found {
		t.Fatalf("edge of another type: found %v err %v", found, err)
	}

	t.Run("re-discovery from the queue", func(t *testing.T) {
		capture := &auditSQLCapture{}
		traced := tracedPool(t, f, capture)
		h := discovery.NewHandler(discovery.NewPGRepository(traced), ci.NewPGRepository(traced))
		queued := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		mac := func(prefix string, n int) string {
			x := fmt.Sprintf("%06x", n)
			return prefix + x[0:2] + ":" + x[2:4] + ":" + x[4:6]
		}
		// Every 50th device: even records with the primary MAC and both
		// interfaces, odd ones with an interface MAC as primary MAC (found
		// only through the fingerprint) and the other interface.
		var records []plugins.DeviceRecord
		var serials []string
		for n := 50; n <= inventory; n += 50 {
			rec := plugins.DeviceRecord{
				RecordID: plugins.NewRecordID(), ObservedAt: time.Now().UTC().Add(-time.Second), Source: "snmp",
				TargetIP:       fmt.Sprintf("10.250.%d.%d", n/250, n%250+1),
				Identity:       plugins.DeviceIdentity{PrimaryMAC: mac("00:1b:31:", n)},
				Classification: plugins.DeviceClassification{CIType: "server"},
				System:         plugins.DeviceSystem{Name: fmt.Sprintf("scale-%d", n)},
				Interfaces: []plugins.DeviceInterface{
					{Name: "eth1", IfIndex: 2, MAC: mac("00:1b:32:", n)}, {Name: "eth2", IfIndex: 3, MAC: mac("00:1b:33:", n)},
				},
			}
			if len(records)%2 == 1 {
				rec.Identity.PrimaryMAC = mac("00:1b:32:", n)
				rec.Interfaces = rec.Interfaces[1:]
			}
			records = append(records, rec)
			serials = append(serials, fmt.Sprintf("SCALE-%d", n))
		}
		start := time.Now().UTC().Truncate(time.Second)
		capture.take()
		for from := 0; from < len(records); from += 100 {
			batch := records[from:min(from+100, len(records))]
			for i, result := range discovery.ConsumeRecords(queued, h, f.OrgA, batch...) {
				if result != "ack" {
					t.Fatalf("record %d: delivery %s", from+i, result)
				}
			}
		}
		statements, args := capture.take()

		var total, confirmed, stale int
		if err := f.Admin.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE serial_number = ANY($2) AND last_seen_at >= $3)
			FROM ci WHERE organization_id = $1`, f.OrgA, serials, start).Scan(&total, &confirmed); err != nil {
			t.Fatal(err)
		}
		if total != inventory || confirmed != len(records) {
			t.Errorf("re-discovery of %d known devices: %d CIs (want %d, no new CI), %d confirmed (want %d)",
				len(records), total, inventory, confirmed, len(records))
		}
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND
			(SELECT COALESCE(array_agg(m.mac ORDER BY m.mac), '{}') FROM ci_identity_mac m WHERE m.ci_id = c.id)
				IS DISTINCT FROM (SELECT COALESCE(array_agg(x ORDER BY x), '{}') FROM unnest(ci_identity_macs(c.primary_mac, c.attributes)) AS x)`,
			f.OrgA).Scan(&stale); err != nil || stale != 0 {
			t.Errorf("%d CIs whose ci_identity_mac rows do not match their MACs (err %v)", stale, err)
		}

		// No statement reads the organization: every read of ci and
		// ci_identity_mac goes through the primary key or an identity index.
		allowed := map[string][]string{
			"ci": {"ci_pkey", "ci_id_organization_key", "idx_ci_serial", "uq_ci_org_serial", "idx_ci_hardware_uuid", "uq_ci_org_hardware_uuid",
				"idx_ci_primary_mac", "uq_ci_org_primary_mac", "idx_ci_mgmt_ip", "idx_ci_mgmt_ip_sys_object", "idx_ci_hostname_lc", "idx_ci_fqdn_lc"},
			"ci_identity_mac": {"ci_identity_mac_pkey", "idx_ci_identity_mac"},
		}
		reads := regexp.MustCompile(`(?i)\b(from|join)\s+(ci|ci_identity_mac)\b`)
		query := regexp.MustCompile(`(?is)^\s*(/\*.*?\*/\s*)?(select|with)\b`)
		explained, identity := 0, 0
		for i, sql := range statements {
			if !reads.MatchString(sql) || !query.MatchString(sql) {
				continue
			}
			if strings.Contains(sql, "ci_identity_mac") {
				identity++
			}
			explained++
			for relation, indexes := range allowed {
				for _, r := range relationReads(t, f, relation, sql, args[i]...) {
					if !slices.Contains(indexes, r.index) || (relation == "ci_identity_mac" && !strings.Contains(r.cond, "mac = ")) {
						t.Errorf("re-discovery reads %s with %s (index condition %q): %s", relation, r, r.cond, sql)
					}
				}
			}
		}
		if identity == 0 || identity > 2*len(records) {
			t.Errorf("%d identity queries over ci_identity_mac for %d records", identity, len(records))
		}
		t.Logf("re-discovery of %d known devices from the queue: %d statements, %d reading ci explained, %d identity queries over ci_identity_mac",
			len(records), len(statements), explained, identity)
	})
}
