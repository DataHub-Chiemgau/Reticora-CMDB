package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Discovery jobs in the generic job model (JOB-01, WP-502, F-146): a created
// discovery job is tracked, UpdateJobStatus mirrors its status once
// committed; failures of the tracker are logged and leave the discovery job
// and its status in place; without a tracker nothing changes.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestDiscoveryJobTracking' ./internal/discovery/

// recordingTracker records the calls of the generic job model; fail makes
// every call fail.
type recordingTracker struct {
	mu      sync.Mutex
	fail    bool
	tracked []string
	mirrors []jobs.Status
	errMsgs []string
}

func (r *recordingTracker) Track(_ context.Context, _, kind, refTable, refID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tracked = append(r.tracked, kind+" "+refTable+" "+refID)
	if r.fail {
		return errors.New("job store down")
	}
	return nil
}

func (r *recordingTracker) Mirror(_ context.Context, _, refTable, _ string, status jobs.Status, errMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if refTable != "discovery_job" {
		return errors.New("unexpected ref table " + refTable)
	}
	r.mirrors = append(r.mirrors, status)
	r.errMsgs = append(r.errMsgs, errMsg)
	if r.fail {
		return errors.New("job store down")
	}
	return nil
}

// captureJobLogs routes slog into the returned buffer for the test.
func captureJobLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// createJob posts a discovery job through the handler.
func createJob(t *testing.T, h *discovery.Handler, orgID, collectorID string) discovery.Job {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/jobs",
		strings.NewReader(`{"collector_id":"`+collectorID+`","job_type":"poll"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: orgID, UserID: "u1"}))
	w := httptest.NewRecorder()
	h.CreateJob(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /discovery/jobs = %d %s", w.Code, w.Body.String())
	}
	var job discovery.Job
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestDiscoveryJobTrackingCreate(t *testing.T) {
	const org = "00000000-0000-0000-0000-0000000000aa"
	t.Run("tracked", func(t *testing.T) {
		repo := discovery.NewMemoryRepository()
		tracker := &recordingTracker{}
		job := createJob(t, discovery.NewHandler(repo, nil).WithJobTracker(tracker), org, "c1")
		if len(tracker.tracked) != 1 || tracker.tracked[0] != "discovery.poll discovery_job "+job.ID {
			t.Errorf("tracked %v, want discovery.poll discovery_job %s", tracker.tracked, job.ID)
		}
	})
	t.Run("track failure keeps the job", func(t *testing.T) {
		logs := captureJobLogs(t)
		repo := discovery.NewMemoryRepository()
		tracker := &recordingTracker{fail: true}
		job := createJob(t, discovery.NewHandler(repo, nil).WithJobTracker(tracker), org, "c1")
		stored, err := repo.GetJob(context.Background(), org, job.ID)
		if err != nil || stored.Status != discovery.JobStatusPending {
			t.Fatalf("discovery job after a failed Track: %+v, %v", stored, err)
		}
		if !strings.Contains(logs.String(), "track discovery job") || !strings.Contains(logs.String(), "job store down") {
			t.Errorf("failed Track not logged: %s", logs.String())
		}
	})
	t.Run("without tracker", func(t *testing.T) {
		repo := discovery.NewMemoryRepository()
		job := createJob(t, discovery.NewHandler(repo, nil), org, "c1")
		if stored, err := repo.GetJob(context.Background(), org, job.ID); err != nil || stored.Status != discovery.JobStatusPending {
			t.Fatalf("discovery job without tracker: %+v, %v", stored, err)
		}
	})
}

// TestDiscoveryJobTrackingStatus runs UpdateJobStatus against PostgreSQL
// (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryJobTrackingStatus(t *testing.T) {
	f := scopetest.Seed(t, "a3")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	newJob := func(repo *discovery.PGRepository) *discovery.Job {
		t.Helper()
		col := &discovery.Collector{OrganizationID: f.OrgA, Name: "wp502-" + f.ID()[:8], Status: discovery.CollectorPending, Config: map[string]any{}}
		if err := repo.RegisterCollector(ctx, col); err != nil {
			t.Fatal(err)
		}
		job := &discovery.Job{OrganizationID: f.OrgA, CollectorID: col.ID, JobType: discovery.JobTypeSweep}
		if err := repo.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	stored := func(id string) (status, summary string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(result_summary::text, '') FROM discovery_job WHERE id = $1`, id).Scan(&status, &summary); err != nil {
			t.Fatal(err)
		}
		return status, summary
	}
	genericJobs := func(id string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM job WHERE ref_id::text = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("mirrored", func(t *testing.T) {
		repo := discovery.NewPGRepository(f.App)
		tracker := &recordingTracker{}
		discovery.NewHandler(repo, nil).WithJobTracker(tracker)
		job := newJob(repo)
		steps := []struct {
			status, errMsg string
			want           jobs.Status
		}{
			{discovery.JobStatusRunning, "", jobs.StatusRunning},
			{discovery.JobStatusCompleted, "", jobs.StatusSucceeded},
			{discovery.JobStatusPending, "", jobs.StatusQueued},
			{discovery.JobStatusFailed, "snmp timeout", jobs.StatusFailed},
		}
		for i, step := range steps {
			updated, err := repo.UpdateJobStatus(ctx, f.OrgA, job.ID, step.status, step.errMsg)
			if err != nil || updated.Status != step.status {
				t.Fatalf("UpdateJobStatus(%s) = %+v, %v", step.status, updated, err)
			}
			if len(tracker.mirrors) != i+1 || tracker.mirrors[i] != step.want || tracker.errMsgs[i] != step.errMsg {
				t.Errorf("after %s: mirrored %v %q, want %s %q last", step.status, tracker.mirrors, tracker.errMsgs, step.want, step.errMsg)
			}
		}
		got, err := repo.GetJob(ctx, f.OrgA, job.ID)
		if err != nil || got.Status != discovery.JobStatusFailed || got.StartedAt == "" || got.CompletedAt == "" {
			t.Fatalf("failed discovery job: %+v, %v", got, err)
		}
		if cause := discovery.JobErrorOf(got); cause == nil || cause.Message != "snmp timeout" {
			t.Errorf("cause of the failed job: %+v", cause)
		}
		var fe *api.FieldError
		if _, err := repo.UpdateJobStatus(ctx, f.OrgA, job.ID, "done", ""); !errors.As(err, &fe) || fe.Field != "status" {
			t.Errorf("unknown status: %v", err)
		}
		if _, err := repo.UpdateJobStatus(f.OrgCtx(f.OrgB), f.OrgB, job.ID, discovery.JobStatusRunning, ""); err == nil || err.Error() != "not found" {
			t.Errorf("organization B moved A's discovery job: %v", err)
		}
		if len(tracker.mirrors) != len(steps) {
			t.Errorf("refused updates were mirrored: %v", tracker.mirrors)
		}
	})

	t.Run("mirror failure keeps the status", func(t *testing.T) {
		logs := captureJobLogs(t)
		repo := discovery.NewPGRepository(f.App)
		discovery.NewHandler(repo, nil).WithJobTracker(&recordingTracker{fail: true})
		job := newJob(repo)
		updated, err := repo.UpdateJobStatus(ctx, f.OrgA, job.ID, discovery.JobStatusRunning, "")
		if err != nil || updated.Status != discovery.JobStatusRunning {
			t.Fatalf("UpdateJobStatus with a failing tracker = %+v, %v", updated, err)
		}
		if status, _ := stored(job.ID); status != discovery.JobStatusRunning {
			t.Errorf("discovery job status after a failed Mirror: %s, want running", status)
		}
		if !strings.Contains(logs.String(), "mirror discovery job") || !strings.Contains(logs.String(), "job store down") {
			t.Errorf("failed Mirror not logged: %s", logs.String())
		}
	})

	t.Run("without tracker", func(t *testing.T) {
		repo := discovery.NewPGRepository(f.App)
		job := newJob(repo)
		if _, err := repo.UpdateJobStatus(ctx, f.OrgA, job.ID, discovery.JobStatusCompleted, ""); err != nil {
			t.Fatal(err)
		}
		if status, summary := stored(job.ID); status != discovery.JobStatusCompleted || summary != "" {
			t.Errorf("discovery job without tracker: %s %q", status, summary)
		}
		if n := genericJobs(job.ID); n != 0 {
			t.Errorf("generic jobs without tracker: %d", n)
		}
	})
}
