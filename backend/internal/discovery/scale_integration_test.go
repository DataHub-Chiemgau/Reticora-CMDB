package discovery_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestReconciliationBeyondTenThousandCIs covers NFR-01 (WP-205) against
// PostgreSQL: with more than 10,000 CIs the identity search finds the newest
// CI by serial number, returns every match of a hostname shared by more CIs
// than one page holds (no upper bound, including CIs with the same
// created_at) and finds an edge of a source with more than 1,000 edges by
// index lookup.
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
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, serial_number, hostname, created_at)
		SELECT $1, $2, 'scale-' || n, 'active', 'SCALE-' || n,
			CASE WHEN n <= $4 THEN 'shared-host' ELSE 'scale-host-' || n END,
			CASE WHEN n <= $4 THEN now() ELSE now() - make_interval(secs => $3 - n) END
		FROM generate_series(1, $3) AS n`, f.OrgA, serverType, inventory, shared); err != nil {
		t.Fatal(err)
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
}
