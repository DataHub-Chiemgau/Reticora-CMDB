package history_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestHistoryRepositoryClientScope runs the history repository with the
// principal's tenant scope (TEN-06, WP-011): a principal restricted to client
// 1 neither reads nor extends the change trail of a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHistoryRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "16")
	own := f.CI(t, f.OrgA, f.Client1, "history-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "history-c2")

	repo := history.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	for _, id := range []string{own, foreign} {
		if err := repo.Record(orgCtx, &history.Change{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, ChangeType: "update", FieldName: "name"}); err != nil {
			t.Fatalf("org-wide record for %s: %v", id, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	changes, total, err := repo.List(ctx, f.OrgA, "ci", own, page)
	if err != nil || total != 1 || len(changes) != 1 {
		t.Fatalf("client-1 trail of own CI: total=%d len=%d err=%v, want 1/1/nil", total, len(changes), err)
	}
	changes, total, err = repo.List(ctx, f.OrgA, "ci", foreign, page)
	if err != nil || total != 0 || len(changes) != 0 {
		t.Fatalf("client-1 trail of client-2 CI: total=%d len=%d err=%v, want 0/0/nil", total, len(changes), err)
	}
	trail, err := repo.TrailUpTo(ctx, f.OrgA, "ci", foreign, time.Now().Add(time.Minute))
	if err != nil || len(trail) != 0 {
		t.Fatalf("client-1 replay of client-2 CI: len=%d err=%v, want 0/nil", len(trail), err)
	}
	if err = repo.Record(ctx, &history.Change{OrganizationID: f.OrgA, EntityType: "ci", EntityID: foreign, ChangeType: "comment"}); err == nil {
		t.Fatal("client-1 principal extended the trail of a client-2 CI")
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM entity_change WHERE entity_id = $1`, foreign).Scan(&count); err != nil {
		t.Fatalf("count foreign trail: %v", err)
	}
	if count != 1 {
		t.Fatalf("client-2 CI trail has %d rows, want 1", count)
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, "ci", own, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestLocationHistoryClientScope covers LOC-11 and TEN-06 (WP-473): a
// principal restricted to client 1 reads the history of a moved client-1
// location, while GET /api/v1/history/location/{id} of a moved client-2
// location is 404, before and after that location's deletion; an org-wide
// principal still reads the deleted location's move.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationHistoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "16")
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	locs := locations.NewPGRepository(f.App)
	moved := map[string]string{}
	for _, client := range []string{f.Client1, f.Client2} {
		tree := locationTree{t: t, ctx: orgCtx, org: f.OrgA, clientID: client, repo: locs}
		site := tree.mk(locations.KindSite, "", "hist-scope-site-"+client[len(client)-2:])
		b1 := tree.mk(locations.KindBuilding, site, "hist-scope-b1")
		b2 := tree.mk(locations.KindBuilding, site, "hist-scope-b2")
		room := tree.mk(locations.KindRoom, b1, "hist-scope-room")
		tree.move(room, b2)
		moved[client] = room
	}
	h := history.NewHandler(history.NewPGRepository(f.App))
	ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	if rec, own := getHistory(ctx, t, h, "/api/v1/history/location/"+moved[f.Client1]); rec.Code != http.StatusOK || own.Total != 1 {
		t.Fatalf("client-1 history of own moved room: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := getHistory(ctx, t, h, "/api/v1/history/location/"+moved[f.Client2]); rec.Code != http.StatusNotFound {
		t.Fatalf("client-1 history of a client-2 room: %d %s, want 404", rec.Code, rec.Body)
	}
	if _, _, err := history.NewPGRepository(f.App).List(ctx, f.OrgA, "location", moved[f.Client2], api.PaginationParams{Limit: 100}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("client-1 repository list of a client-2 room: %v, want api.ErrNotFound", err)
	}
	if err := locs.Delete(orgCtx, f.OrgA, moved[f.Client2]); err != nil {
		t.Fatalf("delete client-2 room: %v", err)
	}
	if rec, _ := getHistory(ctx, t, h, "/api/v1/history/location/"+moved[f.Client2]); rec.Code != http.StatusNotFound {
		t.Fatalf("client-1 history of a deleted client-2 room: %d %s, want 404", rec.Code, rec.Body)
	}
	if rec, other := getHistory(orgCtx, t, h, "/api/v1/history/location/"+moved[f.Client2]); rec.Code != http.StatusOK || other.Total != 1 {
		t.Fatalf("org-wide history of the deleted client-2 room: %d %s", rec.Code, rec.Body)
	}
}
