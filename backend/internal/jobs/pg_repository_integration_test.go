package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// TestJobsAgainstPostgres covers JOB-01 (WP-129) against PostgreSQL: the
// job table with its checks, idempotent creation per organization, the
// lifecycle with retry, concurrent claims (each job once), organization
// isolation under row level security, and the tracker of specialised jobs.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestJobsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "92")
	bg := context.Background()
	repo := jobs.NewPGRepository(f.App)
	s := jobs.NewService(repo)
	s.Register(jobs.Kind{Name: "wp129.render", MaxAttempts: 2, Timeout: time.Minute,
		Backoff: func(int) time.Duration { return 0 }})

	job, created, err := s.Enqueue(bg, f.OrgA, "wp129.render", map[string]int{"n": 1},
		jobs.EnqueueOptions{IdempotencyKey: "render-1", CreatedBy: f.User})
	if err != nil || !created || job.Status != jobs.StatusQueued || job.CreatedBy != f.User || job.TimeoutSeconds != 60 {
		t.Fatalf("enqueue: %+v %v %v", job, created, err)
	}
	again, created, err := s.Enqueue(bg, f.OrgA, "wp129.render", nil, jobs.EnqueueOptions{IdempotencyKey: "render-1"})
	if err != nil || created || again.ID != job.ID {
		t.Errorf("idempotent enqueue: %+v %v %v", again, created, err)
	}
	if other, createdB, errB := s.Enqueue(bg, f.OrgB, "wp129.render", nil, jobs.EnqueueOptions{IdempotencyKey: "render-1"}); errB != nil || !createdB || other.ID == job.ID {
		t.Errorf("same key in organization B: %+v %v %v", other, createdB, errB)
	}

	// Isolation: organization B neither reads nor changes A's job.
	if _, err = s.Get(bg, f.OrgB, job.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("organization B read A's job: %v", err)
	}
	if _, err = s.Cancel(bg, f.OrgB, job.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("organization B cancelled A's job: %v", err)
	}
	if list, total, _ := s.List(bg, f.OrgB, jobs.Filter{}, api.PaginationParams{Limit: 50}); total != 1 || list[0].OrganizationID != f.OrgB {
		t.Errorf("organization B lists %d jobs: %+v", total, list)
	}

	// Lifecycle with one retry.
	claimed, err := s.Claim(bg, f.OrgA, "wp129.render")
	if err != nil || claimed == nil || claimed.ID != job.ID || claimed.Attempts != 1 || claimed.StartedAt == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err = s.Progress(bg, f.OrgA, job.ID, 40); err != nil {
		t.Fatal(err)
	}
	retried, err := s.Fail(bg, f.OrgA, job.ID, errors.New("temporary"))
	if err != nil || retried.Status != jobs.StatusQueued || retried.Error != "temporary" || retried.FinishedAt != nil {
		t.Fatalf("retry: %+v %v", retried, err)
	}
	if _, err = s.Claim(bg, f.OrgA, "wp129.render"); err != nil {
		t.Fatal(err)
	}
	done, err := s.Succeed(bg, f.OrgA, job.ID, map[string]string{"object": "x.csv"})
	if err != nil || done.Status != jobs.StatusSucceeded || done.Attempts != 2 || done.Progress != 100 || done.FinishedAt == nil {
		t.Fatalf("succeed: %+v %v", done, err)
	}
	if _, err = s.Cancel(bg, f.OrgA, job.ID); !errors.Is(err, jobs.ErrTransition) {
		t.Errorf("cancel a finished job: %v", err)
	}

	// Concurrent workers take each queued job exactly once.
	const n = 6
	for i := range n {
		if _, _, err = s.Enqueue(bg, f.OrgA, "wp129.render", map[string]int{"i": i}, jobs.EnqueueOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, cerr := s.Claim(bg, f.OrgA, "wp129.render")
				if cerr != nil || j == nil {
					return
				}
				mu.Lock()
				seen[j.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("claimed %d distinct jobs, want %d", len(seen), n)
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("job %s claimed %d times", id, count)
		}
	}

	// The database holds the model.
	for name, stmt := range map[string]string{
		"progress 101":            `UPDATE job SET progress = 101 WHERE id = $1`,
		"finished without an end": `UPDATE job SET status = 'running' WHERE id = $1`,
		"unknown status":          `UPDATE job SET status = 'done', finished_at = now() WHERE id = $1`,
	} {
		if _, err = f.Admin.Exec(bg, stmt, job.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	// Tracker: a specialised job is registered once and mirrored.
	exportID := f.ID()
	if err = s.Track(bg, f.OrgA, "export", "export_job", exportID, f.User); err != nil {
		t.Fatal(err)
	}
	if err = s.Track(bg, f.OrgA, "export", "export_job", exportID, f.User); err != nil {
		t.Fatal(err)
	}
	for _, st := range []jobs.Status{jobs.StatusRunning, jobs.StatusSucceeded} {
		if err = s.Mirror(bg, f.OrgA, "export_job", exportID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	tracked, _, err := s.List(bg, f.OrgA, jobs.Filter{Kind: "export"}, api.PaginationParams{Limit: 10})
	if err != nil || len(tracked) != 1 || tracked[0].Status != jobs.StatusSucceeded || tracked[0].RefID != exportID {
		t.Errorf("tracked export job: %+v %v", tracked, err)
	}
}
