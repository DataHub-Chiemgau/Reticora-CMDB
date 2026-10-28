package discovery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestIdentityHitsMatchSingle covers WP-333 (NFR-03, REC-02, CI-13) against
// PostgreSQL with 10,000 CIs: the batch candidate queries return for every
// record exactly the candidates and the order of the per-record queries –
// live CIs by serial number, hardware UUID, primary and fingerprint MAC,
// management IP, hostname and FQDN, beyond one page of 1,000 hits with equal
// created_at, and the newest 1,000 soft-deleted CIs – and each branch of
// them reads ci only through its identity index under row-level security.
// The MAC branch reads ci_identity_mac through its index and ci through the
// primary key, for primary and fingerprint MACs alike (WP-434, migration
// 000161; before, jsonb containment read the organization once per batch).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIdentityHitsMatchSingle(t *testing.T) {
	f := scopetest.Seed(t, "e1")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	// 10,000 live CIs: 1,200 share a hostname and their created_at, every
	// tenth carries an interface MAC in its fingerprint. 1,100 soft-deleted
	// CIs share a management IP, the 100 newest of them their deleted_at.
	const inventory, shared, gone = 10000, 1200, 1100
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, serial_number, hardware_uuid,
			primary_mac, management_ip, hostname, fqdn, attributes, created_at)
		SELECT $1, $2, 'ih-' || n, 'active', 'IH-' || n,
			('4c4c4544-0000-4000-8000-' || lpad(to_hex(n), 12, '0'))::uuid,
			('00:1b:21:' || substr(lpad(to_hex(n), 6, '0'), 1, 2) || ':' || substr(lpad(to_hex(n), 6, '0'), 3, 2) || ':'
				|| substr(lpad(to_hex(n), 6, '0'), 5, 2))::macaddr,
			('10.88.' || (n / 250) || '.' || (n % 250 + 1))::inet,
			CASE WHEN n <= $4 THEN 'IH-Shared' ELSE 'ih-host-' || n END,
			'ih-host-' || n || '.example.test',
			CASE WHEN n % 10 = 0 THEN jsonb_build_object('fingerprint', jsonb_build_object('mac_addresses',
				jsonb_build_array('00:1b:22:' || substr(lpad(to_hex(n), 6, '0'), 1, 2) || ':' || substr(lpad(to_hex(n), 6, '0'), 3, 2)
					|| ':' || substr(lpad(to_hex(n), 6, '0'), 5, 2)))) ELSE '{}'::jsonb END,
			CASE WHEN n <= $4 THEN now() ELSE now() - make_interval(secs => $3 - n) END
		FROM generate_series(1, $3) AS n`, f.OrgA, serverType, inventory, shared); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, serial_number, management_ip,
			hostname, deleted_at)
		SELECT $1, $2, 'ih-gone-' || n, 'active', 'IH-GONE-' || n, '10.99.0.1'::inet, 'ih-gone-' || n,
			CASE WHEN n <= 100 THEN now() ELSE now() - make_interval(secs => n) END
		FROM generate_series(1, $3) AS n`, f.OrgA, serverType, gone); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"ci", "ci_identity_mac"} {
		if _, err := f.Admin.Exec(bg, `ANALYZE `+table); err != nil {
			t.Fatal(err)
		}
	}
	repo := discovery.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	uuidOf := func(n int) string { return fmt.Sprintf("4c4c4544-0000-4000-8000-%012x", n) }
	macOf := func(prefix string, n int) string {
		h := fmt.Sprintf("%06x", n)
		return prefix + h[0:2] + ":" + h[2:4] + ":" + h[4:6]
	}

	qs := []discovery.IdentityQuery{
		{SerialNumber: "IH-42"},
		{HardwareUUID: strings.ToUpper(uuidOf(77))},
		{MACs: []string{macOf("00:1b:21:", 100), macOf("00:1b:22:", 200)}},
		{ManagementIP: "10.88.1.51/24"},
		{Hostnames: []string{"ih-shared"}},
		{Hostnames: []string{"ih-host-500.example.test"}},
		{SerialNumber: "IH-601", HardwareUUID: uuidOf(600), MACs: []string{macOf("00:1b:21:", 602), macOf("00:1b:22:", 610)},
			ManagementIP: "10.88.2.104", Hostnames: []string{"ih-host-605", "ih-host-606.example.test"}},
		{},
		{SerialNumber: "IH-42", ManagementIP: "10.88.0.43"},
		{ManagementIP: "10.99.0.1"},
		{SerialNumber: "IH-GONE-1050", Hostnames: []string{"ih-gone-7"}},
		{SerialNumber: "IH-UNKNOWN"},
	}
	include := make([]bool, len(qs))
	for i := range include {
		include[i] = true
	}
	keys, refs := discovery.BatchKeys(qs, include)
	hits, err := repo.IdentityHits(ctx, f.OrgA, keys)
	if err != nil {
		t.Fatal(err)
	}
	got := discovery.AssignCandidates(refs, hits)
	deletedHits, err := repo.DeletedIdentityHits(ctx, f.OrgA, keys)
	if err != nil {
		t.Fatal(err)
	}
	gotDeleted := discovery.AssignDeleted(refs, deletedHits)
	for i := range qs {
		want, err := repo.IdentityCandidates(ctx, f.OrgA, &qs[i])
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("record %d (%+v): batch %d candidates, per record %d, or another order", i, qs[i], len(got[i]), len(want))
		}
		wantDeleted, err := repo.DeletedIdentityCandidates(ctx, f.OrgA, &qs[i])
		if err != nil {
			t.Fatal(err)
		}
		if len(gotDeleted[i]) != len(wantDeleted) || (len(wantDeleted) > 0 && !reflect.DeepEqual(gotDeleted[i], wantDeleted)) {
			t.Errorf("record %d (%+v): batch %d deleted candidates, per record %d, or other rows", i, qs[i], len(gotDeleted[i]), len(wantDeleted))
		}
	}
	// The fixture reaches what it is for.
	if len(got[4]) != shared || len(got[6]) < 6 || len(gotDeleted[9]) != 1000 || len(gotDeleted[10]) != 2 || len(got[7]) != 0 {
		t.Errorf("fixture: shared hostname %d candidates, combined record %d, shared deleted IP %d, deleted serial and host %d, no keys %d",
			len(got[4]), len(got[6]), len(gotDeleted[9]), len(gotDeleted[10]), len(got[7]))
	}

	// Each branch on its own, with 20 values of its kind.
	values := func(fn func(n int) string) []string {
		out := make([]string, 20)
		for i := range out {
			out[i] = fn(3000 + 37*i)
		}
		return out
	}
	byIndex := map[string][]string{
		"serial": {"idx_ci_serial_number_lc"}, "uuid": {"idx_ci_hardware_uuid", "uq_ci_org_hardware_uuid"},
		"mac": {}, "ip": {"idx_ci_mgmt_ip", "idx_ci_mgmt_ip_sys_object"},
		"hostname": {"idx_ci_hostname_lc"}, "fqdn": {"idx_ci_fqdn_lc"}, "host": {"idx_ci_hostname_lc", "idx_ci_fqdn_lc"},
	}
	for _, c := range []struct {
		name string
		keys discovery.IdentityKeys
	}{
		{"serial", discovery.IdentityKeys{Serials: values(func(n int) string { return fmt.Sprintf("IH-%d", n) })}},
		{"uuid", discovery.IdentityKeys{UUIDs: values(uuidOf)}},
		{"mac", discovery.IdentityKeys{MACs: values(func(n int) string { return macOf("00:1b:21:", n) })}},
		{"mac", discovery.IdentityKeys{MACs: values(func(n int) string { return macOf("00:1b:22:", n) })}},
		{"ip", discovery.IdentityKeys{IPs: values(func(n int) string { return fmt.Sprintf("10.88.%d.%d", n/250, n%250+1) })}},
		{"hostname", discovery.IdentityKeys{Hostnames: values(func(n int) string { return fmt.Sprintf("ih-host-%d", n) })}},
		{"fqdn", discovery.IdentityKeys{Hostnames: values(func(n int) string { return fmt.Sprintf("ih-host-%d.example.test", n) })}},
		{"host", discovery.IdentityKeys{Hostnames: values(func(n int) string { return fmt.Sprintf("ih-gone-%d", n) })}},
	} {
		found := false
		for _, deleted := range []bool{false, true} {
			query, args, ok := discovery.IdentityBranchQuery(f.OrgA, &c.keys, deleted, c.name)
			if !ok {
				continue
			}
			found = true
			reads := ciReads(t, f, query, args...)
			label := fmt.Sprintf("%s (deleted %v)", c.name, deleted)
			if c.name == "mac" {
				macReads := relationReads(t, f, "ci_identity_mac", query, args...)
				for _, r := range macReads {
					if r.index != "idx_ci_identity_mac" || !strings.Contains(r.cond, "mac = ") {
						t.Errorf("%s reads ci_identity_mac with %s (index condition %q), want idx_ci_identity_mac on the MACs: %s",
							label, r, r.cond, query)
					}
				}
				if len(macReads) == 0 {
					t.Errorf("%s: no read of ci_identity_mac in the plan", label)
				}
			}
			for _, r := range reads {
				allowed := append([]string{"ci_pkey", "ci_id_organization_key"}, byIndex[c.name]...)
				if r.index == "" || !slices.Contains(allowed, r.index) {
					t.Errorf("%s reads ci with %s %s, want one of %v: %s", label, r.node, r.index, allowed, query)
				}
			}
			if len(reads) == 0 {
				t.Errorf("%s: no read of ci in the plan", label)
			}
		}
		if !found {
			t.Errorf("the batch candidate query has no branch %s", c.name)
		}
	}
}

// ciRead is a node of a plan that reads ci, with its index and index
// condition.
type ciRead struct{ node, index, cond string }

func (r ciRead) String() string { return strings.TrimSpace(r.node + " " + r.index) }

// ciReads returns the nodes of the plan of query that read ci, in the
// tenant context of organization A (row-level security applies). A bitmap
// heap scan is reported with the indexes of its bitmap.
func ciReads(t *testing.T, f *scopetest.Fixture, query string, args ...any) []ciRead {
	t.Helper()
	return relationReads(t, f, "ci", query, args...)
}

// relationReads returns the nodes of the plan of query that read relation,
// like ciReads.
func relationReads(t *testing.T, f *scopetest.Fixture, relation, query string, args ...any) []ciRead {
	t.Helper()
	var plan []byte
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan)
	}); err != nil {
		t.Fatalf("explain: %v\n%s", err, query)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	var out []ciRead
	var bitmapIndexes func(node map[string]any) []ciRead
	bitmapIndexes = func(node map[string]any) []ciRead {
		var reads []ciRead
		if name, ok := node["Index Name"].(string); ok {
			cond, _ := node["Index Cond"].(string)
			reads = append(reads, ciRead{"Bitmap Heap Scan", name, cond})
		}
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				reads = append(reads, bitmapIndexes(child)...)
			}
		}
		return reads
	}
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		nodeType, _ := node["Node Type"].(string)
		if node["Relation Name"] == relation {
			switch nodeType {
			case "Bitmap Heap Scan":
				children, _ := node["Plans"].([]any)
				for _, c := range children {
					if child, ok := c.(map[string]any); ok {
						out = append(out, bitmapIndexes(child)...)
					}
				}
				return
			default:
				index, _ := node["Index Name"].(string)
				cond, _ := node["Index Cond"].(string)
				out = append(out, ciRead{nodeType, index, cond})
			}
		}
		children, _ := node["Plans"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				walk(child)
			}
		}
	}
	for _, n := range nodes {
		if p, ok := n["Plan"].(map[string]any); ok {
			walk(p)
		}
	}
	return out
}
