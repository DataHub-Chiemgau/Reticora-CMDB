package ci_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestDeleteDecommissionsAndHardDeletes covers CI-05 and LCY-01 (WP-170)
// against PostgreSQL:
//   - DELETE sets deleted_at and the operative status decommissioned, the
//     lifecycle state stays, the asset link is released (ci.asset_id and
//     the primary asset.ci_id) and the relationships stay;
//   - a restore brings back the previous status and the asset link;
//   - after the retention the hard delete removes the CI with its
//     interfaces and their IP addresses; the asset stays.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeleteDecommissionsAndHardDeletes(t *testing.T) {
	f := scopetest.Seed(t, "b0")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))).RegisterRoutes(mux)
	call := func(method, path string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(nil)).WithContext(ctx))
		return w.Code
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	id := f.CI(t, f.OrgA, f.Client1, "delete-1")
	peer := f.CI(t, f.OrgA, f.Client1, "delete-peer")
	asset := f.Asset(t, f.OrgA, f.Client1, "delete-asset")
	exec(`UPDATE asset SET ci_id = $2 WHERE id = $1`, asset, id)
	exec(`UPDATE ci SET status = 'maintenance' WHERE id = $1`, id)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source) VALUES ($1, $2, $3, 'connected_to', 'manual')`, f.OrgA, id, peer)
	var ifaceID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`, f.OrgA, id).Scan(&ifaceID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ip_address (organization_id, interface_id, address, vrf_id)
		VALUES ($1, $2, '192.0.2.170', (SELECT id FROM vrf WHERE organization_id = $1 AND is_default))`, f.OrgA, ifaceID)

	state := func() (deleted bool, status, lifecycle, ciAsset, assetCI string, rels int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT c.deleted_at IS NOT NULL, c.status, COALESCE(c.lifecycle_state, ''), COALESCE(c.asset_id::text, ''),
				COALESCE((SELECT a.ci_id::text FROM asset a WHERE a.id = $2), ''),
				(SELECT count(*) FROM ci_relationship r WHERE r.source_ci_id = c.id)
			FROM ci c WHERE c.id = $1`, id, asset).Scan(&deleted, &status, &lifecycle, &ciAsset, &assetCI, &rels); err != nil {
			t.Fatal(err)
		}
		return
	}

	_, _, lifecycleBefore, _, _, _ := state()
	if code := call(http.MethodDelete, "/api/v1/cis/"+id); code >= 300 {
		t.Fatalf("delete: %d", code)
	}
	if deleted, status, lifecycle, ciAsset, assetCI, rels := state(); !deleted || status != "decommissioned" || lifecycle != lifecycleBefore ||
		ciAsset != "" || assetCI != "" || rels != 1 {
		t.Errorf("after delete: deleted %v, status %s, lifecycle %q, ci.asset_id %q, asset.ci_id %q, relationships %d",
			deleted, status, lifecycle, ciAsset, assetCI, rels)
	}

	if code := call(http.MethodPost, "/api/v1/cis/"+id+"/restore"); code != http.StatusOK {
		t.Fatalf("restore: %d", code)
	}
	if deleted, status, _, ciAsset, assetCI, _ := state(); deleted || status != "maintenance" || ciAsset != asset || assetCI != id {
		t.Errorf("after restore: deleted %v, status %s, ci.asset_id %q, asset.ci_id %q", deleted, status, ciAsset, assetCI)
	}

	call(http.MethodDelete, "/api/v1/cis/"+id)
	exec(`UPDATE ci SET deleted_at = now() - interval '100 days' WHERE id = $1`, id)
	retention := jobs.NewRetention(f.Admin, jobs.RetentionFromDays(10, 3*365, 90, 365))
	if err := retention.RunSoftDeleted(bg); err != nil {
		t.Fatal(err)
	}
	var cis, ifaces, ips, assets int
	if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM ci WHERE id = $1),
			(SELECT count(*) FROM network_interface WHERE id = $2),
			(SELECT count(*) FROM ip_address WHERE address = '192.0.2.170' AND organization_id = $4),
			(SELECT count(*) FROM asset WHERE id = $3)`, id, ifaceID, asset, f.OrgA).Scan(&cis, &ifaces, &ips, &assets); err != nil {
		t.Fatal(err)
	}
	if cis != 0 || ifaces != 0 || ips != 0 || assets != 1 {
		t.Errorf("after the hard delete: CI %d, interfaces %d, IPs %d, asset %d", cis, ifaces, ips, assets)
	}
}
