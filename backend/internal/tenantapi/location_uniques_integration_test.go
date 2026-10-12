package tenantapi_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// TestLocationUniquesAndHeightUnitsOnPostgreSQL covers WP-108 (LOC-04 –
// LOC-06) against PostgreSQL:
//   - building names are unique per site, room names per building, rack
//     names per room: a duplicate (also by rename) is a conflict on "name",
//     the same name under another parent is allowed;
//   - the rack height is stored as height_units with default 42 and range
//     1–60 (field error outside it);
//   - migration 000102 suffixes existing duplicates before the unique keys.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationUniquesAndHeightUnitsOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "85")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	locs := tenantapi.NewPGRepository(f.App)
	racks := rack.NewPGRepository(f.App)
	var conflict *api.ConflictError
	isNameConflict := func(err error) bool { return errors.As(err, &conflict) && conflict.Field == "name" }
	str := func(s string) *string { return &s }

	site1 := &tenantapi.Site{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "site-108-1"}
	site2 := &tenantapi.Site{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "site-108-2"}
	for _, s := range []*tenantapi.Site{site1, site2} {
		if err := locs.CreateSite(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	// LOC-04: buildings per site.
	b1 := &tenantapi.Building{OrganizationID: f.OrgA, SiteID: site1.ID, Name: "Main"}
	if err := locs.CreateBuilding(ctx, b1); err != nil {
		t.Fatal(err)
	}
	if err := locs.CreateBuilding(ctx, &tenantapi.Building{OrganizationID: f.OrgA, SiteID: site1.ID, Name: "Main"}); !isNameConflict(err) {
		t.Errorf("duplicate building per site: %v", err)
	}
	b2 := &tenantapi.Building{OrganizationID: f.OrgA, SiteID: site2.ID, Name: "Main"}
	if err := locs.CreateBuilding(ctx, b2); err != nil {
		t.Errorf("same building name at another site: %v", err)
	}
	annex := &tenantapi.Building{OrganizationID: f.OrgA, SiteID: site1.ID, Name: "Annex"}
	if err := locs.CreateBuilding(ctx, annex); err != nil {
		t.Fatal(err)
	}
	if _, err := locs.UpdateBuilding(ctx, f.OrgA, annex.ID, tenantapi.UpdateBuildingRequest{Name: str("Main")}); !isNameConflict(err) {
		t.Errorf("rename building onto an existing name: %v", err)
	}

	// LOC-05: rooms per building.
	r1 := &tenantapi.Room{OrganizationID: f.OrgA, BuildingID: b1.ID, Name: "DC-1"}
	if err := locs.CreateRoom(ctx, r1); err != nil {
		t.Fatal(err)
	}
	if err := locs.CreateRoom(ctx, &tenantapi.Room{OrganizationID: f.OrgA, BuildingID: b1.ID, Name: "DC-1"}); !isNameConflict(err) {
		t.Errorf("duplicate room per building: %v", err)
	}
	r2 := &tenantapi.Room{OrganizationID: f.OrgA, BuildingID: b2.ID, Name: "DC-1"}
	if err := locs.CreateRoom(ctx, r2); err != nil {
		t.Errorf("same room name in another building: %v", err)
	}

	// LOC-06: racks per room, height_units.
	rk := &rack.Rack{OrganizationID: f.OrgA, RoomID: r1.ID, Name: "A01", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}
	if err := racks.CreateRack(ctx, rk); err != nil {
		t.Fatal(err)
	}
	if err := racks.CreateRack(ctx, &rack.Rack{OrganizationID: f.OrgA, RoomID: r1.ID, Name: "A01", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}); !isNameConflict(err) {
		t.Errorf("duplicate rack per room: %v", err)
	}
	if err := racks.CreateRack(ctx, &rack.Rack{OrganizationID: f.OrgA, RoomID: r2.ID, Name: "A01", HeightUnits: 42, WidthMM: 600, DepthMM: 1000}); err != nil {
		t.Errorf("same rack name in another room: %v", err)
	}
	var fieldErr *api.FieldError
	if err := racks.CreateRack(ctx, &rack.Rack{OrganizationID: f.OrgA, RoomID: r1.ID, Name: "A02", HeightUnits: 0, WidthMM: 600, DepthMM: 1000}); !errors.As(err, &fieldErr) || fieldErr.Field != "height_units" {
		t.Errorf("height_units 0 stored: %v", err)
	}
	var height int
	var defaultHeight string
	if err := f.Admin.QueryRow(bg, `SELECT height_units FROM rack WHERE id = $1`, rk.ID).Scan(&height); err != nil || height != 42 {
		t.Errorf("height_units: %d %v", height, err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT column_default FROM information_schema.columns WHERE table_name = 'rack' AND column_name = 'height_units'`).Scan(&defaultHeight); err != nil || defaultHeight != "42" {
		t.Errorf("height_units default: %q %v", defaultHeight, err)
	}

	// Migration 000102 suffixes existing duplicates.
	raw, err := os.ReadFile("../../migrations/000102_location_uniques_height_units.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "UPDATE building b SET name")
	rename := text[start : start+strings.Index(text[start:], ";")]
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, `ALTER TABLE building DROP CONSTRAINT building_site_name_key`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, `UPDATE building SET name = 'Main' WHERE id = $1`, annex.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, rename); err != nil {
		t.Fatal(err)
	}
	var kept, renamed string
	if err = tx.QueryRow(bg, `SELECT (SELECT name FROM building WHERE id = $1), (SELECT name FROM building WHERE id = $2)`, b1.ID, annex.ID).Scan(&kept, &renamed); err != nil ||
		kept != "Main" || renamed != "Main ("+annex.ID[:8]+")" {
		t.Errorf("migrated building names: %q %q %v", kept, renamed, err)
	}
}
