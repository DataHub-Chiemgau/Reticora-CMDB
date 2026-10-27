package ci_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
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

// TestLinkingAssetWithoutLocationKeepsCILocation is the retest of F-239
// (WP-486; DB-05, AST-04, E-81 option 1) against PostgreSQL: linking a CI
// with a location to an asset without location used to set the CI's
// location to the asset's NULL (mirror trigger ci_mirror_asset_location),
// so the CI lost location, site and room and with them its scope. Now the
// asset takes the CI's location in the same transaction, audited on the
// asset, and the CI keeps location, site and room. An asset with a location
// keeps it and the CI takes it as before; a CI without location linked to
// an asset without location changes nothing.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLinkingAssetWithoutLocationKeepsCILocation(t *testing.T) {
	f := scopetest.Seed(t, "00")
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
	site := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "link-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "link-b1"})
	room1 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "link-r1"})
	room2 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "link-r2"})

	newAsset := func(tag, location string) string {
		t.Helper()
		var id string
		err := database.WithTenant(bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO asset (organization_id, asset_tag, name, location_id)
				VALUES ($1, $2, $2, NULLIF($3, '')::uuid) RETURNING id::text`, f.OrgA, tag, location).Scan(&id)
		})
		if err != nil {
			t.Fatalf("asset %s: %v", tag, err)
		}
		return id
	}
	assetLocation := func(id string) string {
		t.Helper()
		var loc string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, id).Scan(&loc); err != nil {
			t.Fatal(err)
		}
		return loc
	}
	assetAudits := func(id string) []string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT action || ' ' || COALESCE(changes -> 'after' ->> 'location_id', '') FROM audit_log
			WHERE organization_id = $1 AND resource_type = 'asset' AND resource_id = $2 ORDER BY seq`, f.OrgA, id)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	svc := ci.NewService(repo)
	manual := &ci.ManualChange{Author: f.User}
	newCI := func(name, location string) string {
		t.Helper()
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: serverType(t, f), Name: name, Status: "active",
			LocationID: location, Attributes: map[string]any{}}
		if err := svc.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}

	// A CI in room 1 linked to an asset without location keeps location,
	// site and room; the asset takes room 1, audited on the asset.
	placed := newCI("link-placed", room1)
	before, err := repo.GetByID(ctx, f.OrgA, placed)
	if err != nil || before.LocationID != room1 || before.SiteID != site || before.RoomID == "" {
		t.Fatalf("CI in room 1 before the link: %+v, %v", before, err)
	}
	roomID := before.RoomID
	unlocated := newAsset("LINK-NOLOC", "")
	got, err := svc.Update(ctx, f.OrgA, placed, ci.UpdateRequest{AssetID: &unlocated, Manual: manual})
	if err != nil {
		t.Fatalf("link CI in room 1 with an asset without location: %v", err)
	}
	if got.AssetID != unlocated || got.LocationID != room1 || got.SiteID != site || got.RoomID != roomID {
		t.Errorf("linked CI: asset %q location %q site %q room %q; want %s, %s, %s, %s",
			got.AssetID, got.LocationID, got.SiteID, got.RoomID, unlocated, room1, site, roomID)
	}
	if got.Asset == nil || got.Asset.LocationID != room1 {
		t.Errorf("asset facts on the linked CI: %+v, want location %s", got.Asset, room1)
	}
	if loc := assetLocation(unlocated); loc != room1 {
		t.Errorf("asset location after the link: %q, want the CI's %s", loc, room1)
	}
	if audits := assetAudits(unlocated); len(audits) != 1 || audits[0] != "asset.updated "+room1 {
		t.Errorf("audit entries on the asset: %q, want one asset.updated to %s", audits, room1)
	}
	// The stored row, read back under a principal restricted to client 1.
	if stored, readErr := repo.GetByID(f.ClientCtx(f.Client1), f.OrgA, placed); readErr != nil ||
		stored.LocationID != room1 || stored.SiteID != site || stored.RoomID != roomID {
		t.Errorf("stored CI for client 1: %+v, %v", stored, readErr)
	}
	// The asset is canonical from now on: moving it moves the CI.
	if err = database.WithTenant(bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `UPDATE asset SET location_id = $1 WHERE id = $2`, room2, unlocated)
		return execErr
	}); err != nil {
		t.Fatal(err)
	}
	if stored, _ := repo.GetByID(ctx, f.OrgA, placed); stored == nil || stored.LocationID != room2 {
		t.Errorf("CI after moving the asset: %+v, want location %s", stored, room2)
	}

	// An asset with a location keeps it; the CI takes it (unchanged).
	other := newCI("link-other", room1)
	located := newAsset("LINK-LOC", room2)
	if got, err = svc.Update(ctx, f.OrgA, other, ci.UpdateRequest{AssetID: &located, Manual: manual}); err != nil ||
		got.LocationID != room2 || assetLocation(located) != room2 {
		t.Errorf("link with a located asset: %+v, %v; want the CI at the asset's %s", got, err, room2)
	}
	if audits := assetAudits(located); len(audits) != 0 {
		t.Errorf("audit entries on the located asset: %q, want none", audits)
	}

	// A CI without location linked to an asset without location: both stay
	// without location, the asset is not written.
	bare := newCI("link-bare", "")
	empty := newAsset("LINK-EMPTY", "")
	if got, err = svc.Update(ctx, f.OrgA, bare, ci.UpdateRequest{AssetID: &empty, Manual: manual}); err != nil ||
		got.AssetID != empty || got.LocationID != "" || assetLocation(empty) != "" {
		t.Errorf("link without any location: %+v, %v", got, err)
	}
	if audits := assetAudits(empty); len(audits) != 0 {
		t.Errorf("audit entries on the asset without location: %q, want none", audits)
	}

	// A principal restricted to client 1 may read but not update an org-wide
	// asset: linking its CI with such an asset without location is refused
	// with 422 on asset_id instead of dropping the CI's location; neither
	// row changes.
	scoped := newCI("link-scoped", room1)
	shared := newAsset("LINK-SHARED", "")
	if _, err = svc.Update(f.ClientCtx(f.Client1), f.OrgA, scoped, ci.UpdateRequest{AssetID: &shared, Manual: manual}); !isValidation(err, "asset_id") {
		t.Errorf("client-scoped link with an org-wide asset without location: %v, want 422 on asset_id", err)
	}
	if stored, _ := repo.GetByID(ctx, f.OrgA, scoped); stored == nil || stored.AssetID != "" || stored.LocationID != room1 || assetLocation(shared) != "" {
		t.Errorf("after the refused client-scoped link: CI %+v, asset location %q", stored, assetLocation(shared))
	}
}

func isValidation(err error, field string) bool {
	var v *ci.ValidationError
	return errors.As(err, &v) && len(v.Violations) > 0 && v.Violations[0].Field == field
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
