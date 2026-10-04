package history_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
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
