package discovery_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestDiscoveryRepositoryClientScope runs the discovery repository with the
// principal's tenant scope (TEN-06, WP-013): a principal restricted to client
// 1 neither sees nor changes a client-2 collector, its jobs, review items
// about client-2 CIs or review items without candidate CIs that a client-2
// collector opened (scope_suggested, WP-342).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "20")
	ownCI := f.CI(t, f.OrgA, f.Client1, "discovery-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "discovery-c2")

	repo := discovery.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownCol := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "col-c1", Config: map[string]any{}}
	foreignCol := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "col-c2", Config: map[string]any{}}
	for _, c := range []*discovery.Collector{ownCol, foreignCol} {
		if err := repo.RegisterCollector(orgCtx, c); err != nil {
			t.Fatalf("org-wide collector %s: %v", c.Name, err)
		}
	}
	foreignJob := &discovery.Job{OrganizationID: f.OrgA, CollectorID: foreignCol.ID, JobType: discovery.JobTypeSweep}
	if err := repo.CreateJob(orgCtx, foreignJob); err != nil {
		t.Fatalf("org-wide job: %v", err)
	}
	foreignReview := &discovery.ReviewItem{OrganizationID: f.OrgA, Kind: discovery.ReviewKindAmbiguousIdentity, CandidateCIIDs: []string{ownCI, foreignCI}}
	if err := repo.CreateReviewItem(orgCtx, foreignReview); err != nil {
		t.Fatalf("org-wide review item: %v", err)
	}
	// A review item without candidates of the client-2 collector, opened the
	// way the subnets report of the collector does.
	handler := discovery.NewHandler(repo, nil)
	if n, err := handler.SuggestScopes(orgCtx, f.OrgA, foreignCol.ID, []string{"10.20.9.0/24"}); err != nil || n != 1 {
		t.Fatalf("suggest a subnet of the client-2 collector: n=%d err=%v", n, err)
	}
	var suggestionID string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested'
		AND payload ->> 'collector_id' = $2`, f.OrgA, foreignCol.ID).Scan(&suggestionID); err != nil {
		t.Fatalf("read the scope_suggested item: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	cols, _, err := repo.ListCollectors(ctx, f.OrgA, page)
	if err != nil || len(cols) != 1 || cols[0].ID != ownCol.ID {
		t.Fatalf("client-1 collectors: %+v err=%v, want only the client-1 collector", cols, err)
	}
	if _, err = repo.Heartbeat(ctx, f.OrgA, foreignCol.ID); err == nil {
		t.Fatal("client-1 principal sent a heartbeat for a client-2 collector")
	}
	if err = repo.RegisterCollector(ctx, &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "intruder", Config: map[string]any{}}); err == nil {
		t.Fatal("client-1 principal registered a collector for client 2")
	}

	jobs, total, err := repo.ListJobs(ctx, f.OrgA, discovery.JobFilter{}, page)
	if err != nil || total != 0 || len(jobs) != 0 {
		t.Fatalf("client-1 jobs: total=%d %+v err=%v, want none", total, jobs, err)
	}
	if _, err = repo.GetJob(ctx, f.OrgA, foreignJob.ID); err == nil {
		t.Fatal("client-1 principal read a job of a client-2 collector")
	}
	if err = repo.CreateJob(ctx, &discovery.Job{OrganizationID: f.OrgA, CollectorID: foreignCol.ID, JobType: discovery.JobTypeSweep}); err == nil {
		t.Fatal("client-1 principal queued a job on a client-2 collector")
	}
	if err = repo.CreateJob(ctx, &discovery.Job{OrganizationID: f.OrgA, CollectorID: ownCol.ID, JobType: discovery.JobTypeSweep}); err != nil {
		t.Fatalf("client-1 job on own collector: %v", err)
	}

	items, total, err := repo.ListReviewItems(ctx, f.OrgA, discovery.ReviewFilter{}, page)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("client-1 review items: total=%d %+v err=%v, want none", total, items, err)
	}
	if _, err = repo.ResolveReviewItem(ctx, f.OrgA, foreignReview.ID, &discovery.Resolution{Status: discovery.ReviewStatusResolved}); err == nil {
		t.Fatal("client-1 principal resolved a review item about a client-2 CI")
	}
	if _, err = repo.GetReviewItem(ctx, f.OrgA, suggestionID); err == nil {
		t.Fatal("client-1 principal read the review item of the client-2 collector")
	}
	if _, err = repo.ResolveReviewItem(ctx, f.OrgA, suggestionID, &discovery.Resolution{Status: discovery.ReviewStatusDismissed, Action: "dismiss"}); err == nil {
		t.Fatal("client-1 principal resolved the review item of the client-2 collector")
	}

	for id, what := range map[string]string{foreignReview.ID: "review item about a client-2 CI", suggestionID: "review item of the client-2 collector"} {
		var status string
		if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM review_item WHERE id = $1`, id).Scan(&status); err != nil {
			t.Fatalf("read review item: %v", err)
		}
		if status != discovery.ReviewStatusOpen {
			t.Fatalf("%s changed to %q", what, status)
		}
	}
	// Positive controls: the client-2 principal sees the suggestion of its
	// collector, the client-1 principal the one of its own collector.
	if _, err = repo.GetReviewItem(f.ClientCtx(f.Client2), f.OrgA, suggestionID); err != nil {
		t.Fatalf("client-2 principal reads the review item of its collector: %v", err)
	}
	n, err := handler.SuggestScopes(orgCtx, f.OrgA, ownCol.ID, []string{"10.20.1.0/24"})
	if err != nil || n != 1 {
		t.Fatalf("suggest a subnet of the client-1 collector: n=%d err=%v", n, err)
	}
	if items, total, err = repo.ListReviewItems(ctx, f.OrgA, discovery.ReviewFilter{}, page); err != nil || total != 1 ||
		len(items) != 1 || items[0].Payload["collector_id"] != ownCol.ID {
		t.Fatalf("client-1 review items after the own suggestion: total=%d %+v err=%v, want only that one", total, items, err)
	}
	if _, _, err = repo.ListCollectors(context.Background(), f.OrgA, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
