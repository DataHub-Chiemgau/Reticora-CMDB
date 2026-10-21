//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT test (must fail until fixed).
//
// WP-109 (TEN-01, TEN-02, TEN-05, TEN-09, RBA-03)
// Findings:
//   - P2 "asset carries location_id but no denormalized site_id, so the site
//     scope cannot apply to placed assets" (TEN-02).
//
// Gap: site-scoped principal confined to its site for location-bearing
// tables, resolved through the production chain role_assignment(scope_site_id)
// → permission.AccessGrants → identity.ResolveAccess →
// middleware.TenantScopeFor → RLS. The confinement to the client of the site
// (F-014) is fixed by WP-390 and covered by TestSiteGrantConfinedToClient
// (sitescope_integration_test.go); the site scope of placed assets is WP-392.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefectSiteGrant' ./internal/locations/
package locations_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

func TestAuditDefectSiteGrantConfinedToClientAndSite(t *testing.T) {
	f := scopetest.Seed(t, "09")

	room1 := f.Room(t, f.Client1, "audit-09-c1-s1")
	room1b := f.Room(t, f.Client1, "audit-09-c1-s1b") // a second site of client 1
	asset1b := f.Asset(t, f.OrgA, f.Client1, "audit-09-asset-c1-s1b")
	placeIn(t, f, "asset", asset1b, room1b)

	site1Only := viewerAtSites(t, f, "audit-09-site1", siteOfRoom(t, f, room1))

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		table string
		id    string
		want  bool
	}{
		// P2 (TEN-02): an asset placed at another site of the same client.
		{"site1: asset of client 1 at site 1b invisible (P2 TEN-02)", site1Only, "asset", asset1b, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibleTo(tc.ctx, t, f, tc.table, tc.id); got != tc.want {
				t.Errorf("%s %s visible = %v, want %v", tc.table, tc.id, got, tc.want)
			}
		})
	}
}
