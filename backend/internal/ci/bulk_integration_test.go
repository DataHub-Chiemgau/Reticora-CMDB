package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
