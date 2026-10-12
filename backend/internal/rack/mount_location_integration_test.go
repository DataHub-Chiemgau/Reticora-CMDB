package rack_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/jackc/pgx/v5"
)

// TestMountSetsLocationWithHistory covers WP-078 (RCK-03, DB-05) against
// PostgreSQL: mounting sets the CI's location to the rack, moving to another
// rack follows, unmounting sets the rack's room; for a CI linked to an asset
// the asset's location is set and mirrored to the CI. Every change writes
// ci_change (movement, location_change) and an audit entry in the same
// transaction.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMountSetsLocationWithHistory(t *testing.T) {
	f := scopetest.Seed(t, "69")
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
	site := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "mount-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "mount-b1"})
	room := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "mount-r1"})
	rack1 := newLocation(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "mount-k1"})
	rack2 := newLocation(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "mount-k2"})

	repo := rack.NewPGRepository(f.App)
	locationOf := func(ciID string) string {
		t.Helper()
		var loc string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM ci WHERE id = $1`, ciID).Scan(&loc); err != nil {
			t.Fatal(err)
		}
		return loc
	}
	history := func(ciID string) (changes, audits []string) {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT change_type || ':' || COALESCE(field_name, '') FROM ci_change
			WHERE ci_id = $1 AND change_type IN ('movement', 'location_change') ORDER BY created_at`, ciID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			changes = append(changes, c)
		}
		rows.Close()
		rows, err = f.Admin.Query(bg, `SELECT action FROM audit_log WHERE resource_id = $1 AND action LIKE 'ci.rack_%' ORDER BY timestamp`, ciID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a string
			_ = rows.Scan(&a)
			audits = append(audits, a)
		}
		rows.Close()
		return changes, audits
	}

	// A CI without an asset: mount, reposition, move, unmount.
	ciID := f.CI(t, f.OrgA, f.Client1, "mount-ci")
	m := &rack.RackMount{OrganizationID: f.OrgA, RackID: rack1, CIID: ciID, PositionU: 1, HeightU: 2, Face: "front"}
	if err := repo.CreateMount(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != rack1 {
		t.Fatalf("location after mount: %q, want rack %q", got, rack1)
	}
	pos := 10
	if _, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{PositionU: &pos}); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != rack1 {
		t.Errorf("location after repositioning: %q", got)
	}
	if _, err := repo.UpdateMount(ctx, f.OrgA, m.ID, rack.UpdateMountRequest{RackID: &rack2}); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != rack2 {
		t.Errorf("location after moving to rack 2: %q, want %q", got, rack2)
	}
	if err := repo.DeleteMount(ctx, f.OrgA, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := locationOf(ciID); got != room {
		t.Errorf("location after unmount: %q, want the room %q", got, room)
	}
	changes, audits := history(ciID)
	// Four movements; the location changed on mount, move and unmount.
	count := map[string]int{}
	for _, c := range changes {
		count[c]++
	}
	if count["movement:rack_mount"] != 4 || count["location_change:location_id"] != 3 || len(changes) != 7 {
		t.Errorf("ci_change rows %v", changes)
	}
	wantAudits := []string{"ci.rack_mounted", "ci.rack_moved", "ci.rack_moved", "ci.rack_unmounted"}
	if len(audits) != len(wantAudits) {
		t.Fatalf("audit entries %v, want %v", audits, wantAudits)
	}
	for i := range wantAudits {
		if audits[i] != wantAudits[i] {
			t.Errorf("audit %d: %s, want %s", i, audits[i], wantAudits[i])
		}
	}

	// A CI linked to an asset: the asset's location is set and mirrored.
	linked := f.CI(t, f.OrgA, f.Client1, "mount-linked")
	var assetID string
	err := database.WithTenant(bg, f.App, scopePtr(f.OrgA, f.User), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO asset (organization_id, asset_tag, name, ci_id) VALUES ($1, 'MOUNT-A', 'MOUNT-A', $2)
			RETURNING id::text`, f.OrgA, linked).Scan(&assetID)
	})
	if err != nil {
		t.Fatal(err)
	}
	lm := &rack.RackMount{OrganizationID: f.OrgA, RackID: rack1, CIID: linked, PositionU: 20, HeightU: 1, Face: "front"}
	if err = repo.CreateMount(ctx, lm); err != nil {
		t.Fatal(err)
	}
	var assetLocation string
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, assetID).Scan(&assetLocation); err != nil {
		t.Fatal(err)
	}
	if assetLocation != rack1 || locationOf(linked) != rack1 {
		t.Errorf("linked CI after mount: asset %q, CI %q, want rack %q", assetLocation, locationOf(linked), rack1)
	}
	if _, audits = history(linked); len(audits) != 1 || audits[0] != "ci.rack_mounted" {
		t.Errorf("linked CI audit %v", audits)
	}
}

func scopePtr(orgID, userID string) *database.TenantScope {
	scope := database.OrgWideScope(orgID, userID)
	return &scope
}
