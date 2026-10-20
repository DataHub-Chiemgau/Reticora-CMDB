//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T7-ui: DEFECT tests (they fail until the
// product is fixed). PostgreSQL integration through the production CI and
// job handlers; the harness (auditT7BulkEnv) is in
// audit_t7_bulk_harness_test.go. The bulk status change of WP-181 (F-020:
// CIs with a manual status override skipped and reported ok, no version
// bump, no protected override) is fixed by WP-368; its test
// TestAuditT7BulkReportMatchesStoredStatus is in bulk_integration_test.go.
//
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
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT7BulkJobCancelStopsWork|TestAuditT7JobAPI' ./internal/ci/

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

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
