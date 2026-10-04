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
// 1 neither sees nor changes a client-2 collector, its jobs or review items
// about client-2 CIs.
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

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	cols, _, err := repo.ListCollectors(ctx, f.OrgA, page)
	if err != nil || len(cols) != 1 || cols[0].ID != ownCol.ID {
		t.Fatalf("client-1 collectors: %+v err=%v, want only the client-1 collector", cols, err)
	}
	if err = repo.Heartbeat(ctx, f.OrgA, foreignCol.ID); err == nil {
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
	if _, err = repo.ResolveReviewItem(ctx, f.OrgA, foreignReview.ID, discovery.Resolution{Status: discovery.ReviewStatusResolved}); err == nil {
		t.Fatal("client-1 principal resolved a review item about a client-2 CI")
	}

	var status string
	if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM review_item WHERE id = $1`, foreignReview.ID).Scan(&status); err != nil {
		t.Fatalf("read review item: %v", err)
	}
	if status != discovery.ReviewStatusOpen {
		t.Fatalf("review item about a client-2 CI changed to %q", status)
	}
	if _, _, err = repo.ListCollectors(context.Background(), f.OrgA, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
