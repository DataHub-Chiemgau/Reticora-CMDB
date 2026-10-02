package stocktake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
)

// TestStocktakeRepositoryClientScope runs the stocktake repository with the
// principal's tenant scope (TEN-06, WP-018): in an organization-wide
// stocktake a principal restricted to client 1 neither sees scans or
// differences of client-2 assets nor scans them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-foreign")
	user := f.AppUser(t, f.OrgA, "counter")

	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: "Inventur", Status: "in_progress", Scope: "full"}
	if err := repo.Create(orgCtx, st); err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	scan := func(assetID string) *stocktake.StockScan {
		return &stocktake.StockScan{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: assetID, ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"}
	}
	if err := repo.AddScan(orgCtx, scan(foreign)); err != nil {
		t.Fatalf("org-wide scan: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.AddScan(ctx, scan(foreign)); err == nil {
		t.Fatal("client-1 principal scanned a client-2 asset")
	}
	if err := repo.AddScan(ctx, scan(own)); err != nil {
		t.Fatalf("client-1 scan of own asset: %v", err)
	}
	page := api.PaginationParams{Limit: 100}
	scans, total, err := repo.ListScans(ctx, f.OrgA, st.ID, page)
	if err != nil || total != 1 || len(scans) != 1 || scans[0].AssetID != own {
		t.Fatalf("client-1 scans: total=%d %+v err=%v, want only the own one", total, scans, err)
	}
	diff, total, err := repo.Difference(ctx, f.OrgA, st.ID, page)
	if err != nil || total != 1 || len(diff) != 1 || diff[0].Scan.AssetID != own {
		t.Fatalf("client-1 difference: total=%d %+v err=%v, want only the own asset", total, diff, err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM stock_scan WHERE asset_id = $1`, foreign).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 scans: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.ListScans(context.Background(), f.OrgA, st.ID, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
