package reviews_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLongOperationsRunAsJobsAgainstPostgres covers JOB-04 (WP-183)
// against PostgreSQL: resolving a review item by merge is a job of kind
// review.merge with the item as reference (succeeded, or failed with the
// error of the effect); a state snapshot is a job of kind history.snapshot
// whose result is the replayed state.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLongOperationsRunAsJobsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ba")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))

	fail := false
	svc := reviews.NewService(reviews.NewPGRepository(f.App)).WithJobs(jobSvc).
		WithEffect(reviews.KindProbableDuplicate, func(context.Context, *reviews.Item, *reviews.Resolution) (bool, error) {
			if fail {
				return false, errors.New("the candidate is gone")
			}
			return false, nil
		})
	open := func(name string) *reviews.Item {
		t.Helper()
		candidate := f.CI(t, f.OrgA, f.Client1, name)
		item := &reviews.Item{OrganizationID: f.OrgA, Kind: reviews.KindProbableDuplicate, Status: reviews.StatusOpen,
			Payload: map[string]any{"ci_id": candidate}, CandidateCIIDs: []string{candidate}, ObjectKey: "ci:" + candidate}
		if _, err := svc.Open(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	jobOf := func(itemID string) jobs.Job {
		t.Helper()
		list, _, err := jobSvc.List(ctx, f.OrgA, jobs.Filter{Kind: reviews.MergeJobKind}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for i := range list {
			if list[i].RefID == itemID {
				return list[i]
			}
		}
		t.Fatalf("no review.merge job for %s in %+v", itemID, list)
		return jobs.Job{}
	}

	merged := open("job-merge-ok")
	if _, err := svc.Resolve(ctx, f.OrgA, merged.ID, &reviews.Resolution{Action: reviews.ActionMerge, ResolvedBy: f.User}); err != nil {
		t.Fatal(err)
	}
	if job := jobOf(merged.ID); job.Status != jobs.StatusSucceeded || job.RefTable != "review_item" || job.FinishedAt == nil {
		t.Errorf("merge job: %+v", job)
	}
	fail = true
	failed := open("job-merge-fail")
	if _, err := svc.Resolve(ctx, f.OrgA, failed.ID, &reviews.Resolution{Action: reviews.ActionMerge}); err == nil {
		t.Fatal("the failing merge resolved")
	}
	if job := jobOf(failed.ID); job.Status != jobs.StatusFailed || job.Error != "the candidate is gone" {
		t.Errorf("failed merge job: %+v", job)
	}
	// Dismissing is no long operation and no job.
	fail = false
	dismissed := open("job-dismiss")
	if _, err := svc.Resolve(ctx, f.OrgA, dismissed.ID, &reviews.Resolution{Action: reviews.ActionDismiss}); err != nil {
		t.Fatal(err)
	}
	if list, _, _ := jobSvc.List(ctx, f.OrgA, jobs.Filter{Kind: reviews.MergeJobKind}, api.PaginationParams{Limit: 50}); len(list) != 2 {
		t.Errorf("review jobs: %d, want 2", len(list))
	}

	// The state snapshot is a job with the replayed state as result.
	jobSvc.Register(jobs.Kind{Name: history.SnapshotJobKind, MaxAttempts: 1})
	ciID := f.CI(t, f.OrgA, f.Client1, "job-snapshot")
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, new_value, ci_version)
		VALUES ($1, $2, 'update', 'name', '"job-snapshot-renamed"', 2)`, f.OrgA, ciID); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	history.NewHandler(history.NewPGRepository(f.App)).WithJobs(jobSvc).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/history/snapshots",
		bytes.NewBufferString(`{"entity_type":"ci","entity_id":"`+ciID+`"}`)).WithContext(ctx))
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || w.Code != http.StatusAccepted {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	var job *jobs.Job
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if job, err = jobSvc.Get(ctx, f.OrgA, accepted.JobID); err == nil && job.Status.Terminal() {
			break
		}
	}
	if job == nil || job.Status != jobs.StatusSucceeded || job.Kind != history.SnapshotJobKind {
		t.Fatalf("snapshot job: %+v %v", job, err)
	}
	var snapshot history.StateSnapshot
	if err = json.Unmarshal(job.Result, &snapshot); err != nil || snapshot.EntityID != ciID {
		t.Errorf("snapshot result: %s %v", job.Result, err)
	}
}
