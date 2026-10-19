//go:build auditdefect

package history_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT test (it fails until
// the product is fixed).
//
// WP-171 (CI-06, AUD-03; acceptance criterion 1 "History-Endpunkt vereint
// entity_change, Audit, location_change und Overrides chronologisch") P2
// "Unified CI history omits location_change table": unifiedCIHistory
// (history/pg_repository.go) selects only ci_change, entity_change and
// audit_log; the non-CI List reads only entity_change. Re-parenting a room
// with locations.PGRepository.Move writes a location_change row (WP-055) that
// neither the room's history nor the history of a CI in that room shows.
// Requirement: the move appears in the location history and in the history
// of the CI located there.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3HistoryIncludesLocationChange' ./internal/history/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT3HistoryIncludesLocationChange(t *testing.T) {
	f := scopetest.Seed(t, "d4")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	locs := locations.NewPGRepository(f.App)
	mk := func(kind locations.Kind, parent, name string) string {
		t.Helper()
		l, err := locs.Create(ctx, f.OrgA, locations.CreateRequest{Kind: kind, ParentID: parent, ClientID: f.Client1, Name: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return l.ID
	}
	site := mk(locations.KindSite, "", "audit-t3-hist-site")
	b1 := mk(locations.KindBuilding, site, "audit-t3-hist-b1")
	b2 := mk(locations.KindBuilding, site, "audit-t3-hist-b2")
	room := mk(locations.KindRoom, b1, "audit-t3-hist-room")
	ciID := f.CI(t, f.OrgA, f.Client1, "audit-t3-hist-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ciID, room); err != nil {
		t.Fatal(err)
	}
	repo := history.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 100}
	_, ciBefore, err := repo.List(ctx, f.OrgA, "ci", ciID, page)
	if err != nil {
		t.Fatal(err)
	}
	_, roomBefore, err := repo.List(ctx, f.OrgA, "location", room, page)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locs.Move(ctx, f.OrgA, room, b2); err != nil {
		t.Fatalf("move room: %v", err)
	}
	var rows int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM location_change WHERE location_id = $1`, room).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("precondition: location_change rows for the room = %d (%v), want 1", rows, err)
	}

	_, roomAfter, err := repo.List(ctx, f.OrgA, "location", room, page)
	if err != nil {
		t.Fatal(err)
	}
	if roomAfter <= roomBefore {
		t.Errorf("history of the moved room: %d entries before, %d after the move; the location_change row is missing", roomBefore, roomAfter)
	}
	_, ciAfter, err := repo.List(ctx, f.OrgA, "ci", ciID, page)
	if err != nil {
		t.Fatal(err)
	}
	if ciAfter <= ciBefore {
		t.Errorf("unified history of the CI in the moved room: %d entries before, %d after the move; the location_change is missing", ciBefore, ciAfter)
	}
}
