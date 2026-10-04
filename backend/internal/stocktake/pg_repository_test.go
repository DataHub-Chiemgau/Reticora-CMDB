package stocktake_test

import (
	"context"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
)

func pageParams() api.PaginationParams {
	return api.PaginationParams{Limit: 50}
}

// These tests run against the migrated CI PostgreSQL (see the migrations CI
// job). They are skipped locally unless TEST_DATABASE_URL is set.
func testPool(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

// TestCompleteAppliesCorrections verifies the atomic completion flow against
// a real database: corrections land on the asset table and the stocktake
// transitions exactly once.
func TestCompleteAppliesCorrections(t *testing.T) {
	dsn := testPool(t)
	ctx := context.Background()
	// The fixture creates its own organization; see the relationship
	// integration test for why a maintenance pool is used.
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	orgID := "00000000-0000-0000-0000-000000000036"
	userID := seedOrgAndUser(t, dsn, orgID)
	// The repositories take the tenant scope from the request context.
	scope := database.OrgWideScope(orgID, userID)
	ctx = database.ContextWithTenantScope(ctx, &scope)

	stRepo := stocktake.NewPGRepository(pool)
	assetRepo := asset.NewPGRepository(pool)

	missing := &asset.Asset{OrganizationID: orgID, AssetTag: "PG-MISS", Name: "missing switch", Category: "hardware", Status: "assigned", Location: "Raum A"}
	wrongLoc := &asset.Asset{OrganizationID: orgID, AssetTag: "PG-WL", Name: "misplaced server", Category: "hardware", Status: "assigned", Location: "Raum A"}
	for _, a := range []*asset.Asset{missing, wrongLoc} {
		if err := assetRepo.Create(ctx, a); err != nil {
			t.Fatalf("create asset: %v", err)
		}
	}

	st := &stocktake.Stocktake{OrganizationID: orgID, Title: "PG completion", Status: "in_progress", Scope: "full", StartedBy: userID}
	if err := stRepo.Create(ctx, st); err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	for _, scan := range []*stocktake.StockScan{
		{OrganizationID: orgID, StocktakeID: st.ID, AssetID: missing.ID, ScannedBy: userID, ScanMethod: "manual", ScanResult: "missing"},
		{OrganizationID: orgID, StocktakeID: st.ID, AssetID: wrongLoc.ID, ScannedBy: userID, ScanMethod: "barcode", ScanResult: "wrong_location", LocationFound: "Raum B"},
	} {
		if err := stRepo.AddScan(ctx, scan); err != nil {
			t.Fatalf("add scan: %v", err)
		}
	}

	// Difference list must join the assets.
	entries, total, err := stRepo.Difference(ctx, orgID, st.ID, pageParams())
	if err != nil {
		t.Fatalf("difference: %v", err)
	}
	if total != 2 || len(entries) != 2 {
		t.Fatalf("expected 2 difference entries, got total=%d len=%d", total, len(entries))
	}
	for _, e := range entries {
		if e.Asset == nil || e.Asset.AssetTag == "" {
			t.Fatalf("expected joined asset on entry %#v", e)
		}
	}

	completion, err := stRepo.Complete(ctx, orgID, st.ID, true)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completion.Stocktake.Status != "completed" || completion.CorrectionsApplied != 2 {
		t.Fatalf("unexpected completion: %#v", completion)
	}

	gotMissing, err := assetRepo.GetByID(ctx, orgID, missing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMissing.Status != "lost" {
		t.Fatalf("missing asset should be lost, got %q", gotMissing.Status)
	}
	gotWL, err := assetRepo.GetByID(ctx, orgID, wrongLoc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotWL.Location != "Raum B" || gotWL.Status != "assigned" {
		t.Fatalf("wrong_location asset should have moved to Raum B: %#v", gotWL)
	}

	if _, err := stRepo.Complete(ctx, orgID, st.ID, true); err == nil {
		t.Fatal("second completion must fail")
	}
}

// seedOrgAndUser creates the tenant fixture rows the stocktake tables
// reference and returns the user id. Leftovers from previous runs are
// removed first so the test is idempotent.
func seedOrgAndUser(t *testing.T, dsn, orgID string) string {
	t.Helper()
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	cleanup := func() {
		c := context.Background()
		// organization lacks cascading deletes for asset/stocktake, so remove
		// dependent rows explicitly first.
		_, _ = db.ExecContext(c, `DELETE FROM stocktake WHERE organization_id = $1`, orgID)
		_, _ = db.ExecContext(c, `DELETE FROM asset WHERE organization_id = $1`, orgID)
		_, _ = db.ExecContext(c, `DELETE FROM app_user WHERE organization_id = $1`, orgID)
		_, _ = db.ExecContext(c, `DELETE FROM organization WHERE id = $1`, orgID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO organization (id, name, slug) VALUES ($1, 'ST Org', 'st-org-36')`, orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	var userID string
	err = db.QueryRowContext(ctx,
		`INSERT INTO app_user (organization_id, email, display_name)
		 VALUES ($1, 'st-tester-36@example.com', 'Stocktake Tester')
		 RETURNING id::text`, orgID).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return userID
}
