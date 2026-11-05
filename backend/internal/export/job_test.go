package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/go-chi/chi/v5"
)

func seedCIs(t *testing.T, repo *ci.MemoryRepository, org string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := repo.Create(context.Background(), &ci.Item{
			OrganizationID: org,
			CITypeID:       "type-server",
			Name:           "srv-" + strings.Repeat("x", i+1),
			Status:         "active",
			Attributes:     map[string]any{},
		}); err != nil {
			t.Fatalf("seed CI: %v", err)
		}
	}
}

func newJobEnv(t *testing.T, cis *ci.MemoryRepository) (*MemoryJobRepository, *JobWorker, *JobHandler, chi.Router) {
	t.Helper()
	jobs := NewMemoryJobRepository()
	blobs := blob.NewFileStore(t.TempDir())
	worker := NewJobWorker(jobs, cis, blobs)
	h := NewJobHandler(jobs, worker, blobs)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return jobs, worker, h, mux
}

func TestCreateJobValidation(t *testing.T) {
	cis := ci.NewMemoryRepository()
	_, _, _, mux := newJobEnv(t, cis)

	req := httptest.NewRequest("POST", "/api/v1/export/jobs", strings.NewReader(`{"format":"xlsx"}`))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreateJobWithoutBlobStorage(t *testing.T) {
	cis := ci.NewMemoryRepository()
	jobs := NewMemoryJobRepository()
	// Worker without blob storage is disabled.
	h := NewJobHandler(jobs, NewJobWorker(jobs, cis, nil), nil)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/export/jobs", strings.NewReader(`{"format":"csv"}`))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestJobLifecycleCompletesWithSignedURL(t *testing.T) {
	cis := ci.NewMemoryRepository()
	seedCIs(t, cis, "org-1", 3)
	_, worker, _, mux := newJobEnv(t, cis)

	// Queue the job.
	req := httptest.NewRequest("POST", "/api/v1/export/jobs", strings.NewReader(`{"format":"csv"}`))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var created jobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Status != JobStatusPending {
		t.Fatalf("expected pending, got %s", created.Status)
	}
	if created.DownloadURL != "" {
		t.Fatalf("pending job must not expose a download URL")
	}

	// Process it synchronously.
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("expected a pending job to be claimed")
	}

	// The completed job carries a signed URL and row count.
	req = httptest.NewRequest("GET", "/api/v1/export/jobs/"+created.ID, nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var done jobResponse
	if err := json.Unmarshal(w.Body.Bytes(), &done); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if done.Status != JobStatusCompleted {
		t.Fatalf("expected completed, got %s", done.Status)
	}
	if done.RowCount == nil || *done.RowCount != 3 {
		t.Fatalf("expected 3 rows, got %v", done.RowCount)
	}
	if done.DownloadURL == "" {
		t.Fatal("completed job must expose a signed download URL")
	}
	if done.ExpiresAt == nil {
		t.Fatal("completed job must carry an expiry")
	}
}

func TestJobRendersCorrectCSV(t *testing.T) {
	cis := ci.NewMemoryRepository()
	seedCIs(t, cis, "org-1", 2)
	jobs := NewMemoryJobRepository()
	blobs := blob.NewFileStore(t.TempDir())
	worker := NewJobWorker(jobs, cis, blobs)

	job := &Job{Format: "csv", Scope: &JobScope{}}
	if err := jobs.CreateJob(context.Background(), "org-1", job); err != nil {
		t.Fatal(err)
	}
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("no job claimed")
	}

	stored, err := jobs.GetJob(context.Background(), "org-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobStatusCompleted {
		t.Fatalf("expected completed, got %s (%s)", stored.Status, stored.ErrorMessage)
	}
	rc, err := blobs.Get(context.Background(), ExportBucket, stored.ObjectKey)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	defer rc.Close()
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, rc); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// UTF-8 BOM and semicolons (EXP-01).
	if !strings.HasPrefix(out, "\ufeffid;name;status") {
		t.Fatalf("unexpected CSV header: %q", out[:min(40, len(out))])
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines", lines)
	}
}

func TestListJobsTenantIsolation(t *testing.T) {
	cis := ci.NewMemoryRepository()
	jobs, _, _, mux := newJobEnv(t, cis)

	for _, org := range []string{"org-1", "org-2"} {
		if err := jobs.CreateJob(context.Background(), org, &Job{Format: "json", InitiatedBy: "user-1", Scope: &JobScope{}}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/export/jobs", nil)
	req = tenantCtx(req) // org-1
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Data  []jobResponse `json:"data"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected exactly 1 job for org-1, got %d", resp.Total)
	}
	for _, job := range resp.Data {
		if job.OrganizationID != "org-1" {
			t.Fatalf("cross-tenant job leaked: %s", job.OrganizationID)
		}
	}
}

// TestJobOutsideTheCreatorsScope covers the memory repository's mirror of
// ownJob (WP-409): the creator reads and lists a job only while its scope
// covers the job's snapshot.
func TestJobOutsideTheCreatorsScope(t *testing.T) {
	const client1, client2 = "00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2"
	jobs, _, _, mux := newJobEnv(t, ci.NewMemoryRepository())
	job := &Job{Format: "json", InitiatedBy: "user-1", Scope: &JobScope{Clients: []string{client1}}}
	if err := jobs.CreateJob(context.Background(), "org-1", job); err != nil {
		t.Fatal(err)
	}
	for client, want := range map[string]int{client1: http.StatusOK, client2: http.StatusNotFound} {
		scope := database.OrgWideScope("org-1", "user-1")
		scope.Clients = database.ScopeIDs(client)
		for path, ok := range map[string]string{"/api/v1/export/jobs/" + job.ID: job.ID, "/api/v1/export/jobs": job.ID} {
			req := tenantCtx(httptest.NewRequest("GET", path, nil))
			req = req.WithContext(database.ContextWithTenantScope(req.Context(), &scope))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if listed := strings.Contains(w.Body.String(), ok); listed != (want == http.StatusOK) || (path != "/api/v1/export/jobs" && w.Code != want) {
				t.Errorf("GET %s with client scope %s: %d, job shown %v; want %d", path, client, w.Code, listed, want)
			}
		}
	}
}

func TestGetJobNotFoundOtherTenant(t *testing.T) {
	cis := ci.NewMemoryRepository()
	jobs, _, _, mux := newJobEnv(t, cis)

	job := &Job{Format: "json"}
	if err := jobs.CreateJob(context.Background(), "org-2", job); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/v1/export/jobs/"+job.ID, nil)
	req = tenantCtx(req) // org-1 tries to read org-2's job
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant job, got %d", w.Code)
	}
}

func TestExpiredJobHidesDownloadURL(t *testing.T) {
	cis := ci.NewMemoryRepository()
	seedCIs(t, cis, "org-1", 1)
	jobs := NewMemoryJobRepository()
	blobs := blob.NewFileStore(t.TempDir())
	worker := NewJobWorker(jobs, cis, blobs)
	// Expire immediately.
	worker.JobTTL = -time.Hour
	h := NewJobHandler(jobs, worker, blobs)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/v1/export/jobs", strings.NewReader(`{"format":"json"}`))
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var created jobResponse
	json.Unmarshal(w.Body.Bytes(), &created)

	worker.ProcessOne(context.Background())

	req = httptest.NewRequest("GET", "/api/v1/export/jobs/"+created.ID, nil)
	req = tenantCtx(req)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var done jobResponse
	json.Unmarshal(w.Body.Bytes(), &done)
	if done.DownloadURL != "" {
		t.Fatal("expired job must not expose a download URL")
	}
}

func TestFailingRenderMarksJobFailed(t *testing.T) {
	// A CI repository that always errors surfaces as a failed job, not a panic.
	jobs := NewMemoryJobRepository()
	blobs := blob.NewFileStore(t.TempDir())
	worker := NewJobWorker(jobs, failingCIRepo{}, blobs)

	job := &Job{Format: "csv", Scope: &JobScope{}}
	if err := jobs.CreateJob(context.Background(), "org-1", job); err != nil {
		t.Fatal(err)
	}
	worker.ProcessOne(context.Background())

	stored, err := jobs.GetJob(context.Background(), "org-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != JobStatusFailed {
		t.Fatalf("expected failed, got %s", stored.Status)
	}
	if stored.ErrorMessage == "" {
		t.Fatal("failed job must record the error")
	}
}

type failingCIRepo struct{ ci.Repository }

func (failingCIRepo) List(context.Context, string, *ci.FilterParams, api.PaginationParams) ([]ci.Item, int, error) {
	return nil, 0, errBoom
}

var errBoom = errors.New("boom")

// TestCompletedExportIsReported covers NTF-05 (WP-140): a completed export
// reports export.completed for its initiator, recorded with the completion
// (WP-550).
func TestCompletedExportIsReported(t *testing.T) {
	cis := ci.NewMemoryRepository()
	seedCIs(t, cis, "org-1", 2)
	repo, worker, _, mux := newJobEnv(t, cis)
	req := tenantCtx(httptest.NewRequest("POST", "/api/v1/export/jobs", strings.NewReader(`{"format":"json"}`)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("no job processed")
	}
	got := repo.outbox
	if len(got) != 1 || got[0].Type != events.ExportCompleted || got[0].OrgID != "org-1" {
		t.Fatalf("events: %+v", got)
	}
	data := got[0].Data.(map[string]any)
	if data["format"] != "json" || data["row_count"] != 2 || data["initiated_by"] == "" {
		t.Errorf("data: %v", data)
	}
}

// TestJobResponseCarriesManifest: an export job names its fields and the
// omitted ones (EXP-01).
func TestJobResponseCarriesManifest(t *testing.T) {
	cis := ci.NewMemoryRepository()
	_, _, _, mux := newJobEnv(t, cis)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("POST", "/api/v1/exports", strings.NewReader(`{"format":"csv"}`))))
	var resp struct {
		Manifest Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if resp.Manifest.Format != "csv" || len(resp.Manifest.Fields) == 0 || len(resp.Manifest.Omitted) == 0 {
		t.Errorf("manifest %+v", resp.Manifest)
	}
}

// TestJobFiltersIDs covers filters.ids (WP-780) with the memory
// repositories: POST /exports takes the selection, the job renders the
// selected CIs in the order of the filtered export (created_at, id),
// without deleted ids; more than 500 ids, an id that is no UUID, ids with
// another filter, with datev or an empty selection are 400.
func TestJobFiltersIDs(t *testing.T) {
	cis := ci.NewMemoryRepository()
	seedCIs(t, cis, "org-1", 4)
	all, _, err := cis.List(context.Background(), "org-1", &ci.FilterParams{SortBy: "created_at", SortDir: "asc"}, api.PaginationParams{Limit: 10})
	if err != nil || len(all) != 4 {
		t.Fatalf("seeded CIs: %d %v", len(all), err)
	}
	if err = cis.Delete(context.Background(), "org-1", all[1].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	jobs, worker, _, mux := newJobEnv(t, cis)

	post := func(body string) *httptest.ResponseRecorder {
		req := tenantCtx(httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(body)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	tooMany := make([]string, MaxExportIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%08d-0000-0000-0000-%012d", i+1, i+1)
	}
	raw, _ := json.Marshal(tooMany)
	for name, body := range map[string]string{
		"more than 500 ids": `{"format":"json","filters":{"ids":` + string(raw) + `}}`,
		"no UUID":           `{"format":"json","filters":{"ids":["not-a-uuid"]}}`,
		"another filter":    `{"format":"json","filters":{"ids":["` + all[0].ID + `"],"status":"active"}}`,
		"datev":             `{"format":"datev","filters":{"ids":["` + all[0].ID + `"]}}`,
		"empty selection":   `{"format":"json","filters":{"ids":[]}}`,
	} {
		if w := post(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}

	// The selection names the CIs out of order, a deleted one and one in
	// upper case.
	w := post(fmt.Sprintf(`{"format":"json","filters":{"ids":[%q,%q,%q]}}`, all[3].ID, all[1].ID, strings.ToUpper(all[0].ID)))
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /exports with ids: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID      string     `json:"id"`
		Filters JobFilters `json:"filters"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if want := []string{all[3].ID, all[1].ID, all[0].ID}; !slices.Equal(created.Filters.IDs, want) {
		t.Fatalf("filters.ids %v, want %v", created.Filters.IDs, want)
	}
	if !worker.ProcessOne(context.Background()) {
		t.Fatal("no job claimed")
	}
	stored, err := jobs.GetJob(context.Background(), "org-1", created.ID)
	if err != nil || stored.Status != JobStatusCompleted {
		t.Fatalf("job %+v %v, want completed", stored, err)
	}
	if stored.RowCount == nil || *stored.RowCount != 2 {
		t.Errorf("row count %v, want 2", stored.RowCount)
	}
	rc, err := worker.Blobs.Get(context.Background(), ExportBucket, stored.ObjectKey)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	defer rc.Close()
	var file struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(rc).Decode(&file); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	got := make([]string, 0, len(file.Data))
	for _, row := range file.Data {
		got = append(got, row.ID)
	}
	if want := []string{all[0].ID, all[3].ID}; !slices.Equal(got, want) {
		t.Errorf("exported %v, want %v (created_at order, no deleted id)", got, want)
	}
}

// TestRenderSelectionNeedsBatchReader: a repository without
// ci.BatchReader fails the export of a selection (fail-closed, WP-780).
func TestRenderSelectionNeedsBatchReader(t *testing.T) {
	var out strings.Builder
	_, _, err := RenderProjected(context.Background(), failingCIRepo{}, "org-1", "json",
		JobFilters{IDs: []string{"00000001-0000-0000-0000-000000000001"}}, nil, &out)
	if !errors.Is(err, errNoBatchReader) {
		t.Fatalf("render without batch reader: %v, want errNoBatchReader", err)
	}
}
