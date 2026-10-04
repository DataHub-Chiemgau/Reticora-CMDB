package export_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestExportJobRunsWithCreatorScope covers WP-035 (EXP-01, TEN-06): an
// export job records its creator and the creator's scope, the worker renders
// it with exactly that scope, only the creator reads the job, and a queued
// job without a snapshot is failed instead of exported org-wide.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestExportJobRunsWithCreatorScope(t *testing.T) {
	f := scopetest.Seed(t, "4f")
	bg := context.Background()
	f.CI(t, f.OrgA, f.Client1, "export-scope-own")
	f.CI(t, f.OrgA, f.Client2, "export-scope-foreign")
	other := f.AppUser(t, f.OrgA, "export-other")

	jobs := export.NewPGJobRepository(f.App)
	blobs := blob.NewFileStore(t.TempDir())
	worker := export.NewJobWorker(jobs, ci.NewPGRepository(f.App), blobs)
	mux := chi.NewRouter()
	export.NewJobHandler(jobs, worker, blobs).RegisterRoutes(mux)

	request := func(method, path, body, userID string, scope *database.TenantScope) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userID})
		req = req.WithContext(database.ContextWithTenantScope(ctx, scope))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	otherScope := database.OrgWideScope(f.OrgA, other)

	create := func(scope *database.TenantScope) string {
		t.Helper()
		w := request(http.MethodPost, "/api/v1/export/jobs", `{"format":"json"}`, scope.UserID, scope)
		if w.Code != http.StatusAccepted {
			t.Fatalf("create job: status %d: %s", w.Code, w.Body.String())
		}
		var job struct {
			ID    string           `json:"id"`
			Scope *export.JobScope `json:"scope"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatalf("decode job: %v", err)
		}
		if job.Scope == nil {
			t.Fatal("created job carries no scope snapshot")
		}
		return job.ID
	}
	restrictedJob := create(&client1)
	orgWideJob := create(&orgWide)

	// A queued job from before migration 000068 has no snapshot.
	var legacyJob string
	if err := f.Admin.QueryRow(bg, `INSERT INTO export_job (organization_id, initiated_by, format, status) VALUES ($1, $2, 'json', 'pending') RETURNING id::text`,
		f.OrgA, f.User).Scan(&legacyJob); err != nil {
		t.Fatalf("seed legacy job: %v", err)
	}

	for worker.ProcessOne(bg) {
	}

	content := func(jobID string) (status, body string) {
		t.Helper()
		var key string
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(object_key, '') FROM export_job WHERE id = $1`, jobID).Scan(&status, &key); err != nil {
			t.Fatalf("read job %s: %v", jobID, err)
		}
		if key == "" {
			return status, ""
		}
		rc, err := blobs.Get(bg, export.ExportBucket, key)
		if err != nil {
			t.Fatalf("read export object: %v", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read export object: %v", err)
		}
		return status, string(data)
	}
	if status, out := content(restrictedJob); status != export.JobStatusCompleted ||
		!strings.Contains(out, "export-scope-own") || strings.Contains(out, "export-scope-foreign") {
		t.Errorf("client-1 job: status %s, content %s; want only the client-1 CI", status, out)
	}
	if status, out := content(orgWideJob); status != export.JobStatusCompleted ||
		!strings.Contains(out, "export-scope-own") || !strings.Contains(out, "export-scope-foreign") {
		t.Errorf("org-wide job: status %s, content %s; want both CIs", status, out)
	}
	if status, _ := content(legacyJob); status != export.JobStatusFailed {
		t.Errorf("job without scope snapshot: status %s, want failed", status)
	}

	// Only the creator reads its jobs.
	if w := request(http.MethodGet, "/api/v1/export/jobs/"+restrictedJob, "", f.User, &client1); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "download_url") {
		t.Errorf("creator reads own job: status %d body %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/export/jobs/"+restrictedJob, "", other, &otherScope); w.Code != http.StatusNotFound {
		t.Errorf("other user reads the job: status %d, want 404", w.Code)
	}
	w := request(http.MethodGet, "/api/v1/export/jobs", "", other, &otherScope)
	var list struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || list.Total != 0 {
		t.Errorf("other user lists %d jobs (status %d), want none", list.Total, w.Code)
	}
}
