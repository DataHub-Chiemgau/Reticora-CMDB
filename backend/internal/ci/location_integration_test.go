package ci_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestCILocationDerivation covers WP-054 (LOC-10, DB-05): ci.location_id
// references the canonical location tree; site_id and room_id are derived
// from it, follow a move of the location and cannot be written directly; a
// location of another client is rejected on location_id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCILocationDerivation(t *testing.T) {
	f := scopetest.Seed(t, "57")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	tree := locations.NewPGRepository(f.App)
	create := func(req locations.CreateRequest) *locations.Location {
		t.Helper()
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc
	}
	site := create(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "loc-s1"})
	building := create(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site.ID, Name: "loc-b1"})
	room := create(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building.ID, Name: "loc-r1"})
	rack := create(locations.CreateRequest{Kind: locations.KindRack, ParentID: room.ID, Name: "loc-k1"})
	warehouse := create(locations.CreateRequest{Kind: locations.KindWarehouse, ParentID: site.ID, Name: "loc-w1"})
	site2 := create(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "loc-s2"})
	building2 := create(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site2.ID, Name: "loc-b2"})
	foreign := create(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client2, Name: "loc-foreign"})

	repo := ci.NewPGRepository(f.App)
	ciID := f.CI(t, f.OrgA, f.Client1, "loc-ci")
	get := func() *ci.Item {
		t.Helper()
		item, err := repo.GetByID(ctx, f.OrgA, ciID)
		if err != nil {
			t.Fatalf("get ci: %v", err)
		}
		return item
	}
	setLocation := func(id string) (*ci.Item, error) {
		return repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{LocationID: &id})
	}

	// A rack: site and room derived from the tree.
	item, err := setLocation(rack.ID)
	if err != nil || item.LocationID != rack.ID || item.SiteID != site.ID || item.RoomID != room.ID {
		t.Fatalf("CI in rack: %+v, %v; want site %s room %s", item, err, site.ID, room.ID)
	}
	// A storage location has a site but no room.
	if item, err = setLocation(warehouse.ID); err != nil || item.SiteID != site.ID || item.RoomID != "" {
		t.Errorf("CI in warehouse: site %q room %q, %v", item.SiteID, item.RoomID, err)
	}
	if _, err = setLocation(rack.ID); err != nil {
		t.Fatal(err)
	}

	// Moving the room to a building of another site moves the CI with it.
	if _, err = tree.Move(ctx, f.OrgA, room.ID, building2.ID); err != nil {
		t.Fatalf("move room: %v", err)
	}
	if item = get(); item.SiteID != site2.ID || item.RoomID != room.ID {
		t.Errorf("CI after the room moved: site %s room %s, want %s %s", item.SiteID, item.RoomID, site2.ID, room.ID)
	}

	// site_id and room_id written directly are overwritten by the derivation.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET site_id = $2, room_id = NULL WHERE id = $1`, ciID, site.ID); err != nil {
		t.Fatalf("direct write: %v", err)
	}
	if item = get(); item.SiteID != site2.ID || item.RoomID != room.ID {
		t.Errorf("CI after a direct site write: site %s room %s, want the derived %s %s", item.SiteID, item.RoomID, site2.ID, room.ID)
	}

	// The API offers no site_id or room_id: a PATCH carrying them is refused.
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	for _, body := range []string{`{"site_id":"` + site.ID + `"}`, `{"room_id":"` + room.ID + `"}`} {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/cis/"+ciID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA})))
		if w.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s: status %d, want 400", body, w.Code)
		}
	}

	// A location of another client or an unknown one is a violation on
	// location_id; the CI keeps its location.
	for _, id := range []string{foreign.ID, f.ID()} {
		_, err = setLocation(id)
		ve, ok := ci.AsValidationError(err)
		if !ok || len(ve.Violations) != 1 || ve.Violations[0].Field != "location_id" {
			t.Errorf("location %s: %v, want a location_id violation", id, err)
		}
	}
	if item = get(); item.LocationID != rack.ID {
		t.Errorf("CI location after rejected writes: %s, want %s", item.LocationID, rack.ID)
	}

	// Clearing the location clears site and room.
	empty := ""
	if item, err = repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{LocationID: &empty}); err != nil || item.SiteID != "" || item.RoomID != "" {
		t.Errorf("CI without location: %+v, %v", item, err)
	}

	// A CI references location: deleting its location is refused.
	if _, err = setLocation(warehouse.ID); err != nil {
		t.Fatal(err)
	}
	if err = tree.Delete(ctx, f.OrgA, warehouse.ID); !errors.Is(err, locations.ErrInUse) {
		t.Errorf("delete a CI's location: %v, want ErrInUse", err)
	}
}

// TestMigration74MovesReferencesToLocation runs the real SQL of migration
// 000074 against references into location_node and checks the mapping, the
// derivation, the archive and the down migration. It works inside one
// transaction that is always rolled back: it first reverts 000074, seeds the
// side tree and its references, then migrates up and down again.
func TestMigration74MovesReferencesToLocation(t *testing.T) {
	f := scopetest.Seed(t, "58")
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/000074_ci_asset_location_fk." + name + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	up, down := read("up"), read("down")
	ctx := context.Background()
	roomID := f.Room(t, f.Client1, "mig74")
	var siteID string
	if err := f.Admin.QueryRow(ctx, `SELECT site_id::text FROM location WHERE id = $1`, roomID).Scan(&siteID); err != nil {
		t.Fatal(err)
	}

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
			t.Fatalf("%.60s...: %v", strings.TrimSpace(sql), execErr)
		}
	}
	exec(down)

	nSite, nRoom, nFloor, nDesk, nWh, nOrphan := f.ID(), f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	for _, n := range []struct{ id, parent, kind, site, room string }{
		{nSite, "", "site", siteID, ""},
		{nRoom, nSite, "room", "", roomID},
		{nFloor, nSite, "floor", "", ""},
		{nDesk, nFloor, "desk", "", ""},
		{nWh, nSite, "warehouse", "", ""},
		{nOrphan, "", "vehicle", "", ""},
	} {
		exec(`INSERT INTO location_node (id, organization_id, parent_id, node_type, name, site_id, room_id)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $4, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid)`,
			n.id, f.OrgA, n.parent, n.kind, n.site, n.room)
	}
	ciDesk, ciRoom, ciLegacy, ciOrphan, ciForeign := f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	for _, c := range []struct{ id, client, location, site, room string }{
		{ciDesk, f.Client1, nDesk, "", ""},
		{ciRoom, f.Client1, nRoom, "", ""},
		{ciLegacy, f.Client1, "", siteID, roomID},
		{ciOrphan, f.Client1, nOrphan, "", ""},
		{ciForeign, f.Client2, nRoom, siteID, ""},
	} {
		exec(`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, location_id, site_id, room_id)
			SELECT $1, $2, $3, id, 'mig74', 'active', NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid
			FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`,
			c.id, f.OrgA, c.client, c.location, c.site, c.room)
	}
	assetID, movementID, itemID := f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO asset (id, organization_id, client_id, asset_tag, name, location_id) VALUES ($1, $2, $3, 'mig74', 'mig74', $4)`,
		assetID, f.OrgA, f.Client1, nWh)
	exec(`INSERT INTO asset_movement (id, organization_id, item_kind, asset_id, movement_type, from_location_id, to_location_id)
		VALUES ($1, $2, 'asset', $3, 'transfer', $4, $5)`, movementID, f.OrgA, assetID, nDesk, nWh)
	exec(`INSERT INTO quantity_item (id, organization_id, client_id, name, location_id) VALUES ($1, $2, $3, 'mig74', $4)`,
		itemID, f.OrgA, f.Client1, nFloor)

	exec(up)

	type ciRow struct{ location, site, room string }
	readCI := func(id string) ciRow {
		t.Helper()
		var r ciRow
		if scanErr := tx.QueryRow(ctx, `SELECT COALESCE(location_id::text, ''), COALESCE(site_id::text, ''), COALESCE(room_id::text, '') FROM ci WHERE id = $1`, id).
			Scan(&r.location, &r.site, &r.room); scanErr != nil {
			t.Fatal(scanErr)
		}
		return r
	}
	for _, c := range []struct {
		name string
		id   string
		want ciRow
	}{
		{"desk below a floor maps to the site", ciDesk, ciRow{siteID, siteID, ""}},
		{"room node maps to the room", ciRoom, ciRow{roomID, siteID, roomID}},
		{"legacy room becomes the location", ciLegacy, ciRow{roomID, siteID, roomID}},
		{"unmapped node is cleared", ciOrphan, ciRow{"", "", ""}},
		{"location of another client is dropped", ciForeign, ciRow{"", "", ""}},
	} {
		if got := readCI(c.id); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	var reason string
	if err = tx.QueryRow(ctx, `SELECT reason FROM location_ref_migration WHERE row_id = $1 AND column_name = 'location_id'`, ciForeign).Scan(&reason); err != nil || reason != "client_mismatch" {
		t.Errorf("record of the foreign CI: %q, %v", reason, err)
	}
	var assetLoc, from, to, stockLoc string
	if err = tx.QueryRow(ctx, `SELECT a.location_id::text, m.from_location_id::text, m.to_location_id::text, q.location_id::text
		FROM asset a, asset_movement m, quantity_item q WHERE a.id = $1 AND m.id = $2 AND q.id = $3`, assetID, movementID, itemID).
		Scan(&assetLoc, &from, &to, &stockLoc); err != nil {
		t.Fatal(err)
	}
	if assetLoc != nWh || from != siteID || to != nWh || stockLoc != siteID {
		t.Errorf("asset %s, movement %s→%s, stock %s; want imported warehouse, site→warehouse, site", assetLoc, from, to, stockLoc)
	}
	var retired int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM location_node_retired WHERE organization_id = $1`, f.OrgA).Scan(&retired); err != nil || retired != 6 {
		t.Errorf("archived nodes: %d, %v; want 6", retired, err)
	}
	var sideTree *string
	if err = tx.QueryRow(ctx, `SELECT to_regclass('location_node')::text`).Scan(&sideTree); err != nil || sideTree != nil {
		t.Errorf("location_node after up: %v, %v; want dropped", sideTree, err)
	}

	// Separate transactions in production: run the deferred checks of the
	// up part before the down part alters the tables.
	exec(`SET CONSTRAINTS ALL IMMEDIATE`)
	exec(down)

	for _, c := range []struct {
		id   string
		want ciRow
	}{
		{ciDesk, ciRow{nDesk, "", ""}},
		{ciRoom, ciRow{nRoom, "", ""}},
		{ciLegacy, ciRow{"", siteID, roomID}},
		{ciOrphan, ciRow{nOrphan, "", ""}},
		{ciForeign, ciRow{nRoom, siteID, ""}},
	} {
		if got := readCI(c.id); got != c.want {
			t.Errorf("CI %s after down: %+v, want %+v", c.id, got, c.want)
		}
	}
	var parent string
	if err = tx.QueryRow(ctx, `SELECT parent_id::text FROM location_node WHERE id = $1`, nDesk).Scan(&parent); err != nil || parent != nFloor {
		t.Errorf("restored desk node parent: %q, %v; want %s", parent, err, nFloor)
	}
	if err = tx.QueryRow(ctx, `SELECT m.from_location_id::text, q.location_id::text FROM asset_movement m, quantity_item q WHERE m.id = $1 AND q.id = $2`,
		movementID, itemID).Scan(&from, &stockLoc); err != nil || from != nDesk || stockLoc != nFloor {
		t.Errorf("movement from %s, stock %s after down, %v; want %s, %s", from, stockLoc, err, nDesk, nFloor)
	}
}
