package ci_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/jackc/pgx/v5"
)

// TestAssetIsCanonicalForLinkedCIs covers WP-077 (DB-05, CH12, AST-04)
// against PostgreSQL:
//   - a CI linked to an asset (ci.asset_id) shows serial number, location,
//     physical lifecycle and inventory data of the asset read-only;
//   - ci.location_id mirrors asset.location_id and follows a move of the
//     asset; a direct write of the location is rejected (422 on location_id);
//   - ci.serial_number stays the observed value; a difference to the asset
//     opens exactly one open review item serial_mismatch; a manual serial
//     write on a linked CI is rejected;
//   - asset.ci_id (the primary CI) links that CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetIsCanonicalForLinkedCIs(t *testing.T) {
	f := scopetest.Seed(t, "68")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	tree := locations.NewPGRepository(f.App)
	newLocation := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	site := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "canon-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "canon-b1"})
	room1 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "canon-r1"})
	room2 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "canon-r2"})

	newAsset := func(tag, serial, location, primaryCI string) string {
		t.Helper()
		var id string
		err := database.WithTenant(bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO asset (organization_id, asset_tag, name, serial_number, location_id, ci_id,
				lifecycle_state, barcode, supplier, warranty_end)
				VALUES ($1, $2, $2, $3, $4, NULLIF($5, '')::uuid, 'in_use', 'BC-' || $2, 'Acme', '2030-01-31') RETURNING id::text`,
				f.OrgA, tag, serial, location, primaryCI).Scan(&id)
		})
		if err != nil {
			t.Fatalf("asset %s: %v", tag, err)
		}
		return id
	}
	assetExec := func(sql string, args ...any) {
		t.Helper()
		err := database.WithTenant(bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	openMismatches := func(ciID string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item
			WHERE kind = 'serial_mismatch' AND status = 'open' AND payload ->> 'ci_id' = $1`, ciID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	isValidation := func(err error, field string) bool {
		var v *ci.ValidationError
		return errors.As(err, &v) && len(v.Violations) > 0 && v.Violations[0].Field == field
	}

	repo := ci.NewPGRepository(f.App)
	svc := ci.NewService(repo)
	assetA := newAsset("CANON-A", "SN-A", room1, "")

	// A linked CI takes the asset's location and shows its data read-only.
	linked := &ci.Item{OrganizationID: f.OrgA, CITypeID: serverType(t, f), Name: "canon-ci", Status: "active",
		SerialNumber: "SN-A", AssetID: assetA, Attributes: map[string]any{}}
	if err := svc.Create(ctx, linked); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, f.OrgA, linked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AssetID != assetA || got.LocationID != room1 || got.SiteID != site || got.Asset == nil {
		t.Fatalf("linked CI %+v", got)
	}
	if a := got.Asset; a.SerialNumber != "SN-A" || a.LocationID != room1 || a.LifecycleState != "in_use" ||
		a.Barcode != "BC-CANON-A" || a.Supplier != "Acme" || a.WarrantyEnd != "2030-01-31" || a.AssetTag != "CANON-A" {
		t.Errorf("asset facts %+v", a)
	}
	if openMismatches(linked.ID) != 0 {
		t.Error("matching serial numbers opened a review")
	}

	// Creating a linked CI with its own location is rejected.
	other := &ci.Item{OrganizationID: f.OrgA, CITypeID: serverType(t, f), Name: "canon-ci-loc", Status: "active",
		AssetID: assetA, LocationID: room2, Attributes: map[string]any{}}
	if err = svc.Create(ctx, other); !isValidation(err, "location_id") {
		t.Errorf("create linked CI with a location: %v, want 422 on location_id", err)
	}

	// Location writes on a linked CI: manual (service) and direct (database).
	manual := &ci.ManualChange{Author: f.User}
	if _, err = svc.Update(ctx, f.OrgA, linked.ID, ci.UpdateRequest{LocationID: &room2, Manual: manual}); !isValidation(err, "location_id") {
		t.Errorf("manual location write: %v, want 422 on location_id", err)
	}
	if _, err = repo.Update(ctx, f.OrgA, linked.ID, ci.UpdateRequest{LocationID: &room2}); !isValidation(err, "location_id") {
		t.Errorf("direct location write: %v, want 422 on location_id", err)
	}

	// Moving the asset moves the CI.
	assetExec(`UPDATE asset SET location_id = $1 WHERE id = $2`, room2, assetA)
	if got, _ = repo.GetByID(ctx, f.OrgA, linked.ID); got.LocationID != room2 || got.Asset.LocationID != room2 {
		t.Errorf("CI after moving the asset: location %s, want %s", got.LocationID, room2)
	}

	// The observed serial number stays writable for discovery; a mismatch
	// opens exactly one review.
	observed := "SN-OBSERVED"
	if got, err = repo.Update(ctx, f.OrgA, linked.ID, ci.UpdateRequest{SerialNumber: &observed}); err != nil || got.SerialNumber != observed {
		t.Fatalf("observed serial write: %v", err)
	}
	other2 := "SN-OBSERVED-2"
	if _, err = repo.Update(ctx, f.OrgA, linked.ID, ci.UpdateRequest{SerialNumber: &other2}); err != nil {
		t.Fatal(err)
	}
	if n := openMismatches(linked.ID); n != 1 {
		t.Errorf("open serial_mismatch reviews: %d, want 1", n)
	}
	if got, _ = repo.GetByID(ctx, f.OrgA, linked.ID); got.SerialNumber != other2 || got.Asset.SerialNumber != "SN-A" {
		t.Errorf("serial numbers: observed %q, asset %q", got.SerialNumber, got.Asset.SerialNumber)
	}
	if _, err = svc.Update(ctx, f.OrgA, linked.ID, ci.UpdateRequest{SerialNumber: &observed, Manual: manual}); !isValidation(err, "serial_number") {
		t.Errorf("manual serial write on a linked CI: %v, want 422 on serial_number", err)
	}

	// asset.ci_id links its primary CI; unlinking keeps the last location.
	plainID := f.CI(t, f.OrgA, "", "canon-primary")
	assetB := newAsset("CANON-B", "SN-B", room1, plainID)
	if got, _ = repo.GetByID(ctx, f.OrgA, plainID); got.AssetID != assetB || got.LocationID != room1 {
		t.Errorf("primary CI of asset B: asset %q, location %q", got.AssetID, got.LocationID)
	}
	unlink := ""
	if got, err = svc.Update(ctx, f.OrgA, plainID, ci.UpdateRequest{AssetID: &unlink, LocationID: &room2, Manual: manual}); err != nil ||
		got.AssetID != "" || got.LocationID != room2 || got.Asset != nil {
		t.Errorf("unlink with a new location: %+v, %v", got, err)
	}
}

func scopePtr(orgID, userID string) *database.TenantScope {
	scope := database.OrgWideScope(orgID, userID)
	return &scope
}

func serverType(t *testing.T, f *scopetest.Fixture) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(),
		`SELECT id::text FROM ci_type WHERE organization_id IS NULL AND name = 'server'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
