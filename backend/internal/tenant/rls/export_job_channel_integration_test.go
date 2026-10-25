package rls_test

// WP-347 (F-059, EXP-01, TEN-06, TEN-09): the export worker mirrors export
// jobs into the generic job model; GET /api/v1/jobs, /jobs/{id},
// /jobs/{id}/result, DELETE /jobs/{id} and POST /jobs/{id}/revert hand such
// a job to its creator only, as GET /api/v1/export/jobs does. Another user
// of another client and an org-wide org_admin, both with job:read, neither
// list it nor reach it by id (404); EXP-01 defines no exception for
// administrators. Other jobs stay visible within the scope of their
// creator (WP-404).
//
// Formerly the probe TestProbeV05ExportJobsVisibleInGenericJobs (audit_db_05).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^TestExportJobsStayWithTheirCreator$' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

func TestExportJobsStayWithTheirCreator(t *testing.T) {
	f := scopetest.Seed(t, "c3")
	bg := context.Background()
	blobs := blob.NewFileStore(t.TempDir())
	env := newChannelEnvWithBlobs(t, f, blobs)
	f.CI(t, f.OrgA, f.Client1, "t1-export-own")

	userA := f.AppUser(t, f.OrgA, "export-a")
	userB := f.AppUser(t, f.OrgA, "export-b")
	admin := f.AppUser(t, f.OrgA, "export-admin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, admin); err != nil {
		t.Fatalf("role assignment: %v", err)
	}
	// channelPrincipal carries every permission, job:read included.
	a := &channelPrincipal{kind: "creator client 1", org: f.OrgA, user: userA,
		scope: identity.Scope{Clients: only(f.Client1), Sites: all(), Teams: all()}}
	b := &channelPrincipal{kind: "other user client 2", org: f.OrgA, user: userB,
		scope: identity.Scope{Clients: only(f.Client2), Sites: all(), Teams: all()}}
	orgAdmin := &channelPrincipal{kind: "org admin", org: f.OrgA, user: admin,
		scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}

	code, body := env.call(a, http.MethodPost, "/api/v1/export/jobs", map[string]string{"format": "json"})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || code != http.StatusAccepted || created.ID == "" {
		t.Fatalf("setup: create export as A: %d %s", code, trim(body))
	}
	// The worker of cmd/server with the generic job model as tracker.
	worker := export.NewJobWorker(export.NewPGJobRepository(f.App), ci.NewPGRepository(f.App), blobs)
	worker.Tracker = jobs.NewService(jobs.NewPGRepository(f.App))
	for worker.ProcessOne(bg) {
	}
	var jobID, status string
	if err := f.Admin.QueryRow(bg, `SELECT id::text, status FROM job WHERE ref_table = 'export_job' AND ref_id = $1`,
		created.ID).Scan(&jobID, &status); err != nil || status != string(jobs.StatusSucceeded) {
		t.Fatalf("setup: generic job of the export: %s %s %v", jobID, status, err)
	}
	// A job of another kind stays visible to every job:read holder whose
	// scope covers it (WP-404): one per client, with the snapshot of A and
	// of B.
	otherJobs := map[*channelPrincipal]string{}
	for p, client := range map[*channelPrincipal]string{a: f.Client1, b: f.Client2} {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO job (organization_id, kind, status, created_by, scope_recorded, scope_clients)
			VALUES ($1, 'wp347.other', 'queued', $2, true, ARRAY[$3::uuid]) RETURNING id::text`, f.OrgA, p.user, client).Scan(&id); err != nil {
			t.Fatalf("setup: other job: %v", err)
		}
		otherJobs[p] = id
	}
	otherJob := otherJobs[a]

	listed := func(p *channelPrincipal, path string) (map[string]bool, int) {
		t.Helper()
		code, body := env.call(p, http.MethodGet, path, nil)
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(body), &list); err != nil || code != http.StatusOK {
			t.Fatalf("GET %s as %s: %d %s", path, p.kind, code, trim(body))
		}
		ids := map[string]bool{}
		for _, j := range list.Data {
			ids[j.ID] = true
		}
		return ids, list.Total
	}

	// The creator sees the job, and the other job of its client only.
	if ids, _ := listed(a, "/api/v1/jobs"); !ids[jobID] || !ids[otherJob] || ids[otherJobs[b]] {
		t.Errorf("creator lists %v, want the export job %s and the other job of client 1", ids, jobID)
	}
	if code, body := env.call(a, http.MethodGet, "/api/v1/jobs/"+jobID, nil); code != http.StatusOK || !strings.Contains(body, created.ID) {
		t.Errorf("creator GET /jobs/{id}: %d %s, want 200", code, trim(body))
	}

	for p, others := range map[*channelPrincipal][]string{b: {otherJobs[b]}, orgAdmin: {otherJobs[a], otherJobs[b]}} {
		ids, _ := listed(p, "/api/v1/jobs")
		if ids[jobID] || len(ids) != len(others) {
			t.Errorf("F-059: %s lists %v, want the other jobs %v without the export job %s", p.kind, ids, others, jobID)
		}
		for _, other := range others {
			if !ids[other] {
				t.Errorf("%s does not list the other job %s", p.kind, other)
			}
			if code, body := env.call(p, http.MethodGet, "/api/v1/jobs/"+other, nil); code != http.StatusOK {
				t.Errorf("%s GET the other job: %d %s, want 200", p.kind, code, trim(body))
			}
		}
		if ids, total := listed(p, "/api/v1/jobs?kind=export"); len(ids) != 0 || total != 0 {
			t.Errorf("F-059: %s lists %d export jobs %v, want none", p.kind, total, ids)
		}
		for _, req := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/jobs/" + jobID},
			{http.MethodGet, "/api/v1/jobs/" + jobID + "/result"},
			{http.MethodDelete, "/api/v1/jobs/" + jobID},
			{http.MethodPost, "/api/v1/jobs/" + jobID + "/revert"},
		} {
			code, body := env.call(p, req.method, req.path, nil)
			if code != http.StatusNotFound || strings.Contains(body, created.ID) || strings.Contains(body, userA) {
				t.Errorf("F-059: %s %s %s: %d %s, want 404 without the job", p.kind, req.method, req.path, code, trim(body))
			}
		}
	}
	if code, body := env.call(b, http.MethodGet, "/api/v1/jobs/"+otherJob, nil); code != http.StatusNotFound {
		t.Errorf("%s GET the other job of client 1: %d %s, want 404", b.kind, code, trim(body))
	}
	// Nothing above changed the job.
	if err := f.Admin.QueryRow(bg, `SELECT status FROM job WHERE id = $1`, jobID).Scan(&status); err != nil || status != string(jobs.StatusSucceeded) {
		t.Errorf("the export job is now %s (%v), want succeeded", status, err)
	}
}
