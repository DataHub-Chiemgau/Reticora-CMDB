package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// TestExportJobsOnlyForTheirCreator covers WP-347 (F-059, EXP-01, TEN-06):
// the generic job model mirrors export jobs, and request paths see such a
// job only as its creator — like export.ownJob, without an exception for
// org-wide administrators. Without a user id in the scope no export job is
// visible; other kinds and the worker paths (no scope in the context) stay
// organization-wide.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestExportJobsOnlyForTheirCreator(t *testing.T) {
	f := scopetest.Seed(t, "aa")
	bg := context.Background()
	s := jobs.NewService(jobs.NewPGRepository(f.App))
	s.Register(jobs.Kind{Name: "wp347.other", MaxAttempts: 1})
	other := f.ID()
	as := func(user string, clients ...string) context.Context {
		scope := database.OrgWideScope(f.OrgA, user)
		if len(clients) > 0 {
			scope.Clients = database.ScopeIDs(clients...)
		}
		return database.ContextWithTenantScope(bg, &scope)
	}
	creator := as(f.User, f.Client1)
	foreign := as(other, f.Client2)
	orgAdmin := as(f.ID())
	anonymous := as("")

	// refJob is the generic job of an export row, read without RLS.
	refJob := func(exportID string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM job WHERE ref_table = 'export_job' AND ref_id = $1`, exportID).Scan(&id); err != nil {
			t.Fatalf("generic job of export %s: %v", exportID, err)
		}
		return id
	}

	exportID := f.ID()
	if err := s.Track(creator, f.OrgA, "export", "export_job", exportID, f.User); err != nil {
		t.Fatal(err)
	}
	// The worker mirrors without a scope (export.JobWorker).
	if err := s.Mirror(bg, f.OrgA, "export_job", exportID, jobs.StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	exported := refJob(exportID)
	plain, _, err := s.Enqueue(creator, f.OrgA, "wp347.other", nil, jobs.EnqueueOptions{CreatedBy: f.User})
	if err != nil {
		t.Fatal(err)
	}

	ids := func(ctx context.Context, filter jobs.Filter) (map[string]bool, int) {
		t.Helper()
		list, total, lerr := s.List(ctx, f.OrgA, filter, api.PaginationParams{Limit: 50})
		if lerr != nil {
			t.Fatalf("list: %v", lerr)
		}
		out := map[string]bool{}
		for _, j := range list {
			out[j.ID] = true
		}
		return out, total
	}
	for name, ctx := range map[string]context.Context{"creator": creator, "worker": bg} {
		if got, gerr := s.Get(ctx, f.OrgA, exported); gerr != nil || got.CreatedBy != f.User {
			t.Errorf("%s reads the export job: %+v %v", name, got, gerr)
		}
		if seen, total := ids(ctx, jobs.Filter{}); !seen[exported] || !seen[plain.ID] || total != 2 {
			t.Errorf("%s lists %d jobs %v, want the export job and the other job", name, total, seen)
		}
	}
	for name, ctx := range map[string]context.Context{"other user": foreign, "org admin": orgAdmin, "no user id": anonymous} {
		if got, gerr := s.Get(ctx, f.OrgA, exported); !errors.Is(gerr, jobs.ErrNotFound) {
			t.Errorf("%s reads the export job: %+v %v, want ErrNotFound", name, got, gerr)
		}
		if _, cerr := s.Cancel(ctx, f.OrgA, exported); !errors.Is(cerr, jobs.ErrNotFound) {
			t.Errorf("%s cancels the export job: %v, want ErrNotFound", name, cerr)
		}
		seen, total := ids(ctx, jobs.Filter{})
		if seen[exported] || !seen[plain.ID] || total != 1 {
			t.Errorf("%s lists %d jobs %v, want only the other job", name, total, seen)
		}
		if _, total = ids(ctx, jobs.Filter{Kind: "export"}); total != 0 {
			t.Errorf("%s counts %d export jobs, want 0", name, total)
		}
		// Other kinds stay organization-wide.
		if got, gerr := s.Get(ctx, f.OrgA, plain.ID); gerr != nil || got.ID != plain.ID {
			t.Errorf("%s reads the other job: %+v %v", name, got, gerr)
		}
	}
	// The worker still moves the job; the creator cancels a queued one.
	if err = s.Mirror(bg, f.OrgA, "export_job", exportID, jobs.StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if got, gerr := s.Get(creator, f.OrgA, exported); gerr != nil || got.Status != jobs.StatusSucceeded {
		t.Errorf("creator after the worker: %+v %v", got, gerr)
	}
	queued := f.ID()
	if err = s.Track(creator, f.OrgA, "export", "export_job", queued, f.User); err != nil {
		t.Fatal(err)
	}
	queuedJob := refJob(queued)
	if got, cerr := s.Cancel(creator, f.OrgA, queuedJob); cerr != nil || got.Status != jobs.StatusCancelled {
		t.Errorf("creator cancels the queued export job: %+v %v", got, cerr)
	}
}
