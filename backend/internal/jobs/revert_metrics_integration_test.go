package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestJobRevertAgainstPostgres covers the revert of a merge job (API-09,
// JOB-03, WP-553; probe f6 of WP-182, retest of F-329) against PostgreSQL
// with the runtime login: a real CI merge of client 1 is a ci.merge job of
// its creator; a stranger without ci:delete is refused (403) and nothing
// changes; a principal of client 2, also with ci:delete, and one of another
// organization do not find the job (404); the creator reverts it (200), the
// victim is back and the job records reverted_at and reverted_by, in the
// API and in the table; a second revert is 409 "already reverted".
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestJobRevertAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "92")
	bg := context.Background()
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	ciSvc := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithJobs(svc)
	creator := f.AppUser(t, f.OrgA, "wp553-creator")
	stranger := f.AppUser(t, f.OrgA, "wp553-stranger")
	outsider := f.AppUser(t, f.OrgA, "wp553-outsider")
	foreign := f.AppUser(t, f.OrgB, "wp553-foreign")

	// requestCtx is the context of a request of user in org, restricted to
	// client (empty: org-wide), with job:read and perms.
	requestCtx := func(org, user, client string, perms ...identity.Permission) context.Context {
		scope := database.OrgWideScope(org, user)
		if client != "" {
			scope.Clients = database.ScopeIDs(client)
		}
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: org, UserID: user})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: org,
			Permissions: append([]identity.Permission{identity.PermJobRead}, perms...)})
	}

	// The revert of server/router.go: the unmerge of the merge, its
	// refusals as 409.
	mux := chi.NewRouter()
	jobs.NewAPIHandler(svc).WithKind(ci.MergeJobKind, identity.PermCIDelete, func(ctx context.Context, job *jobs.Job) error {
		_, err := ciSvc.RevertMerge(ctx, job.OrganizationID, job.RefID)
		for _, refusal := range []error{ci.ErrSnapshotExpired, ci.ErrMergeConflict, ci.ErrUnmergeConflict, ci.ErrMergeUnsupported} {
			if errors.Is(err, refusal) {
				return &jobs.RevertRefusedError{Reason: refusal}
			}
		}
		if err != nil && err.Error() == "not found" {
			return &jobs.RevertRefusedError{Reason: errors.New("the merge of the job no longer exists")}
		}
		return err
	}).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, http.NoBody).WithContext(ctx))
		return w
	}

	survivor := f.CI(t, f.OrgA, f.Client1, "wp553-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "wp553-victim")
	merged, err := ciSvc.Merge(requestCtx(f.OrgA, creator, f.Client1, identity.PermCIDelete), f.OrgA, survivor, victim)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	job, err := svc.Get(f.OrgCtx(f.OrgA), f.OrgA, mustMergeJob(t, f, merged.MergeID))
	if err != nil || job.Kind != ci.MergeJobKind || job.Status != jobs.StatusSucceeded || job.CreatedBy != creator {
		t.Fatalf("merge job: %+v %v", job, err)
	}
	victimMerged := func() bool {
		t.Helper()
		var into bool
		if qerr := f.Admin.QueryRow(bg, `SELECT merged_into_id IS NOT NULL FROM ci WHERE id = $1`, victim).Scan(&into); qerr != nil {
			t.Fatal(qerr)
		}
		return into
	}
	stored := func() string {
		t.Helper()
		var s string
		if qerr := f.Admin.QueryRow(bg, `SELECT status || '|' || COALESCE(reverted_by::text, 'NULL') || '|' || (reverted_at IS NOT NULL)::text
			FROM job WHERE id = $1`, job.ID).Scan(&s); qerr != nil {
			t.Fatal(qerr)
		}
		return s
	}
	revert := "/api/v1/jobs/" + job.ID + "/revert"

	// A stranger of client 1 without ci:delete: 403, nothing reverted.
	if w := call(requestCtx(f.OrgA, stranger, f.Client1), "POST", revert); w.Code != http.StatusForbidden {
		t.Errorf("stranger without permission: %d %s, want 403", w.Code, w.Body.String())
	}
	// A principal of client 2 and one of organization B, both with
	// ci:delete, do not find the job.
	for name, ctx := range map[string]context.Context{
		"client 2":       requestCtx(f.OrgA, outsider, f.Client2, identity.PermCIDelete),
		"organization B": requestCtx(f.OrgB, foreign, "", identity.PermCIDelete),
	} {
		if w := call(ctx, "POST", revert); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s, want 404", name, w.Code, w.Body.String())
		}
	}
	if !victimMerged() || stored() != "succeeded|NULL|false" {
		t.Fatalf("refused reverts changed something: victim merged %v, job %s", victimMerged(), stored())
	}

	// The creator reverts the merge; the job records it.
	creatorCtx := requestCtx(f.OrgA, creator, f.Client1)
	w := call(creatorCtx, "POST", revert)
	var answer struct {
		JobID      string     `json:"job_id"`
		Reverted   bool       `json:"reverted"`
		RevertedAt *time.Time `json:"reverted_at"`
		RevertedBy string     `json:"reverted_by"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &answer); err != nil || w.Code != http.StatusOK || answer.JobID != job.ID ||
		!answer.Reverted || answer.RevertedAt == nil || answer.RevertedBy != creator {
		t.Fatalf("revert by the creator: %d %s", w.Code, w.Body.String())
	}
	if victimMerged() {
		t.Error("the victim is still merged after the revert")
	}
	if got := stored(); got != "succeeded|"+creator+"|true" {
		t.Errorf("stored job after the revert = %q", got)
	}
	var shown struct {
		Status     string     `json:"status"`
		RevertedAt *time.Time `json:"reverted_at"`
		RevertedBy string     `json:"reverted_by"`
	}
	if w = call(creatorCtx, "GET", "/api/v1/jobs/"+job.ID); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &shown) != nil ||
		shown.Status != "succeeded" || shown.RevertedAt == nil || !shown.RevertedAt.Equal(*answer.RevertedAt) || shown.RevertedBy != creator {
		t.Errorf("GET of the reverted job: %d %s", w.Code, w.Body.String())
	}

	// A second revert is 409 "already reverted", also for a holder of
	// ci:delete; the job keeps its first revert.
	for name, ctx := range map[string]context.Context{
		"creator":             creatorCtx,
		"holder of ci:delete": requestCtx(f.OrgA, stranger, f.Client1, identity.PermCIDelete),
	} {
		w = call(ctx, "POST", revert)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already reverted") {
			t.Errorf("second revert by the %s: %d %s, want 409 already reverted", name, w.Code, w.Body.String())
		}
	}
	if got := stored(); got != "succeeded|"+creator+"|true" {
		t.Errorf("stored job after the second revert = %q", got)
	}
	// A principal of client 2 still does not find the reverted job.
	if w = call(requestCtx(f.OrgA, outsider, f.Client2, identity.PermCIDelete), "POST", revert); w.Code != http.StatusNotFound {
		t.Errorf("client 2 after the revert: %d %s, want 404", w.Code, w.Body.String())
	}
}

// mustMergeJob returns the id of the job of the merge mergeID.
func mustMergeJob(t *testing.T, f *scopetest.Fixture, mergeID string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM job WHERE ref_table = 'ci_merge' AND ref_id = $1`,
		mergeID).Scan(&id); err != nil {
		t.Fatalf("job of merge %s: %v", mergeID, err)
	}
	return id
}

// TestJobBacklogMetricsAgainstPostgres covers the job metrics of JOB-03
// (WP-553; probe f9 of WP-182) against PostgreSQL: the backlog collector
// reads the job table on every scrape; two queued jobs are
// reticora_job_backlog 2 with the age of the older one; a claim lowers the
// backlog; succeeding and failing a job raise the duration histogram and
// the failure counter of its kind.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestJobBacklogMetricsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "92")
	bg := context.Background()
	const kind = "wp553.metrics"
	repo := jobs.NewPGRepository(f.App)
	svc := jobs.NewService(repo)
	svc.Register(jobs.Kind{Name: kind, MaxAttempts: 1, Timeout: time.Minute})
	jobs.SetBacklogSource(repo)
	t.Cleanup(func() { jobs.SetBacklogSource(nil) })
	reg := prometheus.NewRegistry()
	jobs.RegisterMetrics(reg)

	// scrape reads the series of kind: backlog, oldest age, the number of
	// observed durations by status and the failures.
	type sample struct {
		backlog, oldest, failures float64
		durations                 map[string]uint64
	}
	scrape := func() sample {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		s := sample{durations: map[string]uint64{}}
		for _, fam := range families {
			for _, m := range fam.GetMetric() {
				labels := map[string]string{}
				for _, l := range m.GetLabel() {
					labels[l.GetName()] = l.GetValue()
				}
				if labels["kind"] != kind {
					continue
				}
				switch fam.GetName() {
				case "reticora_job_backlog":
					s.backlog = m.GetGauge().GetValue()
				case "reticora_job_oldest_queued_age_seconds":
					s.oldest = m.GetGauge().GetValue()
				case "reticora_job_failures_total":
					s.failures = m.GetCounter().GetValue()
				case "reticora_job_duration_seconds":
					s.durations[labels["status"]] = m.GetHistogram().GetSampleCount()
				}
			}
		}
		return s
	}

	before := scrape()
	if before.backlog != 0 {
		t.Fatalf("backlog before the test: %+v", before)
	}
	ctx := f.OrgCtx(f.OrgA)
	first, _, err := svc.Enqueue(ctx, f.OrgA, kind, map[string]int{"n": 1}, jobs.EnqueueOptions{CreatedBy: f.User})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.Enqueue(ctx, f.OrgA, kind, map[string]int{"n": 2}, jobs.EnqueueOptions{CreatedBy: f.User}); err != nil {
		t.Fatal(err)
	}
	queued := scrape()
	if queued.backlog != 2 || queued.oldest <= 0 {
		t.Errorf("two queued jobs: backlog %v, oldest age %v; want 2 and > 0", queued.backlog, queued.oldest)
	}
	// The older job waits for 30 s: its age is the oldest.
	if _, err = f.Admin.Exec(bg, `UPDATE job SET created_at = now() - interval '30 seconds' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if aged := scrape(); aged.backlog != 2 || aged.oldest < 30 || aged.oldest > 120 {
		t.Errorf("oldest queued job 30 s old: backlog %v, oldest age %v", aged.backlog, aged.oldest)
	}

	// A claim lowers the backlog; the claimed job succeeds.
	claimed, err := svc.Claim(bg, f.OrgA, kind)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if after := scrape(); after.backlog != 1 {
		t.Errorf("backlog after a claim = %v, want 1", after.backlog)
	}
	if _, err = svc.Succeed(bg, f.OrgA, claimed.ID, map[string]int{"ok": 1}); err != nil {
		t.Fatal(err)
	}
	succeeded := scrape()
	if succeeded.durations["succeeded"] != before.durations["succeeded"]+1 {
		t.Errorf("succeeded durations %d, before %d", succeeded.durations["succeeded"], before.durations["succeeded"])
	}

	// The second job fails for good (one attempt).
	claimed, err = svc.Claim(bg, f.OrgA, kind)
	if err != nil || claimed == nil {
		t.Fatalf("second claim: %+v %v", claimed, err)
	}
	failed, err := svc.Fail(bg, f.OrgA, claimed.ID, errors.New("wp553 boom"))
	if err != nil || failed.Status != jobs.StatusFailed {
		t.Fatalf("fail: %+v %v", failed, err)
	}
	end := scrape()
	if end.backlog != 0 {
		t.Errorf("backlog with no queued job = %v, want 0", end.backlog)
	}
	if end.failures != before.failures+1 {
		t.Errorf("failures %v, before %v", end.failures, before.failures)
	}
	if end.durations["failed"] != before.durations["failed"]+1 || end.durations["succeeded"] != before.durations["succeeded"]+1 {
		t.Errorf("durations %v, before %v", end.durations, before.durations)
	}
}
