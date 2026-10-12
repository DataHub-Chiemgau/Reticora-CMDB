package reviews_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// TestReviewRepositoryOnPostgreSQL covers WP-088 (REC-08) against
// PostgreSQL: one open item per object and type (unique index
// review_item_open_object), a new item after the resolution, the
// action and resolver of a resolution, the 14-day escalation and the tenant
// scope (items about CIs outside it are invisible).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReviewRepositoryOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "80")
	bg := context.Background()
	repo := reviews.NewPGRepository(f.App)
	svc := reviews.NewService(repo)
	ctx := f.OrgCtx(f.OrgA)
	own := f.CI(t, f.OrgA, f.Client1, "review-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "review-c2")

	open := func(kind, key string, candidates ...string) (*reviews.Item, bool) {
		t.Helper()
		item := &reviews.Item{OrganizationID: f.OrgA, Kind: kind, ObjectKey: key, CandidateCIIDs: candidates,
			Payload: map[string]any{"matched_ci_id": own}}
		created, err := svc.Open(ctx, item)
		if err != nil {
			t.Fatalf("open %s %s: %v", kind, key, err)
		}
		return item, created
	}
	first, created := open(reviews.KindConflictingValues, "ci:"+own, own)
	if !created || first.ID == "" || first.ObjectKey != "ci:"+own {
		t.Fatalf("first item: %+v, created %v", first, created)
	}
	again, created := open(reviews.KindConflictingValues, "ci:"+own, own)
	if created || again.ID != first.ID {
		t.Errorf("same object and type: created %v, %s vs %s", created, again.ID, first.ID)
	}
	var stored int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND object_key = $2`,
		f.OrgA, "ci:"+own).Scan(&stored); err != nil || stored != 1 {
		t.Errorf("stored items of the object: %d, %v", stored, err)
	}

	// Resolution with action and resolver; afterwards the object is open for
	// a new item.
	resolved, err := repo.ResolveReviewItem(ctx, f.OrgA, first.ID, &reviews.Resolution{Status: reviews.StatusResolved,
		Action: reviews.ActionAccept, Note: "accepted", ResolvedBy: f.User})
	if err != nil || resolved.Action != reviews.ActionAccept || resolved.ResolvedBy != f.User || resolved.ResolvedAt == "" {
		t.Fatalf("resolved: %+v, %v", resolved, err)
	}
	if _, created = open(reviews.KindConflictingValues, "ci:"+own, own); !created {
		t.Error("no new item after the resolution")
	}

	// Escalation after 14 days.
	old, _ := open(reviews.KindDuplicateIP, "ip:192.0.2.80", own)
	if _, err = f.Admin.Exec(bg, `UPDATE review_item SET created_at = now() - interval '15 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	escalated, total, err := repo.ListReviewItems(ctx, f.OrgA, reviews.Filter{Escalated: true}, api.PaginationParams{Limit: 50})
	if err != nil || total != 1 || escalated[0].ID != old.ID || !escalated[0].Escalated {
		t.Errorf("escalated items: %+v, %d, %v", escalated, total, err)
	}
	if got, _ := repo.GetReviewItem(ctx, f.OrgA, first.ID); got == nil || got.Escalated {
		t.Errorf("young resolved item: %+v", got)
	}

	// Items about CIs outside the tenant scope are invisible.
	hidden, _ := open(reviews.KindAmbiguousIdentity, "serial:hidden", own, foreign)
	scoped := f.ClientCtx(f.Client1)
	if _, err = repo.GetReviewItem(scoped, f.OrgA, hidden.ID); err == nil {
		t.Error("client-1 scope sees an item about a client-2 CI")
	}
	all, _, err := repo.ListReviewItems(scoped, f.OrgA, reviews.Filter{Kind: reviews.KindAmbiguousIdentity}, api.PaginationParams{Limit: 50})
	if err != nil || len(all) != 0 {
		t.Errorf("client-1 listing: %+v, %v", all, err)
	}
}
