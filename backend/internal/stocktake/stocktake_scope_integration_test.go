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

// TestStocktakeForeignClientCorrectionAndComplete covers the completion of a
// stocktake with inventory corrections (TEN-06, WP-537). Stocktakes are
// organization-wide, their corrections write the scanned assets: when a
// principal restricted to client 1 completes a stocktake with differences of
// a client-1 and a client-2 asset, only the client-1 asset is corrected; the
// client-2 asset keeps status and location and appears in no correction. A
// stocktake of another organization the principal can neither complete nor
// update (ErrNotFound), and it stays unchanged.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeForeignClientCorrectionAndComplete(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-foreign")
	moved := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-moved")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location = 'rack-a' WHERE id = ANY($1::uuid[])`, []string{own, foreign, moved}); err != nil {
		t.Fatalf("asset locations: %v", err)
	}
	user := f.AppUser(t, f.OrgA, "counter")

	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: "Inventur", Status: "in_progress", Scope: "full"}
	if err := repo.Create(orgCtx, st); err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	for _, s := range []*stocktake.StockScan{
		{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: own, ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"},
		{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: foreign, ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"},
		{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: moved, ScannedBy: user, ScanMethod: "manual", ScanResult: "wrong_location", LocationFound: "rack-z"},
	} {
		if err := repo.AddScan(orgCtx, s); err != nil {
			t.Fatalf("org-wide scan of %s: %v", s.AssetID, err)
		}
	}
	stB := &stocktake.Stocktake{OrganizationID: f.OrgB, Title: "Inventur B", Status: "in_progress", Scope: "full"}
	if err := repo.Create(f.OrgCtx(f.OrgB), stB); err != nil {
		t.Fatalf("create stocktake of organization B: %v", err)
	}
	type assetRow struct{ status, location string }
	asset := func(id string) assetRow {
		var r assetRow
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(location, '') FROM asset WHERE id = $1`, id).Scan(&r.status, &r.location); err != nil {
			t.Fatalf("stored asset %s: %v", id, err)
		}
		return r
	}
	type stocktakeRow struct {
		title, status string
		completed     bool
	}
	stored := func(id string) stocktakeRow {
		var r stocktakeRow
		if err := f.Admin.QueryRow(bg, `SELECT title, status, completed_at IS NOT NULL FROM stocktake WHERE id = $1`, id).Scan(&r.title, &r.status, &r.completed); err != nil {
			t.Fatalf("stored stocktake %s: %v", id, err)
		}
		return r
	}
	untouched := assetRow{status: "in_stock", location: "rack-a"}

	ctx := f.ClientCtx(f.Client1)
	completion, err := repo.Complete(ctx, f.OrgA, st.ID, true)
	if err != nil {
		t.Fatalf("client-1 completion of the stocktake: %v", err)
	}
	for _, c := range completion.Corrections {
		if c.AssetID == foreign || c.AssetID == moved {
			t.Errorf("client-1 completion corrected client-2 asset %s: %+v", c.AssetID, c)
		}
	}
	for _, id := range []string{foreign, moved} {
		if got := asset(id); got != untouched {
			t.Errorf("client-2 asset %s after client-1 completion: %+v, want %+v", id, got, untouched)
		}
	}
	// Positive control: the client-1 asset is corrected.
	if completion.CorrectionsApplied != 1 || len(completion.Corrections) != 1 || completion.Corrections[0].AssetID != own {
		t.Errorf("client-1 completion: %d applied %+v, want exactly the own asset", completion.CorrectionsApplied, completion.Corrections)
	}
	if got := asset(own); got != (assetRow{status: "lost", location: "rack-a"}) {
		t.Errorf("client-1 asset after completion: %+v, want lost at rack-a", got)
	}
	if got := stored(st.ID); got.status != "completed" || !got.completed {
		t.Errorf("stocktake after client-1 completion: %+v, want completed", got)
	}

	// A stocktake of another organization is not found for completion or
	// update and stays unchanged.
	wantB := stored(stB.ID)
	if _, err := repo.Complete(ctx, f.OrgA, stB.ID, true); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("completion of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	title, status := "hijacked", "cancelled"
	if _, err := repo.Update(ctx, f.OrgA, stB.ID, stocktake.UpdateRequest{Title: &title, Status: &status}); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("update of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	if got := stored(stB.ID); got != wantB {
		t.Errorf("organization-B stocktake after foreign writes: %+v, want %+v", got, wantB)
	}
}
