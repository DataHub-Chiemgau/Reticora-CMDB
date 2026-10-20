package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestBulkAgainstPostgres covers API-08 and BLK-01 (WP-181) against
// PostgreSQL: a client-scoped principal changes the status of its CIs and
// gets 404 for a CI of another client, each in the report; from 100 ids on
// the change runs as a job whose result is the report.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestBulkAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b9")
	bg := context.Background()
	ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client1})
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithBulkJobs(jobSvc)).RegisterRoutes(mux)
	post := func(req ci.BulkRequest) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis/bulk", bytes.NewReader(raw)).WithContext(ctx))
		return w
	}

	own1 := f.CI(t, f.OrgA, f.Client1, "bulk-own-1")
	own2 := f.CI(t, f.OrgA, f.Client1, "bulk-own-2")
	foreign := f.CI(t, f.OrgA, f.Client2, "bulk-foreign")
	w := post(ci.BulkRequest{Action: ci.BulkSetStatus, IDs: []string{own1, foreign, own2}, Status: ci.StatusMaintenance})
	var res ci.BulkResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	if res.Succeeded != 2 || res.Failed != 1 || res.Items[1].ID != foreign || res.Items[1].Code != http.StatusNotFound {
		t.Errorf("report: %+v", res)
	}
	var maintenance, foreignStatus string
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT string_agg(status, ',') FROM ci WHERE id IN ($1, $2)),
		(SELECT status FROM ci WHERE id = $3)`, own1, own2, foreign).Scan(&maintenance, &foreignStatus); err != nil {
		t.Fatal(err)
	}
	if maintenance != "maintenance,maintenance" || foreignStatus != "active" {
		t.Errorf("statuses %s, foreign %s", maintenance, foreignStatus)
	}

	// From 100 ids on: a job.
	ids := []string{}
	rows, err := f.Admin.Query(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), 'bulk-job-' || g, 'active'
		FROM generate_series(1, 100) g RETURNING id::text`, f.OrgA, f.Client1)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	w = post(ci.BulkRequest{Action: ci.BulkSetStatus, IDs: ids, Status: ci.StatusInactive})
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || w.Code != http.StatusAccepted {
		t.Fatalf("bulk job: %d %s", w.Code, w.Body.String())
	}
	var job *jobs.Job
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if job, err = jobSvc.Get(ctx, f.OrgA, accepted.JobID); err == nil && job.Status.Terminal() {
			break
		}
	}
	if job == nil || job.Status != jobs.StatusSucceeded {
		t.Fatalf("job: %+v %v", job, err)
	}
	if err = json.Unmarshal(job.Result, &res); err != nil || res.Succeeded != 100 {
		t.Errorf("job result: %+v %v", res, err)
	}
	var inactive int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND status = 'inactive'`, ids).Scan(&inactive); err != nil || inactive != 100 {
		t.Errorf("inactive %d, %v", inactive, err)
	}
}

// TestBulkStatusIsManualWriteUnderRequiredIfMatch covers WP-368 (CI-04,
// OVR-01, API-07, API-08; F-020) with decision E-66 option 1 (transition
// rule): the organization requires If-Match, the bulk status change sends
// none and is not refused (a deliberate overwrite, If-Match "*"). Every CI
// takes the status also over an existing manual override and gets a
// protected status override by the initiating user with the reason
// "bulk set_status", version+1, a ci_change row of the new version and an
// audit entry, synchronously and in the job from 100 ids on. A PATCH of
// status with the If-Match read before is 409 fields=[status]; an automated
// write of status under the bulk override is still dropped.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestBulkStatusIsManualWriteUnderRequiredIfMatch(t *testing.T) {
	e := newAuditT7BulkEnv(t, "7a")
	bg := context.Background()
	if _, err := e.f.Admin.Exec(bg, `UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.f.Admin.QueryRow(bg, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	// Status overrides of the bulk change, history rows of a version and
	// audit entries of status changes by the user.
	const (
		bulkOverrides = `SELECT count(*) FROM ci_field_value WHERE ci_id = ANY($1::uuid[]) AND field_name = 'status'
			AND override_value = to_jsonb($2::text) AND override_author = $3 AND override_reason = 'bulk set_status' AND protected AND override_at IS NOT NULL`
		statusChanges = `SELECT count(*) FROM ci_change WHERE ci_id = ANY($1::uuid[]) AND change_type = 'status_change' AND field_name = 'status'
			AND new_value = to_jsonb($2::text) AND ci_version = $3 AND actor_id = $4`
		statusAudits = `SELECT count(*) FROM audit_log WHERE resource_id = ANY($1::uuid[]) AND action = 'ci.status_changed' AND actor_id = $2`
	)

	// Synchronous: a carries a manual status override, b none.
	a := e.f.CI(t, e.f.OrgA, e.f.Client1, "bulk-manual-a")
	b := e.f.CI(t, e.f.OrgA, e.f.Client1, "bulk-manual-b")
	if w := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+a, "", map[string]any{"status": "maintenance"}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("control: PATCH without If-Match under require_if_match = %d, want 412", w.Code)
	}
	if w := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+a, `"1"`, map[string]any{"status": "maintenance"}); w.Code != http.StatusOK {
		t.Fatalf("precondition PATCH: %d %s", w.Code, w.Body.String())
	}
	_, va, _ := e.state(a)
	_, vb, _ := e.state(b)
	auditsBefore := count(statusAudits, []string{a, b}, e.user)
	w := e.do(e.ctx, http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": ci.BulkSetStatus, "ids": []string{a, b}, "status": "inactive"})
	var res ci.BulkResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("bulk without If-Match under require_if_match: %d %s, want 200", w.Code, w.Body.String())
	}
	if res.Succeeded != 2 || res.Failed != 0 || res.Items[0] != (ci.BulkItemResult{ID: a, Result: "ok", Code: http.StatusOK}) ||
		res.Items[1] != (ci.BulkItemResult{ID: b, Result: "ok", Code: http.StatusOK}) {
		t.Errorf("report: %+v", res)
	}
	for id, v0 := range map[string]int64{a: va, b: vb} {
		if s, v, o := e.state(id); s != "inactive" || v != v0+1 || !o {
			t.Errorf("%s after bulk: status %s version %d override %v, want inactive, %d, override", id, s, v, o, v0+1)
		}
		if n := count(bulkOverrides, []string{id}, "inactive", e.user); n != 1 {
			t.Errorf("%s: %d protected status overrides of the bulk change by the user, want 1", id, n)
		}
		if n := count(statusChanges, []string{id}, "inactive", v0+1, e.user); n != 1 {
			t.Errorf("%s: %d ci_change rows of version %d, want 1", id, n, v0+1)
		}
	}
	if n := count(statusAudits, []string{a, b}, e.user) - auditsBefore; n != 2 {
		t.Errorf("audit entries of the bulk change: %d, want 2 (one per CI)", n)
	}

	// The version read before the bulk change is stale for status.
	for id, v0 := range map[string]int64{a: va, b: vb} {
		stale := `"` + strconv.FormatInt(v0, 10) + `"`
		pw := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+id, stale, map[string]any{"status": "active"})
		if pw.Code != http.StatusConflict || !strings.Contains(pw.Body.String(), `"fields":["status"]`) {
			t.Errorf("%s: PATCH status with the pre-bulk If-Match %s = %d %s, want 409 fields=[status]", id, stale, pw.Code, pw.Body.String())
		}
		if s, _, _ := e.state(id); s != "inactive" {
			t.Errorf("%s: status after the stale PATCH = %s, want the bulk value inactive", id, s)
		}
	}

	// Negative: an automated write of status stays dropped under the bulk
	// override (OVR-01).
	active, snmp := ci.StatusActive, ci.SourceSNMP
	if _, err := ci.NewPGRepository(e.f.App).Update(e.ctx, e.f.OrgA, b, ci.UpdateRequest{Status: &active, DiscoverySource: &snmp}); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := e.state(b); s != "inactive" {
		t.Errorf("discovery overwrote the bulk status: %s", s)
	}

	// From 100 ids on: the job, with the creator as author.
	ids := e.seedCIs("bulk-manual-job-", ci.BulkJobFromIDs)
	job := e.waitTerminal(e.startJob(e.ctx, ids, "maintenance"), 60*time.Second)
	if job.Status != jobs.StatusSucceeded || job.CreatedBy != e.user {
		t.Fatalf("bulk job ended %s (%s), created by %q", job.Status, job.Error, job.CreatedBy)
	}
	if err := json.Unmarshal(job.Result, &res); err != nil || res.Succeeded != ci.BulkJobFromIDs || res.Failed != 0 {
		t.Fatalf("job result: %+v %v", res, err)
	}
	if n := e.countStatus(ids, "maintenance"); n != ci.BulkJobFromIDs {
		t.Errorf("job: %d CIs with status maintenance, want %d", n, ci.BulkJobFromIDs)
	}
	if n := count(`SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND version = 2`, ids); n != ci.BulkJobFromIDs {
		t.Errorf("job: %d CIs at version 2, want %d", n, ci.BulkJobFromIDs)
	}
	if n := count(bulkOverrides, ids, "maintenance", e.user); n != ci.BulkJobFromIDs {
		t.Errorf("job: %d protected status overrides by the creator, want %d", n, ci.BulkJobFromIDs)
	}
	if n := count(statusChanges, ids, "maintenance", 2, e.user); n != ci.BulkJobFromIDs {
		t.Errorf("job: %d ci_change rows of version 2, want %d", n, ci.BulkJobFromIDs)
	}
	if n := count(statusAudits, ids, e.user); n != ci.BulkJobFromIDs {
		t.Errorf("job: %d audit entries, want %d", n, ci.BulkJobFromIDs)
	}
	stale := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+ids[0], `"1"`, map[string]any{"status": "active"})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"fields":["status"]`) {
		t.Errorf("job: PATCH status with If-Match \"1\" = %d %s, want 409 fields=[status]", stale.Code, stale.Body.String())
	}
}

// TestAuditT3BulkStatusIsManualWrite covers WP-368 (CI-04, OVR-01, API-07;
// F-020, audit WP-057/WP-061): a bulk status change is a manual write. It
// is applied over an existing manual status override instead of being
// dropped and reported ok, records a manual override with the initiating
// user, and raises the version so a stale PATCH of status conflicts (409).
// Harness: audit_t3_ci_harness_test.go.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<DB>?sslmode=disable \
//	  go test -count=1 -run 'TestAuditT3BulkStatusIsManualWrite|TestAuditT7BulkReportMatchesStoredStatus' ./internal/ci/
func TestAuditT3BulkStatusIsManualWrite(t *testing.T) {
	r := newAuditT3CIRouter(t, "6a")
	bulk := func(id, status string) (*httptest.ResponseRecorder, ci.BulkResult) {
		t.Helper()
		w := r.call(http.MethodPost, "/api/v1/cis/bulk", "", `{"action":"set_status","ids":["`+id+`"],"status":"`+status+`"}`)
		var res ci.BulkResult
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatalf("bulk body: %v (%s)", err, w.Body.String())
			}
		}
		return w, res
	}

	t.Run("applied over an existing manual status override", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-bulk-ovr")
		if w := r.call(http.MethodPatch, "/api/v1/cis/"+id, "", `{"status":"inactive"}`); w.Code != http.StatusOK {
			t.Fatalf("precondition PATCH status: %d %s", w.Code, w.Body.String())
		}
		if n := r.str(`SELECT count(*)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = 'status' AND override_at IS NOT NULL`, id); n != "1" {
			t.Fatalf("precondition: status override rows = %s, want 1", n)
		}
		w, res := bulk(id, "active")
		if w.Code != http.StatusOK || res.Succeeded != 1 {
			t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT status FROM ci WHERE id = $1`, id); got != "active" {
			t.Errorf("bulk set_status reported %q for the CI but ci.status = %q: the manual bulk change was silently dropped (want 'active', or a non-ok item result)",
				res.Items[0].Result, got)
		}
	})

	t.Run("records a manual override with the initiating user", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-bulk-new")
		if w, res := bulk(id, "maintenance"); w.Code != http.StatusOK || res.Succeeded != 1 {
			t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT status FROM ci WHERE id = $1`, id); got != "maintenance" {
			t.Fatalf("precondition: ci.status = %q, want maintenance", got)
		}
		got := r.str(`SELECT count(*)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = 'status'
			AND override_at IS NOT NULL AND override_author = $2`, id, r.user)
		if got != "1" {
			t.Errorf("manual bulk set_status left %s override rows authored by the user, want 1 (OVR-01: every manual field change is an override with author and time)", got)
		}
	})

	t.Run("raises the version so a stale PATCH of status conflicts", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-bulk-ver")
		v0 := r.version(id)
		if w, res := bulk(id, "maintenance"); w.Code != http.StatusOK || res.Succeeded != 1 {
			t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
		}
		if v1 := r.version(id); v1 != v0+1 {
			t.Errorf("version after manual bulk set_status = %d, want %d (API-07: manual write raises the version)", v1, v0+1)
		}
		w := r.call(http.MethodPatch, "/api/v1/cis/"+id, auditT3Tag(v0), `{"status":"active"}`)
		if w.Code != http.StatusConflict {
			t.Errorf("PATCH status with the pre-bulk If-Match %s = %d, want 409 (the stale edit silently reverted the bulk status)", auditT3Tag(v0), w.Code)
		}
		if got := r.str(`SELECT status FROM ci WHERE id = $1`, id); got != "maintenance" {
			t.Errorf("ci.status after the stale PATCH = %q, want the bulk value maintenance", got)
		}
	})
}

// TestAuditT7BulkReportMatchesStoredStatus covers WP-368 (API-08, BLK-01,
// OVR-01, API-07; F-020, audit WP-181): an id reported ok carries the
// requested status in the database, synchronously and in the bulk job
// (100 ids, report = job result), also for a CI with a manual status
// override; the bulk change raises the version, leaves a protected
// override, and a PATCH with the If-Match read before conflicts. Harness:
// audit_t7_bulk_harness_test.go.
func TestAuditT7BulkReportMatchesStoredStatus(t *testing.T) {
	e := newAuditT7BulkEnv(t, "01")

	t.Run("synchronous bulk over a CI with a manual status override", func(t *testing.T) {
		a := e.f.CI(t, e.f.OrgA, e.f.Client1, "audit-t7-bulk-a")
		b := e.f.CI(t, e.f.OrgA, e.f.Client1, "audit-t7-bulk-b")
		if w := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+a, "", map[string]any{"status": "maintenance"}); w.Code != http.StatusOK {
			t.Fatalf("precondition PATCH: %d %s", w.Code, w.Body.String())
		}
		if s, _, o := e.state(a); s != "maintenance" || !o {
			t.Fatalf("precondition: a status=%s override=%v, want maintenance with override", s, o)
		}
		w := e.do(e.ctx, http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": ci.BulkSetStatus, "ids": []string{a, b}, "status": "inactive"})
		if w.Code != http.StatusOK {
			t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
		}
		var res ci.BulkResult
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		e.checkReport(t, res, "inactive", map[string]string{a: "CI with override (sync)", b: "CI without override (sync)"})
	})

	t.Run("bulk job (100 ids) over a CI with a manual status override", func(t *testing.T) {
		ids := e.seedCIs("audit-t7-bulkjob-", ci.BulkJobFromIDs)
		over := ids[37]
		if w := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+over, "", map[string]any{"status": "maintenance"}); w.Code != http.StatusOK {
			t.Fatalf("precondition PATCH: %d %s", w.Code, w.Body.String())
		}
		job := e.waitTerminal(e.startJob(e.ctx, ids, "inactive"), 60*time.Second)
		if job.Status != jobs.StatusSucceeded {
			t.Fatalf("bulk job ended %s (%s)", job.Status, job.Error)
		}
		var res ci.BulkResult
		if err := json.Unmarshal(job.Result, &res); err != nil {
			t.Fatalf("job result: %v", err)
		}
		e.checkReport(t, res, "inactive", map[string]string{over: "CI with override (job)", ids[0]: "CI without override (job)"})
	})

	t.Run("bulk set_status is a manual write: version+1, protected override, stale If-Match conflicts", func(t *testing.T) {
		c := e.f.CI(t, e.f.OrgA, e.f.Client1, "audit-t7-bulk-c")
		_, v0, _ := e.state(c)
		w := e.do(e.ctx, http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": ci.BulkSetStatus, "ids": []string{c}, "status": "maintenance"})
		if w.Code != http.StatusOK {
			t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
		}
		s, v1, o := e.state(c)
		if s != "maintenance" {
			t.Fatalf("precondition: status after bulk = %s", s)
		}
		if v1 != v0+1 {
			t.Errorf("version after bulk set_status = %d, want %d (API-07: every manual change raises the version)", v1, v0+1)
		}
		if !o {
			t.Errorf("no protected status override after the bulk change (OVR-01: a manual field change is a protected override; the next discovery run may overwrite it)")
		}
		stale := `"` + strconv.FormatInt(v0, 10) + `"`
		pw := e.do(e.ctx, http.MethodPatch, "/api/v1/cis/"+c, stale, map[string]any{"status": "active"})
		if pw.Code != http.StatusConflict && pw.Code != http.StatusPreconditionFailed {
			t.Errorf("PATCH with the pre-bulk If-Match %s = %d, want 409/412 (the stale edit silently reverts the bulk change)", stale, pw.Code)
		}
	})
}
