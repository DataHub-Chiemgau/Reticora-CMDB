package ci_test

// WP-404 (API-09, JOB-03, TEN-09; F-163): the job API is scoped like the
// CIs of the job. A bulk job of client 1 is invisible to a principal
// restricted to client 2: GET /jobs does not list it, GET /jobs/{id},
// /result, DELETE and /revert answer 404 without the client-1 CI ids, while
// the creator and an org-wide principal see it. PostgreSQL integration
// through the production CI and job handlers; the harness (auditT7BulkEnv)
// is in audit_t7_bulk_harness_test.go.
//
// Formerly the defect test of the same name behind the auditdefect tag
// (audit_t7_bulk_jobs_auditdefect_test.go, deleted with WP-514).
// TestAuditT7BulkJobCancelStopsWork (WP-514) is at the end of this file.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_04?sslmode=disable \
//     go test -count=1 -v -run '^TestAuditT7JobAPIHonoursClientScope$' ./internal/ci/

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT7JobAPIHonoursClientScope(t *testing.T) {
	e := newAuditT7BulkEnv(t, "03")
	ids := e.seedCIs("audit-t7-scope-job-", ci.BulkJobFromIDs)
	jobID := e.startJob(e.ctx, ids, "inactive")
	if j := e.waitTerminal(jobID, 60*time.Second); j.Status != jobs.StatusSucceeded {
		t.Fatalf("bulk job ended %s (%s)", j.Status, j.Error)
	}

	principal := func(user string, scope database.TenantScope, client string) context.Context {
		ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
			tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: user, ClientID: client})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: []identity.Permission{identity.PermJobRead, identity.PermCIRead, identity.PermCIWrite}})
	}
	listed := func(ctx context.Context) bool {
		t.Helper()
		w := e.do(ctx, http.MethodGet, "/api/v1/jobs?kind="+ci.BulkJobKind, "", nil)
		var list struct {
			Data []jobs.Job `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		for _, j := range list.Data {
			if j.ID == jobID {
				return true
			}
		}
		return false
	}

	// Control: the creator (client 1) and an org-wide principal see the job
	// and its result.
	adminUser := e.f.AppUser(t, e.f.OrgA, "audit-t7-orgadmin")
	orgCtx := principal(adminUser, database.OrgWideScope(e.f.OrgA, adminUser), "")
	for name, ctx := range map[string]context.Context{"creator": e.ctx, "org-wide": orgCtx} {
		if !listed(ctx) {
			t.Errorf("control: %s does not list the bulk job", name)
		}
		for _, path := range []string{"/api/v1/jobs/" + jobID, "/api/v1/jobs/" + jobID + "/result"} {
			if w := e.do(ctx, http.MethodGet, path, "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ids[0]) {
				t.Errorf("control: %s GET %s = %d, want 200 with the CI ids", name, path, w.Code)
			}
		}
	}

	// A client-2 principal with the same permissions.
	other := e.f.AppUser(t, e.f.OrgA, "audit-t7-client2")
	c2 := database.OrgWideScope(e.f.OrgA, other)
	c2.Clients = database.ScopeIDs(e.f.Client2)
	ctx2 := principal(other, c2, e.f.Client2)

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
	if j, err := e.jobSvc.Get(e.ctx, e.f.OrgA, jobID); err != nil || j.Status != jobs.StatusSucceeded {
		t.Errorf("the bulk job after the client-2 requests: %+v %v, want succeeded", j, err)
	}
}

// TestAuditT7BulkJobCancelStopsWork covers WP-514 (JOB-03, API-08, BLK-01;
// F-162): DELETE /jobs/{id} on a running bulk job of 500 CIs answers 200
// and stops the work at the next checkpoint (at most 25 ids later, plus the
// changes under way): fewer than 500 CIs change, the job stays cancelled
// and keeps its partial report — the changed ids ok, the rest skipped —
// served by GET /jobs/{id}/result (200). It runs in the process of the
// request and, with TEST_NATS_URL (or NATS_URL), on the job queue over NATS
// JetStream, where the queue acknowledges the cancelled job without a
// status change.
//
// Formerly the defect test of WP-181/WP-182 behind the auditdefect tag
// (audit_t7_bulk_jobs_auditdefect_test.go): the cancel only flipped the
// status, the job changed all 500 CIs and the per-ID report was lost.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_04?sslmode=disable \
//	  go test -count=1 -v -run '^TestAuditT7BulkJobCancelStopsWork$' ./internal/ci/
//
// and the same with TEST_NATS_URL=nats://localhost:4222.
func TestAuditT7BulkJobCancelStopsWork(t *testing.T) {
	t.Run("in-process", func(t *testing.T) {
		checkBulkJobCancel(t, newAuditT7BulkEnv(t, "02"))
	})
	t.Run("job queue on NATS", func(t *testing.T) {
		url := os.Getenv("TEST_NATS_URL")
		if url == "" {
			url = os.Getenv("NATS_URL")
		}
		if url == "" {
			t.Skip("TEST_NATS_URL not set")
		}
		checkBulkJobCancel(t, newAuditT7BulkEnvOn(t, "02", url))
	})
}

func checkBulkJobCancel(t *testing.T, e *auditT7BulkEnv) {
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
	if cw.Code != http.StatusOK {
		t.Fatalf("DELETE /jobs/{id} of a running ci.bulk job: %d %s, want 200", cw.Code, cw.Body.String())
	}
	t.Logf("DELETE /api/v1/jobs/{id} -> %d with %d of %d CIs changed", cw.Code, atCancel, len(ids))

	// The job keeps its report once the worker reached its checkpoint.
	var report ci.BulkResult
	var resultCode int
	for settle := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		rw := e.do(e.ctx, http.MethodGet, "/api/v1/jobs/"+jobID+"/result", "", nil)
		resultCode = rw.Code
		if rw.Code == http.StatusOK {
			if err := json.Unmarshal(rw.Body.Bytes(), &report); err != nil {
				t.Fatalf("result: %v %s", err, rw.Body.String())
			}
			break
		}
		if time.Now().After(settle) {
			t.Fatalf("GET /jobs/{id}/result of the cancelled job: %d %s, want 200 with the partial report", rw.Code, rw.Body.String())
		}
	}
	// Nothing changes any more: the count is stable.
	time.Sleep(500 * time.Millisecond)
	changed := e.countStatus(ids, "inactive")
	if again := func() int { time.Sleep(time.Second); return e.countStatus(ids, "inactive") }(); again != changed {
		t.Errorf("CIs still change after the report was kept: %d, then %d", changed, again)
	}
	t.Logf("the job stopped with %d of %d CIs changed", changed, len(ids))
	if changed >= len(ids) {
		t.Errorf("after DELETE /jobs/{id} answered 200 (%d changed at that moment), the job still changed all %d CIs (JOB-03)", atCancel, changed)
	}
	job, err := e.jobSvc.Get(e.ctx, e.f.OrgA, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobs.StatusCancelled || job.Error != "" {
		t.Errorf("job status = %s (error %q), want cancelled", job.Status, job.Error)
	}

	// The report covers every id: the changed ones ok, the rest skipped.
	if resultCode != http.StatusOK || !report.Cancelled || report.Total != len(ids) || len(report.Items) != len(ids) ||
		report.Succeeded != changed || report.Skipped != len(ids)-changed || report.Failed != 0 {
		t.Errorf("report: cancelled %v, total %d, items %d, ok %d, skipped %d, failed %d; want true, %d, %d, %d, %d, 0",
			report.Cancelled, report.Total, len(report.Items), report.Succeeded, report.Skipped, report.Failed,
			len(ids), len(ids), changed, len(ids)-changed)
	}
	byID := map[string]string{}
	for _, item := range report.Items {
		byID[item.ID] = item.Result
	}
	var stored []string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT array_agg(id::text) FROM ci WHERE id = ANY($1::uuid[]) AND status = 'inactive'`, ids).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, id := range stored {
		if byID[id] != "ok" {
			t.Errorf("CI %s changed, reported %q", id, byID[id])
		}
	}
}
