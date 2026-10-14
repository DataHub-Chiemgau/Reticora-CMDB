package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func bulkMux(t *testing.T, n int) (chi.Router, *MemoryRepository, []string, *jobs.Service) {
	t.Helper()
	repo := NewMemoryRepository()
	ids := make([]string, n)
	for i := range ids {
		item := &Item{OrganizationID: "org-1", Name: fmt.Sprintf("bulk-%03d", i), CITypeID: "t", Status: StatusActive}
		if err := repo.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
		ids[i] = item.ID
	}
	jobSvc := jobs.NewService(jobs.NewMemoryRepository())
	jobSvc.Register(jobs.Kind{Name: BulkJobKind, MaxAttempts: 1})
	mux := chi.NewRouter()
	NewHandler(NewService(repo).WithBulkJobs(jobSvc)).RegisterRoutes(mux)
	return mux, repo, ids, jobSvc
}

func postBulk(mux chi.Router, body string) *httptest.ResponseRecorder {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis/bulk", bytes.NewBufferString(body)).WithContext(ctx))
	return w
}

func bulkBody(status string, ids ...string) string {
	raw, _ := json.Marshal(BulkRequest{Action: BulkSetStatus, IDs: ids, Status: status})
	return string(raw)
}

// TestBulkStatusReportsEachID covers API-08 and BLK-01 (WP-181): below 100
// ids the status change runs at once and reports every id; a missing CI
// fails alone.
func TestBulkStatusReportsEachID(t *testing.T) {
	mux, repo, ids, _ := bulkMux(t, 3)
	w := postBulk(mux, bulkBody(StatusMaintenance, ids[0], "missing", ids[1], ids[0]))
	var res BulkResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	if res.Total != 3 || res.Succeeded != 2 || res.Failed != 1 || res.Items[1].ID != "missing" || res.Items[1].Code != http.StatusNotFound {
		t.Errorf("report: %+v", res)
	}
	for _, id := range ids[:2] {
		if item, _ := repo.GetByID(context.Background(), "org-1", id); item.Status != StatusMaintenance {
			t.Errorf("%s: status %s", id, item.Status)
		}
	}
	if item, _ := repo.GetByID(context.Background(), "org-1", ids[2]); item.Status != StatusActive {
		t.Error("a CI outside the request changed")
	}
}

// TestBulkLimits: more than 500 ids, an unknown status or action is 422,
// no id is 400.
func TestBulkLimits(t *testing.T) {
	mux, _, ids, _ := bulkMux(t, 1)
	many := make([]string, MaxBulkIDs+1)
	for i := range many {
		many[i] = fmt.Sprintf("id-%d", i)
	}
	for body, want := range map[string]int{
		bulkBody(StatusActive, many...):                               http.StatusUnprocessableEntity,
		bulkBody("broken", ids[0]):                                    http.StatusUnprocessableEntity,
		`{"action":"delete","ids":["` + ids[0] + `"]}`:                http.StatusUnprocessableEntity,
		bulkBody(StatusActive):                                        http.StatusBadRequest,
		`{"action":"set_status","ids":["a"],"status":"active","x":1}`: http.StatusBadRequest,
	} {
		if w := postBulk(mux, body); w.Code != want {
			t.Errorf("%.60s: %d, want %d", body, w.Code, want)
		}
	}
}

// TestBulkFromHundredIDsIsAJob: from 100 ids the request answers 202 with
// the job; the job result is the report per id.
func TestBulkFromHundredIDsIsAJob(t *testing.T) {
	mux, repo, ids, jobSvc := bulkMux(t, BulkJobFromIDs)
	w := postBulk(mux, bulkBody(StatusInactive, ids...))
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || w.Code != http.StatusAccepted || accepted.JobID == "" ||
		!strings.HasSuffix(w.Header().Get("Location"), accepted.JobID) {
		t.Fatalf("bulk job: %d %s", w.Code, w.Body.String())
	}
	var job *jobs.Job
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var err error
		if job, err = jobSvc.Get(context.Background(), "org-1", accepted.JobID); err == nil && job.Status == jobs.StatusSucceeded {
			break
		}
	}
	if job == nil || job.Status != jobs.StatusSucceeded {
		t.Fatalf("job: %+v", job)
	}
	var res BulkResult
	if err := json.Unmarshal(job.Result, &res); err != nil || res.Succeeded != BulkJobFromIDs || res.Failed != 0 {
		t.Errorf("job result: %+v %v", res, err)
	}
	if item, _ := repo.GetByID(context.Background(), "org-1", ids[99]); item.Status != StatusInactive {
		t.Errorf("status %s", item.Status)
	}
}
