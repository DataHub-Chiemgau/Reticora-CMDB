package locations_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/jackc/pgx/v5"
)

// TestSiteGrantConfinedToClient covers WP-390 (TEN-01, TEN-09, RBA-03,
// F-014): a principal holding a role at a site of client 1 is confined to the
// client of that site. It is resolved through the production chain
// role_assignment(scope_site_id) → permission.AccessGrants →
// identity.ResolveAccess → middleware.TenantScopeFor → RLS.
//   - Contacts, credentials, assets and CIs of client 2, placed at site 2 or
//     unplaced, are invisible (TEN-09: every cross-client exposure is
//     critical).
//   - Contact, credential, asset and CI of client 1 at site 1 and the
//     org-wide contact are visible.
//   - Grants on sites of both clients see both clients.
//
// The site dimension within the client (assets and quantity items at another
// site of client 1, WP-392) is TestSiteGrantConfinedToClientAndSite.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSiteGrantConfinedToClient(t *testing.T) {
	f := scopetest.Seed(t, "d0")
	bg := context.Background()

	room1 := f.Room(t, f.Client1, "audit-d0-c1-s1")
	room2 := f.Room(t, f.Client2, "audit-d0-c2-s2")
	site1, site2 := siteOfRoom(t, f, room1), siteOfRoom(t, f, room2)

	contact := func(clientID any, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO contact (organization_id, client_id, display_name, email)
			VALUES ($1, $2, $3, $3 || '@audit-d0.example') RETURNING id::text`, f.OrgA, clientID, name).Scan(&id); err != nil {
			t.Fatalf("insert contact %s: %v", name, err)
		}
		return id
	}
	credential := func(clientID any, name string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO credential (organization_id, client_id, name, kind, ciphertext)
			VALUES ($1, $2, $3, 'snmp_v2c', '\x00'::bytea) RETURNING id::text`, f.OrgA, clientID, name).Scan(&id); err != nil {
			t.Fatalf("insert credential %s: %v", name, err)
		}
		return id
	}

	contact1 := contact(f.Client1, "audit-d0-contact-c1")
	contact2 := contact(f.Client2, "audit-d0-contact-c2")
	contactOrg := contact(nil, "audit-d0-contact-org")
	cred1 := credential(f.Client1, "audit-d0-cred-c1")
	cred2 := credential(f.Client2, "audit-d0-cred-c2")
	asset1 := f.Asset(t, f.OrgA, f.Client1, "audit-d0-asset-c1-s1")
	placeIn(t, f, "asset", asset1, room1)
	asset2 := f.Asset(t, f.OrgA, f.Client2, "audit-d0-asset-c2-s2")
	placeIn(t, f, "asset", asset2, room2)
	ci1 := f.CI(t, f.OrgA, f.Client1, "audit-d0-ci-c1-s1")
	placeIn(t, f, "ci", ci1, room1)
	ci2Unplaced := f.CI(t, f.OrgA, f.Client2, "audit-d0-ci-c2-unplaced")
	ci2Placed := f.CI(t, f.OrgA, f.Client2, "audit-d0-ci-c2-s2")
	placeIn(t, f, "ci", ci2Placed, room2)

	site1Only := viewerAtSites(t, f, "audit-d0-site1", site1)
	bothSites := viewerAtSites(t, f, "audit-d0-site1-site2", site1, site2)

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		table string
		id    string
		want  bool
	}{
		// Site-1 principal (site of client 1): own client and site visible.
		{"site1: contact of client 1 visible", site1Only, "contact", contact1, true},
		{"site1: org-wide contact visible", site1Only, "contact", contactOrg, true},
		{"site1: credential of client 1 visible", site1Only, "credential", cred1, true},
		{"site1: asset of client 1 at site 1 visible", site1Only, "asset", asset1, true},
		{"site1: CI of client 1 at site 1 visible", site1Only, "ci", ci1, true},
		// Nothing of client 2 may be visible.
		{"site1: contact of client 2 invisible", site1Only, "contact", contact2, false},
		{"site1: credential of client 2 invisible", site1Only, "credential", cred2, false},
		{"site1: asset of client 2 at site 2 invisible", site1Only, "asset", asset2, false},
		{"site1: unplaced CI of client 2 invisible", site1Only, "ci", ci2Unplaced, false},
		{"site1: CI of client 2 at site 2 invisible", site1Only, "ci", ci2Placed, false},
		// Edge: grants on sites of two clients see exactly both clients.
		{"site1+site2: contact of client 1 visible", bothSites, "contact", contact1, true},
		{"site1+site2: contact of client 2 visible", bothSites, "contact", contact2, true},
		{"site1+site2: asset of client 2 at site 2 visible", bothSites, "asset", asset2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibleTo(tc.ctx, t, f, tc.table, tc.id); got != tc.want {
				t.Errorf("%s %s visible = %v, want %v", tc.table, tc.id, got, tc.want)
			}
		})
	}
}

// TestSiteGrantConfinedToClientAndSite covers WP-392 (TEN-01, TEN-02,
// F-014, F-141): a principal holding a role at site 1 of client 1 sees the
// assets and quantity items of site 1 only, not those placed at another site
// of the same client (site 1b). Asset and quantity item carry the site of
// their location (site_id, migration 000151) and their policies apply
// app.site_scope. It is resolved through the same production chain as
// TestSiteGrantConfinedToClient.
//   - Asset and quantity item of client 1 at site 1b are invisible; so are
//     those of client 2 at site 2 (TEN-09).
//   - Asset and quantity item of client 1 at site 1, and an unplaced asset of
//     client 1 (no site, TEN-05), are visible.
//   - Grants on sites 1 and 1b see both sites.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSiteGrantConfinedToClientAndSite(t *testing.T) {
	f := scopetest.Seed(t, "09")

	room1 := f.Room(t, f.Client1, "audit-09-c1-s1")
	room1b := f.Room(t, f.Client1, "audit-09-c1-s1b") // a second site of client 1
	room2 := f.Room(t, f.Client2, "audit-09-c2-s2")
	asset1 := f.Asset(t, f.OrgA, f.Client1, "audit-09-asset-c1-s1")
	placeIn(t, f, "asset", asset1, room1)
	asset1b := f.Asset(t, f.OrgA, f.Client1, "audit-09-asset-c1-s1b")
	placeIn(t, f, "asset", asset1b, room1b)
	asset2 := f.Asset(t, f.OrgA, f.Client2, "audit-09-asset-c2-s2")
	placeIn(t, f, "asset", asset2, room2)
	asset1Unplaced := f.Asset(t, f.OrgA, f.Client1, "audit-09-asset-c1-unplaced")
	item1 := quantityItem(t, f, f.Client1, "audit-09-item-c1-s1", room1)
	item1b := quantityItem(t, f, f.Client1, "audit-09-item-c1-s1b", room1b)
	item2 := quantityItem(t, f, f.Client2, "audit-09-item-c2-s2", room2)

	site1Only := viewerAtSites(t, f, "audit-09-site1", siteOfRoom(t, f, room1))
	site1And1b := viewerAtSites(t, f, "audit-09-site1-site1b", siteOfRoom(t, f, room1), siteOfRoom(t, f, room1b))

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		table string
		id    string
		want  bool
	}{
		// P2 (TEN-02): placed at another site of the same client.
		{"site1: asset of client 1 at site 1b invisible (P2 TEN-02)", site1Only, "asset", asset1b, false},
		{"site1: quantity item of client 1 at site 1b invisible", site1Only, "quantity_item", item1b, false},
		{"site1: asset of client 2 at site 2 invisible", site1Only, "asset", asset2, false},
		{"site1: quantity item of client 2 at site 2 invisible", site1Only, "quantity_item", item2, false},
		// The own site and rows without site.
		{"site1: asset of client 1 at site 1 visible", site1Only, "asset", asset1, true},
		{"site1: quantity item of client 1 at site 1 visible", site1Only, "quantity_item", item1, true},
		{"site1: unplaced asset of client 1 visible", site1Only, "asset", asset1Unplaced, true},
		// Edge: grants on both sites of client 1 see both.
		{"site1+site1b: asset of client 1 at site 1b visible", site1And1b, "asset", asset1b, true},
		{"site1+site1b: quantity item of client 1 at site 1b visible", site1And1b, "quantity_item", item1b, true},
		{"site1+site1b: asset of client 2 at site 2 invisible", site1And1b, "asset", asset2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibleTo(tc.ctx, t, f, tc.table, tc.id); got != tc.want {
				t.Errorf("%s %s visible = %v, want %v", tc.table, tc.id, got, tc.want)
			}
		})
	}
}

// quantityItem inserts a quantity item of clientID placed in room through the
// maintenance pool and returns its id.
func quantityItem(t *testing.T, f *scopetest.Fixture, clientID, name, room string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `INSERT INTO quantity_item (organization_id, client_id, name, location_id)
		VALUES ($1, $2, $3, $4) RETURNING id::text`, f.OrgA, clientID, name, room).Scan(&id); err != nil {
		t.Fatalf("insert quantity item %s: %v", name, err)
	}
	return id
}

// siteOfRoom returns the site of a room of the fixture.
func siteOfRoom(t *testing.T, f *scopetest.Fixture, room string) string {
	t.Helper()
	var site string
	if err := f.Admin.QueryRow(context.Background(), `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room).Scan(&site); err != nil {
		t.Fatalf("site of room %s: %v", room, err)
	}
	return site
}

// placeIn places the row id of table (asset, ci) in room.
func placeIn(t *testing.T, f *scopetest.Fixture, table, id, room string) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), `UPDATE `+table+` SET location_id = $2 WHERE id = $1`, id, room); err != nil {
		t.Fatalf("place %s %s: %v", table, id, err)
	}
}

// viewerAtSites resolves a new user of organization A holding the viewer role
// at the given sites through the production chain
// role_assignment(scope_site_id) → permission.AccessGrants →
// identity.ResolveAccess → middleware.TenantScopeFor and returns its request
// context.
func viewerAtSites(t *testing.T, f *scopetest.Fixture, name string, sites ...string) context.Context {
	t.Helper()
	bg := context.Background()
	var viewerRole string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA).Scan(&viewerRole); err != nil {
		t.Fatalf("viewer role of organization A: %v", err)
	}
	user := f.AppUser(t, f.OrgA, name)
	for _, site := range sites {
		if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id)
			VALUES ($1, $2, $3, NULL, $4)`, f.OrgA, user, viewerRole, site); err != nil {
			t.Fatalf("assign viewer at site %s: %v", site, err)
		}
	}
	grants, err := permission.NewPGRepository(f.App).AccessGrants(bg, f.OrgA, user)
	if err != nil {
		t.Fatalf("access grants: %v", err)
	}
	access := identity.ResolveAccess(grants)
	p := &identity.Principal{Subject: user, OrganizationID: f.OrgA, Scope: &access.Scope, Permissions: access.Permissions}
	ts := middleware.TenantScopeFor(p)
	t.Logf("%s: resolved db scope clients=%+v sites=%+v", name, ts.Clients, ts.Sites)
	return database.ContextWithTenantScope(bg, &ts)
}

// visibleTo reports whether the row id of table is visible in the request
// context ctx of organization A.
func visibleTo(ctx context.Context, t *testing.T, f *scopetest.Fixture, table, id string) bool {
	t.Helper()
	var ok bool
	if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&ok)
	}); err != nil {
		t.Fatalf("read %s %s: %v", table, id, err)
	}
	return ok
}
