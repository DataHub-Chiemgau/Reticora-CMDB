package export_test

// WP-515 (JOB-03; F-162 export part, F-347 backend part): an export is
// cancelled through the generic job API (DELETE /api/v1/jobs/{id} on the
// job the export mirrors), and the mirrored job of a finished export has a
// result. PostgreSQL integration through the production handlers: export
// jobs (export.NewJobHandler), the job API (jobs.NewAPIHandler, routes
// authorized like the server, server.AuthorizeRoute) and the export worker
// with the job service as tracker, the export kind registered as in
// server/jobkinds.go.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestExportCancelViaJobAPI' ./internal/export/

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// cancellingStore is a blob store that runs onPut after each stored object:
// the cancel arrives while the export runs, after its object was written.
type cancellingStore struct {
	blob.Store
	onPut func()
}

func (s *cancellingStore) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	if err := s.Store.Put(ctx, bucket, key, r, size, contentType); err != nil {
		return err
	}
	if s.onPut != nil {
		s.onPut()
	}
	return nil
}

// TestExportCancelViaJobAPI: a DELETE /api/v1/jobs/{id} on the mirrored job
// of a queued export stops the export — the export job is not completed but
// failed with "cancelled by user", no object is stored, no export.completed
// is reported, the mirrored job stays cancelled; a cancel while the export
// runs, after its object was stored, deletes the object. Without a cancel
// the mirrored job succeeds with the result {export_id, row_count} (GET
// /jobs/{id}/result 200, F-347). The DELETE is refused without the route
// permission job:read (403) and is not found for a user of another client
// or another user of the same client (404: the creator rule of WP-347, the
// scope of WP-404); the job stays queued.
func TestExportCancelViaJobAPI(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	f.CI(t, f.OrgA, f.Client1, "export-cancel-ci")
	colleague := f.AppUser(t, f.OrgA, "export-cancel-colleague")
	foreign := f.AppUser(t, f.OrgA, "export-cancel-foreign")

	exportJobs := export.NewPGJobRepository(f.App)
	store := &cancellingStore{Store: blob.NewFileStore(t.TempDir())}
	worker := export.NewJobWorker(exportJobs, ci.NewPGRepository(f.App), store)
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	// As registered by the server (server/jobkinds.go).
	jobSvc.Register(jobs.Kind{Name: "export", MaxAttempts: 1, Cancellable: true})
	worker.Tracker = jobSvc
	// completedEvents counts the export.completed events of the
	// organization in the outbox: CompleteJob writes them with the
	// completion (WP-550).
	completedEvents := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = $2`,
			f.OrgA, events.ExportCompleted).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	mux := chi.NewRouter()
	export.NewJobHandler(exportJobs, worker, store).RegisterRoutes(mux)
	jobAPI := jobs.NewAPIHandler(jobSvc).WithKind("export", identity.PermExportRun, nil)
	for _, route := range []struct {
		method, pattern string
		handler         http.HandlerFunc
	}{
		{http.MethodGet, "/api/v1/jobs", jobAPI.List},
		{http.MethodGet, "/api/v1/jobs/{id}", jobAPI.Get},
		{http.MethodGet, "/api/v1/jobs/{id}/result", jobAPI.Result},
		{http.MethodDelete, "/api/v1/jobs/{id}", jobAPI.Cancel},
	} {
		mux.With(server.AuthorizeRoute(route.method, route.pattern)).Method(route.method, route.pattern, route.handler)
	}

	all := []identity.Permission{identity.PermExportRun, identity.PermJobRead, identity.PermCIRead}
	principal := func(user, client string, perms ...identity.Permission) context.Context {
		scope := database.OrgWideScope(f.OrgA, user)
		scope.Clients = database.ScopeIDs(client)
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user, ClientID: client})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Permissions: perms})
	}
	creator := principal(f.User, f.Client1, all...)
	do := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body))).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// start queues an export of the creator and returns its id and the id
	// of the job mirroring it.
	start := func() (exportID, jobID string) {
		t.Helper()
		w := do(creator, http.MethodPost, "/api/v1/export/jobs", `{"format":"json"}`)
		if w.Code != http.StatusAccepted {
			t.Fatalf("create export: %d %s", w.Code, w.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		w = do(creator, http.MethodGet, "/api/v1/jobs?kind=export&limit=100", "")
		var list struct {
			Data []jobs.Job `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK {
			t.Fatalf("list jobs: %d %s", w.Code, w.Body.String())
		}
		for _, j := range list.Data {
			if j.RefTable == "export_job" && j.RefID == created.ID {
				if j.Status != jobs.StatusQueued || !j.Cancellable {
					t.Fatalf("mirrored job of the new export: %+v, want queued and cancellable", j)
				}
				return created.ID, j.ID
			}
		}
		t.Fatalf("no job mirrors the export %s", created.ID)
		return "", ""
	}
	type exportState struct{ status, message, key string }
	stateOf := func(id string) exportState {
		t.Helper()
		var s exportState
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(error_message, ''), COALESCE(object_key, '') FROM export_job WHERE id = $1`,
			id).Scan(&s.status, &s.message, &s.key); err != nil {
			t.Fatalf("read export job %s: %v", id, err)
		}
		return s
	}
	mirrored := func(id string) jobs.Job {
		t.Helper()
		w := do(creator, http.MethodGet, "/api/v1/jobs/"+id, "")
		var j jobs.Job
		if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || w.Code != http.StatusOK {
			t.Fatalf("read job %s: %d %s", id, w.Code, w.Body.String())
		}
		return j
	}
	// stored reports whether the object of the export is in the store.
	stored := func(exportID string) bool {
		t.Helper()
		rc, err := store.Get(bg, export.ExportBucket, export.ObjectKey(&export.Job{ID: exportID, OrganizationID: f.OrgA, Format: "json"}))
		if err != nil {
			return false
		}
		_ = rc.Close()
		return true
	}
	drain := func() {
		for worker.ProcessOne(bg) {
		}
	}

	t.Run("cancel before the worker runs", func(t *testing.T) {
		exportID, jobID := start()
		reported := completedEvents()
		for _, c := range []struct {
			name string
			ctx  context.Context
			want int
		}{
			{"a user without job:read", principal(colleague, f.Client1, identity.PermExportRun, identity.PermCIRead), http.StatusForbidden},
			{"a user of another client", principal(foreign, f.Client2, all...), http.StatusNotFound},
			{"another user of the client", principal(colleague, f.Client1, all...), http.StatusNotFound},
		} {
			if w := do(c.ctx, http.MethodDelete, "/api/v1/jobs/"+jobID, ""); w.Code != c.want {
				t.Errorf("DELETE by %s = %d %s, want %d", c.name, w.Code, w.Body.String(), c.want)
			}
		}
		if j := mirrored(jobID); j.Status != jobs.StatusQueued {
			t.Fatalf("job after the refused cancels: %s, want queued", j.Status)
		}
		if w := do(creator, http.MethodDelete, "/api/v1/jobs/"+jobID, ""); w.Code != http.StatusOK {
			t.Fatalf("DELETE by the creator = %d %s, want 200", w.Code, w.Body.String())
		}
		drain()
		if s := stateOf(exportID); s.status == export.JobStatusCompleted || s.status != export.JobStatusFailed ||
			s.message != export.CancelledByUser || s.key != "" {
			t.Errorf("export job after the cancel: %+v, want failed with %q and no object", s, export.CancelledByUser)
		}
		if stored(exportID) {
			t.Error("the cancelled export stored its object")
		}
		if n := completedEvents() - reported; n != 0 {
			t.Errorf("the cancelled export reported %d events, want none", n)
		}
		if j := mirrored(jobID); j.Status != jobs.StatusCancelled || len(j.Result) != 0 {
			t.Errorf("mirrored job after the worker ran: %+v, want cancelled without result", j)
		}
		if w := do(creator, http.MethodGet, "/api/v1/jobs/"+jobID+"/result", ""); w.Code != http.StatusConflict {
			t.Errorf("result of the cancelled export = %d %s, want 409", w.Code, w.Body.String())
		}
	})

	t.Run("cancel after the object was stored", func(t *testing.T) {
		exportID, jobID := start()
		reported := completedEvents()
		var cancel int
		store.onPut = func() {
			cancel = do(creator, http.MethodDelete, "/api/v1/jobs/"+jobID, "").Code
		}
		drain()
		store.onPut = nil
		if cancel != http.StatusOK {
			t.Fatalf("DELETE of the running export = %d, want 200", cancel)
		}
		if s := stateOf(exportID); s.status != export.JobStatusFailed || s.message != export.CancelledByUser {
			t.Errorf("export job after the cancel: %+v, want failed with %q", s, export.CancelledByUser)
		}
		if stored(exportID) {
			t.Error("the object of the cancelled export was not deleted")
		}
		if n := completedEvents() - reported; n != 0 {
			t.Errorf("the cancelled export reported %d events, want none", n)
		}
		if j := mirrored(jobID); j.Status != jobs.StatusCancelled {
			t.Errorf("mirrored job: %s, want cancelled", j.Status)
		}
	})

	t.Run("without cancel the mirrored job has a result", func(t *testing.T) {
		exportID, jobID := start()
		reported := completedEvents()
		drain()
		if s := stateOf(exportID); s.status != export.JobStatusCompleted || !stored(exportID) {
			t.Fatalf("export job: %+v, want completed with its object", s)
		}
		if j := mirrored(jobID); j.Status != jobs.StatusSucceeded {
			t.Errorf("mirrored job: %s, want succeeded", j.Status)
		}
		w := do(creator, http.MethodGet, "/api/v1/jobs/"+jobID+"/result", "")
		var result struct {
			ExportID string `json:"export_id"`
			RowCount *int   `json:"row_count"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK ||
			result.ExportID != exportID || result.RowCount == nil || *result.RowCount != 1 {
			t.Errorf("result = %d %s, want 200 with export %s and 1 row", w.Code, w.Body.String(), exportID)
		}
		if n := completedEvents() - reported; n != 1 {
			t.Errorf("the export reported %d events, want export.completed once", n)
		}
	})
}
