//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T7-ui: DEFECT tests (they fail until the
// product is fixed). PostgreSQL integration through the production CI and
// job handlers; the harness (auditT7BulkEnv) is in
// audit_t7_bulk_harness_test.go. The bulk status change of WP-181 (F-020:
// CIs with a manual status override skipped and reported ok, no version
// bump, no protected override) is fixed by WP-368; its test
// TestAuditT7BulkReportMatchesStoredStatus is in bulk_integration_test.go.
// The client scope of the job API (WP-182, F-163) is fixed by WP-404; its
// test TestAuditT7JobAPIHonoursClientScope is in
// bulk_jobs_integration_test.go.
//
// WP-181/WP-182 (API-09, JOB-03) P2 "Cancelling a running bulk job does not
// stop it and discards its report": jobs.Service.Cancel only flips the
// status; runBulkJob never checks it, changes all 500 CIs, and the later
// Succeed is refused, so the per-ID report is lost.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_04?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run '^TestAuditT7BulkJobCancelStopsWork$' ./internal/ci/

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
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
