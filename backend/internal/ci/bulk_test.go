package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// bulkRepo records the update request of every CI a bulk changes; with
// keepStatus it stores none of the statuses, like a write that does not
// take the status.
type bulkRepo struct {
	*MemoryRepository
	keepStatus bool
	// anyCase finds a CI by its id in any letter case, as PostgreSQL
	// compares uuid values.
	anyCase  bool
	mu       sync.Mutex
	requests map[string]UpdateRequest
	writes   map[string]int
}

func (r *bulkRepo) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	if r.anyCase {
		id = strings.ToLower(id)
	}
	r.mu.Lock()
	r.writes[id]++
	r.requests[id] = req
	r.mu.Unlock()
	if r.keepStatus {
		req.Status = nil
	}
	return r.MemoryRepository.Update(ctx, orgID, id, req)
}

// checkManual asserts that every id was written as a manual change of
// author with If-Match "*" (WP-368, E-66 option 1) and the reason bulk
// (bulk:<job_id> in a job, WP-304) and has version 2.
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
		case (req.ChangeReason != "bulk" && !strings.HasPrefix(req.ChangeReason, "bulk:")) || req.IfMatch == nil || *req.IfMatch != api.IfMatchAny:
			t.Errorf("%s: reason %q, If-Match %v, want bulk (or bulk:<job_id>) and *", id, req.ChangeReason, req.IfMatch)
		}
		if item, _ := r.GetByID(context.Background(), "org-1", id); item.Version != 2 {
			t.Errorf("%s: version %d, want 2", id, item.Version)
		}
	}
}

func newBulkRepo(t *testing.T, n int) (*bulkRepo, []string) {
	t.Helper()
	repo := &bulkRepo{MemoryRepository: NewMemoryRepository(), requests: map[string]UpdateRequest{}, writes: map[string]int{}}
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

// TestBulkDeduplicatesUUIDCase covers WP-690 (API-08, BLK-01; F-419): ids
// that name the same CI in another letter case of the UUID are one id. The
// CI is written once (version 2) and reported once, under its canonical
// lower-case id; an id sent only in upper case is applied and reported in
// lower case as well; synchronously and in the job.
func TestBulkDeduplicatesUUIDCase(t *testing.T) {
	// hexIDs gives the CIs of repo ids with hex letters, found in any
	// letter case like by PostgreSQL.
	hexIDs := func(repo *bulkRepo, ids []string) {
		repo.anyCase = true
		repo.MemoryRepository.mu.Lock()
		defer repo.MemoryRepository.mu.Unlock()
		for i, id := range ids {
			item := repo.items[id]
			delete(repo.items, id)
			item.ID = fmt.Sprintf("c0dea0%02x-abcd-4ef0-8abc-%012x", i, 0xdef000+i)
			repo.items[item.ID] = item
			ids[i] = item.ID
		}
	}
	check := func(t *testing.T, res BulkResult, repo *bulkRepo, ids []string) {
		t.Helper()
		if res.Total != len(ids) || res.Succeeded != len(ids) || res.Failed != 0 || len(res.Items) != len(ids) {
			t.Errorf("report totals: total %d, succeeded %d, failed %d, %d items, want %d ids once each",
				res.Total, res.Succeeded, res.Failed, len(res.Items), len(ids))
		}
		for i := range res.Items {
			if i < len(ids) && (res.Items[i].ID != ids[i] || res.Items[i].Result != "ok") {
				t.Errorf("item %d: %+v, want ok for the canonical id %s", i, res.Items[i], ids[i])
			}
		}
		repo.mu.Lock()
		again := 0
		for _, id := range ids {
			if repo.writes[id] != 1 {
				if again == 0 {
					item, _ := repo.GetByID(context.Background(), "org-1", id)
					t.Errorf("%s: written %d times (version %d), want once (version 2)", id, repo.writes[id], item.Version)
				}
				again++
			}
		}
		repo.mu.Unlock()
		if again > 0 {
			t.Errorf("%d of %d CIs not written exactly once", again, len(ids))
			return
		}
		repo.checkManual(t, "user-1", ids)
	}

	t.Run("synchronous", func(t *testing.T) {
		mux, repo, ids, _ := bulkMux(t, 2)
		hexIDs(repo, ids)
		mixed := strings.ToUpper(ids[0][:8]) + ids[0][8:]
		w := postBulk(mux, bulkBody(StatusInactive, ids[0], strings.ToUpper(ids[0]), mixed, strings.ToUpper(ids[1])))
		var res BulkResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
			t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
		}
		check(t, res, repo, ids)
	})

	t.Run("job", func(t *testing.T) {
		mux, repo, ids, jobSvc := bulkMux(t, BulkJobFromIDs)
		hexIDs(repo, ids)
		sent := append([]string{}, ids...)
		for _, id := range ids {
			sent = append(sent, strings.ToUpper(id))
		}
		w := postBulk(mux, bulkBody(StatusInactive, sent...))
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
		f := bulkError(tc.err)
		code, msg := f.code, f.msg
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
	// WP-304: the reason of a job names it.
	repo.mu.Lock()
	reason := repo.requests[ids[0]].ChangeReason
	repo.mu.Unlock()
	if reason != "bulk:"+job.ID {
		t.Errorf("reason of the job's change %q, want bulk:<job_id>", reason)
	}
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
	all := NewService(repo2).runBulk(ctx, "org-1", "user-2", "", &BulkRequest{Action: BulkSetStatus, IDs: ids2, Status: StatusMaintenance},
		func() bool { checks++; return false })
	if all.Succeeded != 101 || all.Skipped != 0 || all.Cancelled || checks != 4 {
		t.Errorf("uncancelled run: %d ok, %d skipped, cancelled %v, %d checkpoints; want 101, 0, false, 4", all.Succeeded, all.Skipped, all.Cancelled, checks)
	}
}

// problemField returns the status and the field of a problem answer.
func problemField(t *testing.T, w *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var problem struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	return w.Code, problem.Field
}

// TestBulkValidatesActionAndParams covers the contract of WP-304 (BLK-02,
// API-08): action defaults to set_status, status belongs to set_status
// alone, the other actions take params; an unknown action is 422 on
// action, unknown, missing or invalid parameters 422 on params.<name>, a
// tag the catalog lacks 422 on params.tags; nothing is written.
func TestBulkValidatesActionAndParams(t *testing.T) {
	mux, repo, ids, _ := bulkMux(t, 1)
	id := ids[0]
	body := func(action, params string) string {
		return `{"action":"` + action + `","ids":["` + id + `"],"params":` + params + `}`
	}
	for _, tc := range []struct {
		body  string
		field string
	}{
		{`{"action":"delete","ids":["` + id + `"]}`, "action"},
		{`{"ids":["` + id + `"]}`, "status"},
		{`{"action":"set_fields","ids":["` + id + `"],"status":"active","params":{"fields":{"model":"x"}}}`, "status"},
		{`{"action":"set_status","ids":["` + id + `"],"status":"active","params":{"fields":{}}}`, "params.fields"},
		{body(BulkSetFields, `{}`), "params.fields"},
		{body(BulkSetFields, `[]`), "params"},
		{body(BulkSetFields, `{"fields":{}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"status":"active"}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"location_id":""}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"owner_team_id":""}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"asset_id":""}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"bogus":1}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"name":null}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"attributes":{"tags":["a"]}}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"attributes":{"_instance":{}}}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"management_ip":"not-an-ip"}}`), "params.fields"},
		{body(BulkSetFields, `{"fields":{"model":"x"},"tags":["a"]}`), "params.tags"},
		{body(BulkSetLocation, `{}`), "params.location_id"},
		{body(BulkSetLocation, `{"location_id":"x"}`), "params.location_id"},
		{body(BulkAddTags, `{}`), "params.tags"},
		{body(BulkAddTags, `{"tags":[]}`), "params.tags"},
		{body(BulkAddTags, `{"tags":["Not A Key"]}`), "params.tags"},
		{body(BulkAddTags, `{"tags":["unknown-tag"]}`), "params.tags"},
		{body(BulkRemoveTags, `{"tags":["archived","unknown-tag"]}`), "params.tags"},
		{body(BulkSetOwner, `{}`), "params.owner_user_id"},
		{body(BulkSetOwner, `{"owner_team_id":"team-1"}`), "params.owner_team_id"},
		{body(BulkArchive, `{"reason":"x"}`), "params.reason"},
	} {
		code, field := problemField(t, postBulk(mux, tc.body))
		if code != http.StatusUnprocessableEntity || field != tc.field {
			t.Errorf("%s: %d on %q, want 422 on %s", tc.body, code, field, tc.field)
		}
	}
	repo.mu.Lock()
	written := len(repo.requests)
	repo.mu.Unlock()
	if item, _ := repo.GetByID(context.Background(), "org-1", id); written != 0 || item.Version != 1 || len(item.Tags) != 0 {
		t.Errorf("an invalid request wrote: %d updates, version %d, tags %v", written, item.Version, item.Tags)
	}
	// Valid: without action set_status; archive without params.
	for _, ok := range []string{`{"ids":["` + id + `"],"status":"active"}`, `{"action":"archive","ids":["` + id + `"]}`} {
		if w := postBulk(mux, ok); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s, want 200", ok, w.Code, w.Body.String())
		}
	}
}

// TestBulkFieldOperations covers the field operations of WP-304 (BLK-02,
// CI-08, OVR-03) against the memory repository: set_fields, set_location
// and set_owner write manual changes of the caller with the reason bulk;
// add_tags and remove_tags change the tags, an unchanged CI keeps its
// version; archive sets inactive and the tag archived once.
func TestBulkFieldOperations(t *testing.T) {
	mux, repo, ids, _ := bulkMux(t, 2)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	if _, err := NewService(repo).CreateTag(ctx, "org-1", &CreateTagRequest{Key: "dmz", DisplayName: map[string]string{"en": "DMZ"}}); err != nil {
		t.Fatal(err)
	}
	get := func(id string) *Item {
		item, err := repo.GetByID(context.Background(), "org-1", id)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	run := func(action, params string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"action": action, "ids": ids, "params": json.RawMessage(params)})
		w := postBulk(mux, string(raw))
		var res BulkResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
		if res.Action != action || res.Succeeded != len(ids) || res.Failed != 0 {
			t.Errorf("%s: report %+v, want every id ok", action, res)
		}
	}

	run(BulkSetFields, `{"fields":{"model":"bulk-model","attributes":{"rack_unit":"u1"}}}`)
	repo.checkManual(t, "user-1", ids)
	for _, id := range ids {
		if item := get(id); item.Model != "bulk-model" || item.Attributes["rack_unit"] != "u1" {
			t.Errorf("%s after set_fields: model %q, attributes %v", id, item.Model, item.Attributes)
		}
	}

	const loc, owner = "0a0a0a0a-0000-4000-8000-00000000000a", "0b0b0b0b-0000-4000-8000-00000000000b"
	run(BulkSetLocation, `{"location_id":"`+loc+`"}`)
	run(BulkSetOwner, `{"owner_user_id":"`+strings.ToUpper(owner)+`"}`)
	repo.mu.Lock()
	req := repo.requests[ids[1]]
	repo.mu.Unlock()
	// Every bulk is a job (WP-305): the reason names it.
	if req.OwnerUserID == nil || *req.OwnerUserID != owner || req.OwnerTeamID != nil || !strings.HasPrefix(req.ChangeReason, "bulk:") || req.Manual == nil {
		t.Errorf("set_owner request: %+v", req)
	}
	if item := get(ids[0]); item.LocationID != loc || item.OwnerUserID != owner {
		t.Errorf("after set_location and set_owner: location %q owner %q", item.LocationID, item.OwnerUserID)
	}

	run(BulkAddTags, `{"tags":["dmz"]}`)
	version := get(ids[0]).Version
	run(BulkAddTags, `{"tags":["dmz"]}`)
	if item := get(ids[0]); !slices.Equal(item.Tags, []string{"dmz"}) || item.Version != version {
		t.Errorf("after add_tags twice: tags %v version %d, want [dmz] and version %d", item.Tags, item.Version, version)
	}

	run(BulkArchive, `null`)
	version = get(ids[0]).Version
	run(BulkArchive, `{}`)
	if item := get(ids[0]); item.Status != StatusInactive || !slices.Equal(item.Tags, []string{"archived", "dmz"}) || item.Version != version {
		t.Errorf("after archive twice: status %s tags %v version %d, want inactive, [archived dmz], %d", item.Status, item.Tags, item.Version, version)
	}

	run(BulkRemoveTags, `{"tags":["dmz","archived"]}`)
	if item := get(ids[1]); len(item.Tags) != 0 {
		t.Errorf("after remove_tags: %v", item.Tags)
	}
}

// typedErr is an error with its own problem type.
type typedErr struct{}

func (typedErr) Error() string       { return "typed refusal" }
func (typedErr) ProblemType() string { return "https://reticora.io/problems/typed" }

// TestBulkReportsTypeAndFields covers the report of WP-304: every error
// carries its problem type (an api.TypedProblem its own, otherwise
// /problems/<code>) and the fields it names; a taken unique attribute is
// 409 with the attributes.
func TestBulkReportsTypeAndFields(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		typ    string
		fields []string
	}{
		{&UniqueError{Fields: []string{"asset_tag"}}, http.StatusConflict, "https://reticora.io/problems/409", []string{"asset_tag"}},
		{fmt.Errorf("update: %w", &ConflictError{Fields: []string{"tags"}, Current: 3}), http.StatusConflict, "https://reticora.io/problems/409", []string{"tags"}},
		{&api.FieldError{Field: "location_id", Message: "x", Status: http.StatusUnprocessableEntity}, http.StatusUnprocessableEntity,
			"https://reticora.io/problems/422", []string{"location_id"}},
		{&ValidationError{Violations: []Violation{{Field: "owner_user_id", Detail: "user not found"}, {Field: "owner_user_id"}, {Field: "attributes.k"}}},
			http.StatusUnprocessableEntity, "https://reticora.io/problems/422", []string{"owner_user_id", "attributes.k"}},
		{&api.FieldForbiddenError{Fields: []string{"attributes.secret"}}, http.StatusForbidden, "https://reticora.io/problems/403", []string{"attributes.secret"}},
		{fmt.Errorf("update: %w", typedErr{}), http.StatusInternalServerError, "https://reticora.io/problems/typed", nil},
		{fmt.Errorf("get: %w", ErrNotFound), http.StatusNotFound, "https://reticora.io/problems/404", nil},
	} {
		item := bulkFailed("id-1", tc.err)
		if item.Result != bulkResultError || item.Code != tc.code || item.Type != tc.typ || !slices.Equal(item.Fields, tc.fields) {
			t.Errorf("%v: %+v, want %d %s %v", tc.err, item, tc.code, tc.typ, tc.fields)
		}
	}
	if item := bulkOK("id-1"); item.Type != "" || item.Fields != nil {
		t.Errorf("ok item: %+v", item)
	}
}

// TestBulkActionRights covers the rights per action of WP-304: every field
// operation needs ci:write, which the route middleware checks, so the
// service admits it without a principal; a right beyond ci:write is
// checked before any change, fail-closed without a principal. The
// delegating actions of WP-780 need the right of their single operation.
func TestBulkActionRights(t *testing.T) {
	want := []string{BulkAddTags, BulkArchive, BulkRemoveTags, BulkSetFields, BulkSetLocation, BulkSetOwner, BulkSetStatus}
	delegating := map[string]identity.Permission{
		BulkAddRelationship: identity.PermRelationshipWrite, BulkRemoveRelationship: identity.PermRelationshipWrite,
		BulkLifecycleTransition: identity.PermLifecycleTransition, BulkChangeTemplate: identity.PermCIWrite,
		BulkExport: identity.PermExportRun,
	}
	all := slices.Clone(want)
	for action, perm := range delegating {
		all = append(all, action)
		if got, ok := BulkActionPermission(action); !ok || got != perm {
			t.Errorf("%s needs %q, want %q", action, got, perm)
		}
	}
	slices.Sort(all)
	if got := BulkActions(); !slices.Equal(got, all) {
		t.Errorf("actions %v, want %v", got, all)
	}
	for _, action := range want {
		if perm, ok := BulkActionPermission(action); !ok || perm != identity.PermCIWrite {
			t.Errorf("%s needs %q, want ci:write", action, perm)
		}
		if err := checkBulkPermission(context.Background(), action); err != nil {
			t.Errorf("%s without a principal: %v", action, err)
		}
	}
	const extra = "wp304_test_action"
	bulkActionPermissions[extra] = identity.PermJobRead
	defer delete(bulkActionPermissions, extra)
	var forbidden *api.FieldForbiddenError
	if err := checkBulkPermission(context.Background(), extra); !errors.As(err, &forbidden) || forbidden.Action != string(identity.PermJobRead) {
		t.Errorf("a right beyond ci:write without a principal: %v, want 403", err)
	}
	ctx := identity.WithPrincipal(context.Background(), identity.Principal{Subject: "user-1", Permissions: []identity.Permission{identity.PermJobRead}})
	if err := checkBulkPermission(ctx, extra); err != nil {
		t.Errorf("with the right: %v", err)
	}
	other := identity.WithPrincipal(context.Background(), identity.Principal{Subject: "user-1", Permissions: []identity.Permission{identity.PermCIWrite}})
	if err := checkBulkPermission(other, extra); !errors.As(err, &forbidden) {
		t.Errorf("without the right: %v, want 403", err)
	}
}
