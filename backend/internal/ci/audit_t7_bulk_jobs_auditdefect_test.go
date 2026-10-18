//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T7-ui: DEFECT tests (they fail until the
// product is fixed). PostgreSQL integration through the production CI and
// job handlers (ci.NewHandler + jobs.NewAPIHandler on the PG repositories).
//
// WP-181 (API-08, BLK-01) P1 "Bulk set_status silently skips CIs with a
// manual status override and reports them as ok": runBulk (bulk.go) calls
// Service.Update without req.Manual; PGRepository.Update drops the
// overridden status (dropOverriddenFields) and returns the unchanged CI
// without an error, so the per-ID report says ok/200 while ci.status keeps
// the old value. Covered for the synchronous path (< 100 ids) and the job
// path (>= 100 ids, report = job result).
// WP-181 (API-08; API-07, OVR-01) P2 "Bulk changes are not manual writes: no
// version bump, no protected override" (a stale If-Match PATCH still wins).
// WP-181/WP-182 (API-09, JOB-03) P2 "Cancelling a running bulk job does not
// stop it and discards its report": jobs.Service.Cancel only flips the
// status; runBulkJob never checks it, changes all 500 CIs, and the later
// Succeed is refused, so the per-ID report is lost.
// WP-182 (API-09, JOB-03; TEN-09) P2 "Job API ignores client scope": a
// client-2 principal lists a client-1 bulk job, reads its result with the
// client-1 CI ids and may cancel/revert it (handler.go List/Get/Result and
// mayControl filter by organization only).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_04?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT7Bulk|TestAuditT7JobAPI' ./internal/ci/

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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// auditT7BulkEnv wires the production CI and job handlers on PostgreSQL.
type auditT7BulkEnv struct {
	t      *testing.T
	f      *scopetest.Fixture
	mux    *chi.Mux
	jobSvc *jobs.Service
	user   string
	// ctx is a client-1 user (the creator of the bulk jobs).
	ctx context.Context
}

func newAuditT7BulkEnv(t *testing.T, tag string) *auditT7BulkEnv {
	t.Helper()
	f := scopetest.Seed(t, tag)
	user := f.AppUser(t, f.OrgA, "audit-t7-bulk")
	scope := database.OrgWideScope(f.OrgA, user)
	scope.Clients = database.ScopeIDs(f.Client1)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user, ClientID: f.Client1})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}})
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithBulkJobs(jobSvc)).RegisterRoutes(mux)
	jobs.NewAPIHandler(jobSvc).WithKind(ci.BulkJobKind, identity.PermCIWrite, nil).RegisterRoutes(mux)
	return &auditT7BulkEnv{t: t, f: f, mux: mux, jobSvc: jobSvc, user: user, ctx: ctx}
}

func (e *auditT7BulkEnv) do(ctx context.Context, method, path, ifMatch string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// state reads status, version and whether a status override exists.
func (e *auditT7BulkEnv) state(id string) (status string, version int64, override bool) {
	e.t.Helper()
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status, version,
		EXISTS (SELECT 1 FROM ci_field_value v WHERE v.ci_id = ci.id AND v.field_name = 'status' AND v.override_at IS NOT NULL)
		FROM ci WHERE id = $1`, id).Scan(&status, &version, &override); err != nil {
		e.t.Fatalf("read ci %s: %v", id, err)
	}
	return status, version, override
}

// seedCIs inserts n active client-1 server CIs and returns their ids.
func (e *auditT7BulkEnv) seedCIs(prefix string, n int) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), $3 || g, 'active'
		FROM generate_series(1, $4::int) g RETURNING id::text`, e.f.OrgA, e.f.Client1, prefix, n)
	if err != nil {
		e.t.Fatalf("seed cis: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != n {
		e.t.Fatalf("seeded %d cis, want %d", len(ids), n)
	}
	return ids
}

func (e *auditT7BulkEnv) countStatus(ids []string, status string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND status = $2`, ids, status).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// startJob posts a bulk set_status that runs as a job and returns its id.
func (e *auditT7BulkEnv) startJob(ctx context.Context, ids []string, status string) string {
	e.t.Helper()
	w := e.do(ctx, http.MethodPost, "/api/v1/cis/bulk", "", map[string]any{"action": ci.BulkSetStatus, "ids": ids, "status": status})
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("bulk with %d ids: %d %s, want 202", len(ids), w.Code, w.Body.String())
	}
	var acc struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &acc); err != nil || acc.JobID == "" {
		e.t.Fatalf("bulk 202 body %s: %v", w.Body.String(), err)
	}
	return acc.JobID
}

func (e *auditT7BulkEnv) waitTerminal(jobID string, within time.Duration) *jobs.Job {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for {
		j, err := e.jobSvc.Get(e.ctx, e.f.OrgA, jobID)
		if err == nil && j != nil && j.Status.Terminal() {
			return j
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job %s not terminal after %s (last %+v, err %v)", jobID, within, j, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// checkReport asserts the per-ID report against the stored status: an item
// reported ok must carry the target status in the database (API-08 "result
// per id"; BLK-01 partial failure report).
func (e *auditT7BulkEnv) checkReport(t *testing.T, res ci.BulkResult, target string, focus map[string]string) {
	t.Helper()
	for _, item := range res.Items {
		label, watched := focus[item.ID]
		if !watched {
			continue
		}
		got, _, _ := e.state(item.ID)
		if item.Result == "ok" && got != target {
			t.Errorf("%s: bulk report says %s/%d but ci.status = %q, want %q (the manual bulk change must win over the earlier manual override, or the item must report an error)",
				label, item.Result, item.Code, got, target)
		}
	}
}

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

func TestAuditT7BulkJobCancelStopsWork(t *testing.T) {
	e := newAuditT7BulkEnv(t, "02")
	ids := e.seedCIs("audit-t7-cancel-", ci.MaxBulkIDs)
	jobID := e.startJob(e.ctx, ids, "inactive")

	// Cancel while the job is running: after its first changes.
	deadline := time.Now().Add(20 * time.Second)
	for e.countStatus(ids, "inactive") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the bulk job changed no CI within 20 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cw := e.do(e.ctx, http.MethodDelete, "/api/v1/jobs/"+jobID, "", nil)
	atCancel := e.countStatus(ids, "inactive")
	if atCancel >= len(ids) {
		t.Fatalf("precondition: the job finished before the cancel (%d changed)", atCancel)
	}
	t.Logf("DELETE /api/v1/jobs/{id} -> %d with %d of %d CIs changed", cw.Code, atCancel, len(ids))

	// Let the background run settle: the changed count stays stable.
	last, stableSince := -1, time.Now()
	for settle := time.Now().Add(60 * time.Second); time.Now().Before(settle); time.Sleep(100 * time.Millisecond) {
		n := e.countStatus(ids, "inactive")
		if n != last {
			last, stableSince = n, time.Now()
		}
		if n == len(ids) || time.Since(stableSince) > 2*time.Second {
			break
		}
	}
	job, err := e.jobSvc.Get(e.ctx, e.f.OrgA, jobID)
	if err != nil {
		t.Fatal(err)
	}

	switch cw.Code {
	case http.StatusConflict:
		// The alternative of the requirement: a non-interruptible running
		// job refuses the cancel and completes with its report.
		final := e.waitTerminal(jobID, 60*time.Second)
		if final.Status != jobs.StatusSucceeded || len(final.Result) == 0 {
			t.Errorf("cancel refused with 409, but the job ended %s with result_len=%d", final.Status, len(final.Result))
		}
	case http.StatusOK:
		if last >= len(ids) {
			t.Errorf("after DELETE /jobs/{id} answered 200 'cancelled' (%d changed at that moment), the job still changed all %d CIs (JOB-03: a cancelled job stops at the next checkpoint)", atCancel, last)
		}
		if job.Status != jobs.StatusCancelled {
			t.Errorf("job status = %s, want cancelled", job.Status)
		}
		var res ci.BulkResult
		if len(job.Result) == 0 || json.Unmarshal(job.Result, &res) != nil || len(res.Items) == 0 {
			t.Errorf("the cancelled job keeps no per-ID report (result_len=%d) although %d CIs were changed (BLK-01: result per id)", len(job.Result), last)
		}
	default:
		t.Fatalf("DELETE /jobs/{id}: %d %s", cw.Code, cw.Body.String())
	}
}

func TestAuditT7JobAPIHonoursClientScope(t *testing.T) {
	e := newAuditT7BulkEnv(t, "03")
	ids := e.seedCIs("audit-t7-scope-job-", ci.BulkJobFromIDs)
	jobID := e.startJob(e.ctx, ids, "inactive")
	if j := e.waitTerminal(jobID, 60*time.Second); j.Status != jobs.StatusSucceeded {
		t.Fatalf("bulk job ended %s (%s)", j.Status, j.Error)
	}

	// Control: an org-wide principal sees the job.
	adminUser := e.f.AppUser(t, e.f.OrgA, "audit-t7-orgadmin")
	orgScope := database.OrgWideScope(e.f.OrgA, adminUser)
	orgCtx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &orgScope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: adminUser})
	orgCtx = identity.WithPrincipal(orgCtx, identity.Principal{Subject: adminUser, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}})
	if w := e.do(orgCtx, http.MethodGet, "/api/v1/jobs/"+jobID, "", nil); w.Code != http.StatusOK {
		t.Fatalf("control: org-wide GET /jobs/{id} = %d %s", w.Code, w.Body.String())
	}

	// A client-2 principal with the same permissions.
	other := e.f.AppUser(t, e.f.OrgA, "audit-t7-client2")
	c2 := database.OrgWideScope(e.f.OrgA, other)
	c2.Clients = database.ScopeIDs(e.f.Client2)
	ctx2 := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &c2),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: other, ClientID: e.f.Client2})
	ctx2 = identity.WithPrincipal(ctx2, identity.Principal{Subject: other, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}})

	if w := e.do(ctx2, http.MethodGet, "/api/v1/cis?limit=200", "", nil); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "audit-t7-scope-job-") {
		t.Fatalf("precondition: the client-2 principal lists CIs (%d) without the client-1 CIs: %.300s", w.Code, w.Body.String())
	}
	if w := e.do(ctx2, http.MethodGet, "/api/v1/cis/"+ids[0], "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("precondition: GET of a client-1 CI by the client-2 principal = %d, want 404", w.Code)
	}

	leaked := func(body string) int {
		n := 0
		for _, id := range ids {
			if strings.Contains(body, id) {
				n++
			}
		}
		return n
	}
	for _, tc := range []struct {
		name, method, path string
	}{
		{"GET /jobs?kind=ci.bulk", http.MethodGet, "/api/v1/jobs?kind=" + ci.BulkJobKind},
		{"GET /jobs/{id}", http.MethodGet, "/api/v1/jobs/" + jobID},
		{"GET /jobs/{id}/result", http.MethodGet, "/api/v1/jobs/" + jobID + "/result"},
		{"DELETE /jobs/{id}", http.MethodDelete, "/api/v1/jobs/" + jobID},
		{"POST /jobs/{id}/revert", http.MethodPost, "/api/v1/jobs/" + jobID + "/revert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(ctx2, tc.method, tc.path, "", nil)
			body := w.Body.String()
			if strings.HasPrefix(tc.name, "GET /jobs?") {
				if w.Code != http.StatusOK {
					t.Fatalf("list: %d %s", w.Code, body)
				}
				if strings.Contains(body, jobID) {
					t.Errorf("a client-2 principal lists the client-1 bulk job %s (TEN-09: the job channel is scoped like the CIs)", jobID)
				}
				return
			}
			if w.Code != http.StatusNotFound {
				t.Errorf("%s by a client-2 principal = %d, want 404 like the client-1 CIs themselves (%d client-1 CI ids in the body)", tc.name, w.Code, leaked(body))
			}
			if n := leaked(body); n > 0 {
				t.Errorf("%s leaks %d client-1 CI ids to a client-2 principal", tc.name, n)
			}
		})
	}
}
