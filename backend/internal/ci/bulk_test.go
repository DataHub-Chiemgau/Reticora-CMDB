package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// bulkRepo records the update request of every CI a bulk changes; with
// keepStatus it stores none of the statuses, like a write that does not
// take the status.
type bulkRepo struct {
	*MemoryRepository
	keepStatus bool
	mu         sync.Mutex
	requests   map[string]UpdateRequest
}

func (r *bulkRepo) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	r.mu.Lock()
	r.requests[id] = req
	r.mu.Unlock()
	if r.keepStatus {
		req.Status = nil
	}
	return r.MemoryRepository.Update(ctx, orgID, id, req)
}

// checkManual asserts that every id was written as a manual change of
// author with If-Match "*" (WP-368, E-66 option 1) and has version 2.
func (r *bulkRepo) checkManual(t *testing.T, author string, ids []string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		req, ok := r.requests[id]
		switch {
		case !ok:
			t.Errorf("%s: not written", id)
		case req.Manual == nil || req.Manual.Author != author:
			t.Errorf("%s: manual change %+v, want author %s", id, req.Manual, author)
		case req.ChangeReason != "bulk set_status" || req.IfMatch == nil || *req.IfMatch != api.IfMatchAny:
			t.Errorf("%s: reason %q, If-Match %v, want bulk set_status and *", id, req.ChangeReason, req.IfMatch)
		}
		if item, _ := r.GetByID(context.Background(), "org-1", id); item.Version != 2 {
			t.Errorf("%s: version %d, want 2", id, item.Version)
		}
	}
}

func newBulkRepo(t *testing.T, n int) (*bulkRepo, []string) {
	t.Helper()
	repo := &bulkRepo{MemoryRepository: NewMemoryRepository(), requests: map[string]UpdateRequest{}}
	ids := make([]string, n)
	for i := range ids {
		item := &Item{OrganizationID: "org-1", Name: fmt.Sprintf("bulk-%03d", i), CITypeID: "t", Status: StatusActive}
		if err := repo.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		ids[i] = item.ID
	}
	return repo, ids
}

func bulkMux(t *testing.T, n int) (chi.Router, *bulkRepo, []string, *jobs.Service) {
	t.Helper()
	repo, ids := newBulkRepo(t, n)
	jobSvc := jobs.NewService(jobs.NewMemoryRepository())
	jobSvc.Register(jobs.Kind{Name: BulkJobKind, MaxAttempts: 1})
	mux := chi.NewRouter()
	NewHandler(NewService(repo).WithBulkJobs(jobSvc)).RegisterRoutes(mux)
	return mux, repo, ids, jobSvc
}

func postBulk(mux chi.Router, body string) *httptest.ResponseRecorder {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis/bulk", bytes.NewBufferString(body)).WithContext(ctx))
	return w
}

func bulkBody(status string, ids ...string) string {
	raw, _ := json.Marshal(BulkRequest{Action: BulkSetStatus, IDs: ids, Status: status})
	return string(raw)
}

// TestBulkStatusReportsEachID covers API-08 and BLK-01 (WP-181): below 100
// ids the status change runs at once and reports every id; a missing CI
// fails alone.
func TestBulkStatusReportsEachID(t *testing.T) {
	mux, repo, ids, _ := bulkMux(t, 3)
	const missing = "99999999-0000-4000-8000-000000000099"
	w := postBulk(mux, bulkBody(StatusMaintenance, ids[0], missing, ids[1], ids[0]))
	var res BulkResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	if res.Total != 3 || res.Succeeded != 2 || res.Failed != 1 || res.Items[1].ID != missing || res.Items[1].Code != http.StatusNotFound {
		t.Errorf("report: %+v", res)
	}
	for _, id := range ids[:2] {
		if item, _ := repo.GetByID(context.Background(), "org-1", id); item.Status != StatusMaintenance {
			t.Errorf("%s: status %s", id, item.Status)
		}
	}
	if item, _ := repo.GetByID(context.Background(), "org-1", ids[2]); item.Status != StatusActive {
		t.Error("a CI outside the request changed")
	}
	// WP-368 (CI-04, OVR-01, API-07): each change is a manual one of the
	// requesting user.
	repo.checkManual(t, "user-1", ids[:2])
}

// TestBulkReportsAMalformedIDAs400 covers WP-517 (API-08, BLK-01; F-327):
// an id that is no UUID is reported with 400 "invalid CI id", the status the
// single change answers, and never reaches the repository; the other ids
// are applied, synchronously and in the job.
func TestBulkReportsAMalformedIDAs400(t *testing.T) {
	malformed := []string{"not-a-uuid", "1", "00000000-0000-0000-0000-00000000000g", "{00000000-0000-0000-0000-000000000001}"}
	check := func(t *testing.T, res BulkResult, repo *bulkRepo, valid []string) {
		t.Helper()
		if res.Total != len(valid)+len(malformed) || res.Succeeded != len(valid) || res.Failed != len(malformed) {
			t.Errorf("report totals: %+v", res)
		}
		bad := map[string]bool{}
		for _, id := range malformed {
			bad[id] = true
		}
		for _, item := range res.Items {
			switch {
			case bad[item.ID] && (item.Result != "error" || item.Code != http.StatusBadRequest || item.Error != "invalid CI id"):
				t.Errorf("malformed id %q: %+v, want error 400 invalid CI id", item.ID, item)
			case !bad[item.ID] && (item.Result != "ok" || item.Code != http.StatusOK):
				t.Errorf("valid id %s: %+v, want ok", item.ID, item)
			}
		}
		repo.mu.Lock()
		for _, id := range malformed {
			if _, ok := repo.requests[id]; ok {
				t.Errorf("malformed id %q reached the repository", id)
			}
		}
		repo.mu.Unlock()
		repo.checkManual(t, "user-1", valid)
	}

	t.Run("synchronous", func(t *testing.T) {
		mux, repo, ids, _ := bulkMux(t, 2)
		w := postBulk(mux, bulkBody(StatusMaintenance, append([]string{ids[0]}, append(malformed, ids[1])...)...))
		var res BulkResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
			t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
		}
		if res.Items[1].ID != malformed[0] {
			t.Errorf("report order: %+v", res.Items)
		}
		check(t, res, repo, ids)
	})

	t.Run("job", func(t *testing.T) {
		mux, repo, ids, jobSvc := bulkMux(t, BulkJobFromIDs)
		w := postBulk(mux, bulkBody(StatusInactive, append(append([]string{}, ids...), malformed...)...))
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || w.Code != http.StatusAccepted {
			t.Fatalf("bulk job: %d %s", w.Code, w.Body.String())
		}
		var job *jobs.Job
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if job, _ = jobSvc.Get(context.Background(), "org-1", accepted.JobID); job != nil && job.Status.Terminal() {
				break
			}
		}
		if job == nil || job.Status != jobs.StatusSucceeded {
			t.Fatalf("job: %+v", job)
		}
		var res BulkResult
		if err := json.Unmarshal(job.Result, &res); err != nil {
			t.Fatal(err)
		}
		check(t, res, repo, ids)
	})
}

// TestBulkErrorMapsLikeTheSinglePatch covers WP-517 (API-08; F-327): a
// database error of one id is reported with the status and the sanitized
// detail of the single PATCH (api.ClassifyDBError), not as 500; an
// unknown error stays a generic 500.
func TestBulkErrorMapsLikeTheSinglePatch(t *testing.T) {
	wrap := func(code string) error {
		return fmt.Errorf("update ci: %w", &pgconn.PgError{Code: code, Message: "secret detail of table ci", ConstraintName: "ci_pkey"})
	}
	for _, tc := range []struct {
		err  error
		code int
	}{
		{wrap("22P02"), http.StatusBadRequest},
		{wrap("23505"), http.StatusConflict},
		{wrap("23514"), http.StatusUnprocessableEntity},
		{wrap("40001"), http.StatusConflict},
		{wrap("42501"), http.StatusForbidden},
		{fmt.Errorf("get: %w", ErrNotFound), http.StatusNotFound},
		{errors.New("boom"), http.StatusInternalServerError},
	} {
		code, msg := bulkError(tc.err)
		if code != tc.code {
			t.Errorf("%v: code %d, want %d", tc.err, code, tc.code)
		}
		if strings.Contains(msg, "secret") || strings.Contains(msg, "ci_pkey") || strings.Contains(msg, "boom") {
			t.Errorf("%v: message %q leaks the internal error", tc.err, msg)
		}
	}
}

// TestBulkReportsAStatusNotApplied covers WP-368 (API-08, F-020): an id
// whose CI does not take the status is an error with code and reason in
// the report, not ok.
func TestBulkReportsAStatusNotApplied(t *testing.T) {
	mux, repo, ids, _ := bulkMux(t, 1)
	repo.keepStatus = true
	w := postBulk(mux, bulkBody(StatusInactive, ids[0]))
	var res BulkResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	if item := res.Items[0]; res.Succeeded != 0 || res.Failed != 1 || item.Result != "error" || item.Code != http.StatusConflict ||
		!strings.Contains(item.Error, "keeps status active") {
		t.Errorf("report of a status not applied: %+v", res)
	}
}

// TestBulkLimits: more than 500 ids, an unknown status or action is 422,
// no id is 400.
func TestBulkLimits(t *testing.T) {
	mux, _, ids, _ := bulkMux(t, 1)
	many := make([]string, MaxBulkIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("id-%d", i)
	}
	for body, want := range map[string]int{
		bulkBody(StatusActive, many...):                               http.StatusUnprocessableEntity,
		bulkBody("broken", ids[0]):                                    http.StatusUnprocessableEntity,
		`{"action":"delete","ids":["` + ids[0] + `"]}`:                http.StatusUnprocessableEntity,
		bulkBody(StatusActive):                                        http.StatusBadRequest,
		`{"action":"set_status","ids":["a"],"status":"active","x":1}`: http.StatusBadRequest,
	} {
		if w := postBulk(mux, body); w.Code != want {
			t.Errorf("%.60s: %d, want %d", body, w.Code, want)
		}
	}
}

// TestBulkFromHundredIDsIsAJob: from 100 ids the request answers 202 with
// the job; the job result is the report per id.
func TestBulkFromHundredIDsIsAJob(t *testing.T) {
	mux, repo, ids, jobSvc := bulkMux(t, BulkJobFromIDs)
	w := postBulk(mux, bulkBody(StatusInactive, ids...))
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || w.Code != http.StatusAccepted || accepted.JobID == "" ||
		!strings.HasSuffix(w.Header().Get("Location"), accepted.JobID) {
		t.Fatalf("bulk job: %d %s", w.Code, w.Body.String())
	}
	var job *jobs.Job
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var err error
		if job, err = jobSvc.Get(context.Background(), "org-1", accepted.JobID); err == nil && job.Status == jobs.StatusSucceeded {
			break
		}
	}
	if job == nil || job.Status != jobs.StatusSucceeded {
		t.Fatalf("job: %+v", job)
	}
	var res BulkResult
	if err := json.Unmarshal(job.Result, &res); err != nil || res.Succeeded != BulkJobFromIDs || res.Failed != 0 {
		t.Errorf("job result: %+v %v", res, err)
	}
	if item, _ := repo.GetByID(context.Background(), "org-1", ids[99]); item.Status != StatusInactive {
		t.Errorf("status %s", item.Status)
	}
	// WP-368: the job writes manual changes of its creator.
	if job.CreatedBy != "user-1" {
		t.Errorf("job created by %q", job.CreatedBy)
	}
	repo.checkManual(t, "user-1", ids)
}

// TestBulkJobWritesAsItsCreator covers WP-368 (OVR-01): the author of the
// changes of a bulk job is the creator of the job, not the context it runs
// in.
func TestBulkJobWritesAsItsCreator(t *testing.T) {
	repo, ids := newBulkRepo(t, 2)
	jobSvc := jobs.NewService(jobs.NewMemoryRepository())
	jobSvc.Register(jobs.Kind{Name: BulkJobKind, MaxAttempts: 1})
	svc := NewService(repo).WithBulkJobs(jobSvc)
	req := &BulkRequest{Action: BulkSetStatus, IDs: ids, Status: StatusMaintenance}
	job, _, err := jobSvc.Enqueue(context.Background(), "org-1", BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: "user-2"})
	if err != nil {
		t.Fatal(err)
	}
	svc.runBulkJob(tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"}), "org-1", job.ID, req)
	if job, err = jobSvc.Get(context.Background(), "org-1", job.ID); err != nil || job.Status != jobs.StatusSucceeded {
		t.Fatalf("job: %+v %v", job, err)
	}
	repo.checkManual(t, "user-2", ids)
}

// cancelOnClaim is a job service on which the bulk job is cancelled right
// after its claim; it counts the cancel checkpoints.
type cancelOnClaim struct {
	*jobs.Service
	mu     sync.Mutex
	checks int
}

func (c *cancelOnClaim) ClaimJob(ctx context.Context, orgID, id string) (*jobs.Job, error) {
	job, err := c.Service.ClaimJob(ctx, orgID, id)
	if err == nil && job != nil {
		_, err = c.Cancel(ctx, orgID, id)
	}
	return job, err
}

func (c *cancelOnClaim) IsCancelled(ctx context.Context, orgID, id string) (bool, error) {
	c.mu.Lock()
	c.checks++
	c.mu.Unlock()
	return c.Service.IsCancelled(ctx, orgID, id)
}

// TestBulkJobStopsAfterCancel covers WP-514 (JOB-03, BLK-01, F-162) on the
// in-process path: a bulk job asks for a cancel before every 25th id, at
// most one query per 25 ids; cancelled while running it starts no further
// id and keeps the partial report on the cancelled job: the ids changed
// before the checkpoint with their result, the others skipped.
func TestBulkJobStopsAfterCancel(t *testing.T) {
	repo, ids := newBulkRepo(t, 100)
	jobSvc := jobs.NewService(jobs.NewMemoryRepository())
	jobSvc.Register(jobs.Kind{Name: BulkJobKind, MaxAttempts: 1, Cancellable: true})
	cancels := &cancelOnClaim{Service: jobSvc}
	svc := NewService(repo).WithBulkJobs(cancels)
	req := &BulkRequest{Action: BulkSetStatus, IDs: ids, Status: StatusMaintenance}
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	job, _, err := jobSvc.Enqueue(ctx, "org-1", BulkJobKind, req, jobs.EnqueueOptions{CreatedBy: "user-2"})
	if err != nil {
		t.Fatal(err)
	}
	svc.runBulkJob(ctx, "org-1", job.ID, req)

	if job, err = jobSvc.Get(ctx, "org-1", job.ID); err != nil || job.Status != jobs.StatusCancelled {
		t.Fatalf("job: %+v %v, want cancelled", job, err)
	}
	var report BulkResult
	if err = json.Unmarshal(job.Result, &report); err != nil {
		t.Fatalf("the cancelled job keeps no report: %q %v", job.Result, err)
	}
	if !report.Cancelled || report.Total != 100 || report.Succeeded != bulkCancelEvery || report.Skipped != 100-bulkCancelEvery || report.Failed != 0 {
		t.Errorf("report: cancelled %v, total %d, succeeded %d, skipped %d, failed %d; want true, 100, 25, 75, 0",
			report.Cancelled, report.Total, report.Succeeded, report.Skipped, report.Failed)
	}
	for i, item := range report.Items {
		want := bulkResultOK
		if i >= bulkCancelEvery {
			want = bulkResultSkipped
		}
		if item.ID != ids[i] || item.Result != want {
			t.Errorf("item %d: %+v, want %s of %s", i, item, want, ids[i])
		}
	}
	repo.mu.Lock()
	written := len(repo.requests)
	repo.mu.Unlock()
	if written != bulkCancelEvery || cancels.checks != 1 {
		t.Errorf("%d CIs written with %d checkpoints, want %d with 1", written, cancels.checks, bulkCancelEvery)
	}

	// Not cancelled: one checkpoint per 25 ids, every id done.
	repo2, ids2 := newBulkRepo(t, 101)
	checks := 0
	all := NewService(repo2).runBulk(ctx, "org-1", "user-2", &BulkRequest{Action: BulkSetStatus, IDs: ids2, Status: StatusMaintenance},
		func() bool { checks++; return false })
	if all.Succeeded != 101 || all.Skipped != 0 || all.Cancelled || checks != 4 {
		t.Errorf("uncancelled run: %d ok, %d skipped, cancelled %v, %d checkpoints; want 101, 0, false, 4", all.Succeeded, all.Skipped, all.Cancelled, checks)
	}
}
