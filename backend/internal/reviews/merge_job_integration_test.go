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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLongOperationsRunAsJobsAgainstPostgres covers JOB-04 (WP-183)
// against PostgreSQL: resolving a review item by merge is a job of kind
// review.merge with the item as reference (succeeded, or failed with the
// refusal of the effect; any other error of the effect becomes the fixed
// jobs.FailedMessage, WP-643, F-372); a state snapshot is a job of kind history.snapshot
// whose result is the replayed state with the fields of the change trail.
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
				return false, &reviews.Error{Status: http.StatusNotFound, Message: "the candidate is gone"}
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
	// The result is the replayed state, not only the entity (WP-516,
	// F-164): the renamed name of the trail.
	var snapshot history.StateSnapshot
	if err = json.Unmarshal(job.Result, &snapshot); err != nil || snapshot.EntityID != ciID || snapshot.EntityType != "ci" ||
		snapshot.Fields["name"] != "job-snapshot-renamed" || snapshot.Deleted {
		t.Errorf("snapshot result: %s %v, want the field name job-snapshot-renamed", job.Result, err)
	}
}

// TestReviewMergeRetryJobStatus covers WP-554 (JOB-04, F-330, E-89) against
// PostgreSQL, the binding retest of F-330: a merge whose effect fails leaves
// the item open and its review.merge job failed with the error (sanitized
// to jobs.FailedMessage, WP-643); the retry
// that succeeds resolves the item and ends the same job succeeded with
// attempts=2 and without the error; there is one job per item and kind. The
// merge of a probable_duplicate item is the CI-12 merge of the discovery
// handler: its ci.merge job fails with it (review.merge failed as well),
// and after the retry both are succeeded; the retry runs in the same
// ci.merge job, so no failed ci.merge job stays beside the merge that
// succeeded.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run TestReviewMergeRetryJobStatus ./internal/reviews/
func TestReviewMergeRetryJobStatus(t *testing.T) {
	f := scopetest.Seed(t, "ba")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobsOf := func(kind string) []jobs.Job {
		t.Helper()
		list, _, err := jobSvc.List(ctx, f.OrgA, jobs.Filter{Kind: kind}, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	jobOf := func(kind, refTable, refID string) []jobs.Job {
		t.Helper()
		var out []jobs.Job
		for _, j := range jobsOf(kind) {
			if j.RefTable == refTable && j.RefID == refID {
				out = append(out, j)
			}
		}
		return out
	}
	status := func(itemID string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM review_item WHERE id = $1`, itemID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	t.Run("ambiguous_identity", func(t *testing.T) {
		fail := true
		svc := reviews.NewService(reviews.NewPGRepository(f.App)).WithJobs(jobSvc).
			WithEffect(reviews.KindAmbiguousIdentity, func(context.Context, *reviews.Item, *reviews.Resolution) (bool, error) {
				if fail {
					return false, errors.New("transient failure")
				}
				return false, nil
			})
		candidate := f.CI(t, f.OrgA, f.Client1, "wp554-ambiguous")
		item := &reviews.Item{OrganizationID: f.OrgA, Kind: reviews.KindAmbiguousIdentity, Status: reviews.StatusOpen,
			Payload: map[string]any{"name": "wp554-ambiguous"}, CandidateCIIDs: []string{candidate}, ObjectKey: "wp554:ambiguous"}
		if _, err := svc.Open(ctx, item); err != nil {
			t.Fatal(err)
		}
		merge := &reviews.Resolution{Action: reviews.ActionMerge, CIID: candidate, ResolvedBy: f.User}
		if _, err := svc.Resolve(ctx, f.OrgA, item.ID, merge); err == nil {
			t.Fatal("the merge with a failing effect resolved")
		}
		got := jobOf(reviews.MergeJobKind, "review_item", item.ID)
		if len(got) != 1 || got[0].Status != jobs.StatusFailed || got[0].Error != jobs.FailedMessage || got[0].Attempts != 1 {
			t.Fatalf("job of the failed merge: %+v, want one failed job with the error and attempts 1", got)
		}
		if s := status(item.ID); s != reviews.StatusOpen {
			t.Errorf("item after the failed merge: %s, want open", s)
		}

		fail = false
		retry := &reviews.Resolution{Action: reviews.ActionMerge, CIID: candidate, ResolvedBy: f.User}
		if _, err := svc.Resolve(ctx, f.OrgA, item.ID, retry); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if s := status(item.ID); s != reviews.StatusResolved {
			t.Errorf("item after the retry: %s, want resolved", s)
		}
		got = jobOf(reviews.MergeJobKind, "review_item", item.ID)
		if len(got) != 1 || got[0].Status != jobs.StatusSucceeded || got[0].Attempts != 2 || got[0].Error != "" ||
			got[0].FinishedAt == nil || got[0].Progress != 100 {
			t.Errorf("job after the successful retry: %+v, want one succeeded job with attempts 2 and no error", got)
		}
	})

	t.Run("probable_duplicate", func(t *testing.T) {
		cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
		h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithReviewJobs(jobSvc).
			WithMerger(ci.NewService(cis).WithJobs(jobSvc))
		svc := h.ReviewService()
		survivor := f.CI(t, f.OrgA, f.Client1, "wp554-survivor")
		dup := f.CI(t, f.OrgA, f.Client1, "wp554-duplicate")
		var itemID string
		if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
			VALUES ($1, 'probable_duplicate', 'open', jsonb_build_object('ci_id', $2::text), ARRAY[$3::uuid]) RETURNING id::text`,
			f.OrgA, dup, survivor).Scan(&itemID); err != nil {
			t.Fatal(err)
		}
		before := len(jobsOf(ci.MergeJobKind))

		// Another transaction holds the survivor: the merge is refused
		// (409), the item stays open, ci.merge and review.merge fail.
		lock, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = lock.Exec(bg, `SELECT id FROM ci WHERE id = $1 FOR UPDATE`, survivor); err != nil {
			t.Fatal(err)
		}
		_, err = svc.Resolve(ctx, f.OrgA, itemID, &reviews.Resolution{Action: reviews.ActionMerge, CIID: survivor, ResolvedBy: f.User})
		if rerr := lock.Rollback(bg); rerr != nil {
			t.Fatal(rerr)
		}
		var refusal *reviews.Error
		if !errors.As(err, &refusal) || refusal.Status != http.StatusConflict {
			t.Fatalf("merge of a held survivor: %v, want the refusal 409", err)
		}
		if s := status(itemID); s != reviews.StatusOpen {
			t.Errorf("item after the refused merge: %s, want open", s)
		}
		review := jobOf(reviews.MergeJobKind, "review_item", itemID)
		if len(review) != 1 || review[0].Status != jobs.StatusFailed || review[0].Attempts != 1 {
			t.Fatalf("review.merge after the refused merge: %+v, want one failed job", review)
		}
		merges := jobsOf(ci.MergeJobKind)
		if len(merges) != before+1 || merges[0].Status != jobs.StatusFailed || merges[0].Attempts != 1 {
			t.Fatalf("ci.merge after the refused merge: %d jobs (before %d): %+v; want one more, the newest failed", len(merges), before, merges)
		}
		ciMerge := merges[0]

		// The retry merges: the item is resolved, the same ci.merge job and
		// the review.merge job are succeeded with their second attempt.
		if _, err = svc.Resolve(ctx, f.OrgA, itemID, &reviews.Resolution{Action: reviews.ActionMerge, CIID: survivor, ResolvedBy: f.User}); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if s := status(itemID); s != reviews.StatusResolved {
			t.Errorf("item after the retry: %s, want resolved", s)
		}
		var mergeRow string
		if err = f.Admin.QueryRow(bg, `SELECT coalesce(string_agg(id::text, ','), '') FROM ci_merge WHERE victim_id = $1 AND survivor_id = $2`,
			dup, survivor).Scan(&mergeRow); err != nil {
			t.Fatal(err)
		}
		if mergeRow != ciMerge.RefID {
			t.Errorf("ci_merge rows of the retry: %q, want the one row %s of the ci.merge job", mergeRow, ciMerge.RefID)
		}
		merges = jobsOf(ci.MergeJobKind)
		if len(merges) != before+1 {
			t.Errorf("ci.merge jobs after the retry: %d, want %d: the retry runs in the job of the failed attempt, no second job", len(merges), before+1)
		}
		again := jobOf(ci.MergeJobKind, "ci_merge", ciMerge.RefID)
		if len(again) != 1 || again[0].ID != ciMerge.ID || again[0].Status != jobs.StatusSucceeded || again[0].Attempts != 2 || again[0].Error != "" {
			t.Errorf("ci.merge after the retry: %+v, want job %s succeeded with attempts 2 and no error", again, ciMerge.ID)
		}
		for _, j := range merges {
			if j.Status == jobs.StatusFailed {
				t.Errorf("a failed ci.merge job stays beside the succeeded merge: %+v", j)
			}
		}
		review = jobOf(reviews.MergeJobKind, "review_item", itemID)
		if len(review) != 1 || review[0].Status != jobs.StatusSucceeded || review[0].Attempts != 2 || review[0].Error != "" {
			t.Errorf("review.merge after the retry: %+v, want one succeeded job with attempts 2 and no error", review)
		}
	})
}
