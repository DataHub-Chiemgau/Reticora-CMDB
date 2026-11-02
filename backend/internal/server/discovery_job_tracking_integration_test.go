// Discovery jobs in the generic job model through the production router on
// PostgreSQL (JOB-01, REP-01, WP-502, F-146). Harness:
// audit_t6_harness_test.go.
//
//   - TestAuditDefectDiscoveryJobInGenericJobModel (formerly
//     audit_t6_router_auditdefect_test.go, WP-129, F-146): POST
//     /api/v1/discovery/jobs tracks a job discovery.sweep (ref_table
//     discovery_job, ref_id the discovery job, queued) listed by GET
//     /api/v1/jobs; UpdateJobStatus of the router's discovery repository
//     mirrors running and completed (running, succeeded); organization B
//     gets 404 for the job.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestAuditDefectDiscoveryJob' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditDefectDiscoveryJobInGenericJobModel(t *testing.T) {
	e := t6NewEnv(t, "d8", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d8-admin"), identity.AllPermissions()}
	otherOrg := t6As{f.OrgB, f.AppUser(t, f.OrgB, "audit-d8-b"), identity.AllPermissions()}
	var collector string
	if err := f.Admin.QueryRow(bg, `INSERT INTO collector (organization_id, name) VALUES ($1, 'audit-d8-collector') RETURNING id::text`, f.OrgA).Scan(&collector); err != nil {
		t.Fatal(err)
	}
	r := e.Call(t, admin, http.MethodPost, "/api/v1/discovery/jobs", `{"collector_id":"`+collector+`","job_type":"sweep"}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("POST /discovery/jobs = %d %s", r.Code, t6Short(r.Body))
	}
	discoveryJob := t6ID(r.Body)

	r = e.Call(t, admin, http.MethodGet, "/api/v1/jobs?kind=discovery.sweep", ``)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /jobs = %d %s", r.Code, t6Short(r.Body))
	}
	var list struct {
		Data []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			RefID  string `json:"ref_id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &list); err != nil {
		t.Fatalf("GET /jobs body: %v", err)
	}
	var jobID string
	for _, j := range list.Data {
		if j.RefID == discoveryJob {
			jobID = j.ID
			if j.Kind != "discovery.sweep" || j.Status != "queued" {
				t.Errorf("generic job of the discovery job: kind %q status %q, want discovery.sweep/queued", j.Kind, j.Status)
			}
		}
	}
	if jobID == "" {
		t.Fatalf("GET /api/v1/jobs?kind=discovery.sweep does not list discovery job %s (JOB-01: one job model; %d rows in table job)",
			discoveryJob, e.Count(t, `SELECT count(*) FROM job WHERE ref_id::text = $1`, discoveryJob))
	}
	if n := e.Count(t, `SELECT count(*) FROM job WHERE ref_table = 'discovery_job' AND ref_id::text = $1 AND kind = 'discovery.sweep'`, discoveryJob); n != 1 {
		t.Errorf("job rows of discovery job %s with ref_table discovery_job: %d, want 1", discoveryJob, n)
	}

	// The status of the discovery job is mirrored once committed:
	// pending → running → completed is queued → running → succeeded. The
	// test calls UpdateJobStatus of the router's discovery repository
	// directly; the collector's path over the scope channel is tested in
	// discovery/job_execution_integration_test.go (WP-632).
	repo, ok := e.Repos.Discovery.(*discovery.PGRepository)
	if !ok {
		t.Fatalf("discovery repository %T is not the PG repository", e.Repos.Discovery)
	}
	ctx := f.OrgCtx(f.OrgA)
	for _, step := range []struct{ status, want string }{
		{discovery.JobStatusRunning, "running"},
		{discovery.JobStatusCompleted, "succeeded"},
	} {
		updated, err := repo.UpdateJobStatus(ctx, f.OrgA, discoveryJob, step.status, "")
		if err != nil || updated.Status != step.status {
			t.Fatalf("UpdateJobStatus(%s) = %+v, %v", step.status, updated, err)
		}
		r = e.Call(t, admin, http.MethodGet, "/api/v1/jobs/"+jobID, ``)
		if r.Code != http.StatusOK {
			t.Fatalf("GET /jobs/%s = %d %s", jobID, r.Code, t6Short(r.Body))
		}
		var job struct {
			Status     string  `json:"status"`
			Progress   int     `json:"progress"`
			StartedAt  *string `json:"started_at"`
			FinishedAt *string `json:"finished_at"`
		}
		if err := json.Unmarshal(r.Body, &job); err != nil {
			t.Fatalf("GET /jobs/%s body: %v", jobID, err)
		}
		if job.Status != step.want || job.StartedAt == nil {
			t.Errorf("discovery job %s: generic job status %q started %v, want %s with a start", step.status, job.Status, job.StartedAt, step.want)
		}
		if step.want == "succeeded" && (job.FinishedAt == nil || job.Progress != 100) {
			t.Errorf("succeeded generic job: finished %v progress %d, want finished and 100", job.FinishedAt, job.Progress)
		}
	}
	r = e.Call(t, admin, http.MethodGet, "/api/v1/discovery/jobs/"+discoveryJob, ``)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /discovery/jobs/%s = %d %s", discoveryJob, r.Code, t6Short(r.Body))
	}
	var dj discovery.Job
	if err := json.Unmarshal(r.Body, &dj); err != nil {
		t.Fatal(err)
	}
	if dj.Status != discovery.JobStatusCompleted || dj.StartedAt == "" || dj.CompletedAt == "" {
		t.Errorf("discovery job after completion: %+v", dj)
	}

	if r = e.Call(t, otherOrg, http.MethodGet, "/api/v1/jobs/"+jobID, ``); r.Code != http.StatusNotFound {
		t.Errorf("organization B GET /jobs/%s = %d, want 404", jobID, r.Code)
	}
	// Organization B cannot move the discovery job of A either.
	if _, err := repo.UpdateJobStatus(f.OrgCtx(f.OrgB), f.OrgB, discoveryJob, discovery.JobStatusFailed, "x"); err == nil {
		t.Errorf("organization B UpdateJobStatus of A's discovery job succeeded")
	}
}
