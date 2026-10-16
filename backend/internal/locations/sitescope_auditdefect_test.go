//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT test (must fail until fixed).
//
// WP-109 (TEN-01, TEN-02, TEN-05, TEN-09, RBA-03)
// Findings:
//   - P1 "A site-only grant leaves the client scope NULL, so site-scoped users
//     read other clients' contacts, credentials, assets and unplaced CIs"
//     (identity/principal.go unionDimension → All; middleware/auth.go maps All
//     to an unrestricted client scope).
//   - P2 "asset carries location_id but no denormalized site_id, so the site
//     scope cannot apply to placed assets" (TEN-02).
//
// Gap (critical): site-scoped principal confined to its client and site for
// client-level and location-bearing tables, resolved through the production
// chain role_assignment(scope_site_id) → permission.AccessGrants →
// identity.ResolveAccess → middleware.TenantScopeFor → RLS.
//
// RBA-03: "Eine org-weite Rolle setzt den jeweiligen Scope auf NULL" – only an
// org-wide role may leave a dimension unrestricted. TEN-09: every cross-client
// exposure is critical.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefectSiteGrant' ./internal/locations/
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

func TestAuditDefectSiteGrantConfinedToClientAndSite(t *testing.T) {
	f := scopetest.Seed(t, "d0")
	bg := context.Background()

	siteOf := func(room string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room).Scan(&site); err != nil {
			t.Fatalf("site of room %s: %v", room, err)
		}
		return site
	}
	room1 := f.Room(t, f.Client1, "audit-d0-c1-s1")
	room1b := f.Room(t, f.Client1, "audit-d0-c1-s1b") // a second site of client 1
	room2 := f.Room(t, f.Client2, "audit-d0-c2-s2")
	site1, site2 := siteOf(room1), siteOf(room2)

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
	place := func(table, id, room string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE `+table+` SET location_id = $2 WHERE id = $1`, id, room); err != nil {
			t.Fatalf("place %s %s: %v", table, id, err)
		}
	}

	contact1 := contact(f.Client1, "audit-d0-contact-c1")
	contact2 := contact(f.Client2, "audit-d0-contact-c2")
	contactOrg := contact(nil, "audit-d0-contact-org")
	cred1 := credential(f.Client1, "audit-d0-cred-c1")
	cred2 := credential(f.Client2, "audit-d0-cred-c2")
	asset1 := f.Asset(t, f.OrgA, f.Client1, "audit-d0-asset-c1-s1")
	place("asset", asset1, room1)
	asset1b := f.Asset(t, f.OrgA, f.Client1, "audit-d0-asset-c1-s1b")
	place("asset", asset1b, room1b)
	asset2 := f.Asset(t, f.OrgA, f.Client2, "audit-d0-asset-c2-s2")
	place("asset", asset2, room2)
	ci1 := f.CI(t, f.OrgA, f.Client1, "audit-d0-ci-c1-s1")
	place("ci", ci1, room1)
	ci2Unplaced := f.CI(t, f.OrgA, f.Client2, "audit-d0-ci-c2-unplaced")
	ci2Placed := f.CI(t, f.OrgA, f.Client2, "audit-d0-ci-c2-s2")
	place("ci", ci2Placed, room2)

	var viewerRole string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA).Scan(&viewerRole); err != nil {
		t.Fatalf("viewer role of organization A: %v", err)
	}
	// principal resolves a user holding viewer grants on the given sites
	// through the production chain and returns its request context.
	principal := func(name string, sites ...string) context.Context {
		t.Helper()
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
	visible := func(ctx context.Context, table, id string) bool {
		t.Helper()
		var ok bool
		if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&ok)
		}); err != nil {
			t.Fatalf("read %s %s: %v", table, id, err)
		}
		return ok
	}

	site1Only := principal("audit-d0-site1", site1)
	bothSites := principal("audit-d0-site1-site2", site1, site2)

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
		// P1: nothing of client 2 may be visible.
		{"site1: contact of client 2 invisible (P1)", site1Only, "contact", contact2, false},
		{"site1: credential of client 2 invisible (P1)", site1Only, "credential", cred2, false},
		{"site1: asset of client 2 at site 2 invisible (P1/P2)", site1Only, "asset", asset2, false},
		{"site1: unplaced CI of client 2 invisible (P1)", site1Only, "ci", ci2Unplaced, false},
		{"site1: CI of client 2 at site 2 invisible", site1Only, "ci", ci2Placed, false},
		// P2 (TEN-02): an asset placed at another site of the same client.
		{"site1: asset of client 1 at site 1b invisible (P2 TEN-02)", site1Only, "asset", asset1b, false},
		// Edge: grants on sites of two clients see exactly both clients.
		{"site1+site2: contact of client 1 visible", bothSites, "contact", contact1, true},
		{"site1+site2: contact of client 2 visible", bothSites, "contact", contact2, true},
		{"site1+site2: asset of client 2 at site 2 visible", bothSites, "asset", asset2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visible(tc.ctx, tc.table, tc.id); got != tc.want {
				t.Errorf("%s %s visible = %v, want %v", tc.table, tc.id, got, tc.want)
			}
		})
	}
}
