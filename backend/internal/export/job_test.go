package export

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
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
	if !strings.HasPrefix(out, "id,name,status") {
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

func (failingCIRepo) List(context.Context, string, ci.FilterParams, api.PaginationParams) ([]ci.Item, int, error) {
	return nil, 0, errBoom
}

var errBoom = errors.New("boom")
