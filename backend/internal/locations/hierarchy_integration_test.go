package locations_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
)

// TestHierarchyScopeOnPostgreSQL covers WP-109 (TEN-01): the hierarchy
// Organization → Client → Site → location tree → CI/asset is one structure
// and its scope takes effect at every level.
//   - Sites, buildings and rooms are the nodes of the one location tree
//     (same id, parent chain room → building → site); a CI placed in a room
//     derives the site from it.
//   - An org-wide principal sees both clients' branches; a client-scoped
//     principal only its client's site, location nodes, CI and asset; a
//     site-scoped principal (a role at the site, resolved from its role
//     assignment, WP-390) only its site, location nodes and CI and nothing of
//     the other client; organization B sees none.
//   - Assets are scoped by client (they carry no site scope column).
//   - Resellers above the organization are [P4] and not modelled.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHierarchyScopeOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "86")
	bg := context.Background()
	room1 := f.Room(t, f.Client1, "hier-109-c1")
	room2 := f.Room(t, f.Client2, "hier-109-c2")
	parent := func(id string) string {
		t.Helper()
		var p string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(parent_id::text, '') FROM location WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatalf("location node %s: %v", id, err)
		}
		return p
	}
	// One structure: the specialist rows are the location nodes.
	building1 := parent(room1)
	site1 := parent(building1)
	building2 := parent(room2)
	site2 := parent(building2)
	var b1Site, s1Client string
	if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text, s.client_id::text FROM building b JOIN site s ON s.id = b.site_id WHERE b.id = $1`, building1).Scan(&b1Site, &s1Client); err != nil ||
		b1Site != site1 || s1Client != f.Client1 {
		t.Fatalf("building %s → site %s (location parent %s), client %s: %v", building1, b1Site, site1, s1Client, err)
	}
	var dupes int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM (SELECT id FROM site UNION ALL SELECT id FROM building UNION ALL SELECT id FROM room) s
		WHERE s.id IN ($1::uuid, $2::uuid, $3::uuid) AND NOT EXISTS (SELECT 1 FROM location l WHERE l.id = s.id)`, site1, building1, room1).Scan(&dupes); err != nil || dupes != 0 {
		t.Errorf("site/building/room rows without location node: %d %v", dupes, err)
	}

	// A CI of client 1 placed in room 1 derives site 1 from the tree; an
	// asset of client 2 sits in room 2.
	ci1 := f.CI(t, f.OrgA, f.Client1, "hier-109-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ci1, room1); err != nil {
		t.Fatal(err)
	}
	var ciClient, ciSite string
	if err := f.Admin.QueryRow(bg, `SELECT client_id::text, site_id::text FROM ci WHERE id = $1`, ci1).Scan(&ciClient, &ciSite); err != nil ||
		ciClient != f.Client1 || ciSite != site1 {
		t.Fatalf("CI scope from its room: client %s site %s, want %s %s (%v)", ciClient, ciSite, f.Client1, site1, err)
	}
	asset2 := f.Asset(t, f.OrgA, f.Client2, "hier-109-asset")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location_id = $2 WHERE id = $1`, asset2, room2); err != nil {
		t.Fatal(err)
	}
	var assetClient string
	if err := f.Admin.QueryRow(bg, `SELECT client_id::text FROM asset WHERE id = $1`, asset2).Scan(&assetClient); err != nil || assetClient != f.Client2 {
		t.Fatalf("asset client from its room: %s (%v)", assetClient, err)
	}

	type visible struct{ site1, site2, room1, room2, ci1, asset2 bool }
	see := func(ctx context.Context, orgID string) visible {
		t.Helper()
		var v visible
		err := database.WithRequestTenant(ctx, f.App, orgID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT
				EXISTS (SELECT 1 FROM site WHERE id = $1), EXISTS (SELECT 1 FROM site WHERE id = $2),
				EXISTS (SELECT 1 FROM location WHERE id = $3), EXISTS (SELECT 1 FROM location WHERE id = $4),
				EXISTS (SELECT 1 FROM ci WHERE id = $5), EXISTS (SELECT 1 FROM asset WHERE id = $6)`,
				site1, site2, room1, room2, ci1, asset2).Scan(&v.site1, &v.site2, &v.room1, &v.room2, &v.ci1, &v.asset2)
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		org   string
		wants visible
	}{
		{"organization-wide", f.OrgCtx(f.OrgA), f.OrgA, visible{true, true, true, true, true, true}},
		{"client 1", f.ClientCtx(f.Client1), f.OrgA, visible{site1: true, room1: true, ci1: true}},
		{"client 2", f.ClientCtx(f.Client2), f.OrgA, visible{site2: true, room2: true, asset2: true}},
		// A site principal is confined to the client of its site (F-014):
		// the asset of client 2 is not visible at site 1.
		{"site 1", viewerAtSites(t, f, "hier-109-site1", site1), f.OrgA, visible{site1: true, room1: true, ci1: true}},
		{"site 2", viewerAtSites(t, f, "hier-109-site2", site2), f.OrgA, visible{site2: true, room2: true, asset2: true}},
		{"organization B", f.OrgCtx(f.OrgB), f.OrgB, visible{}},
	} {
		if got := see(tc.ctx, tc.org); got != tc.wants {
			t.Errorf("%s: sees %+v, want %+v", tc.name, got, tc.wants)
		}
	}
}
