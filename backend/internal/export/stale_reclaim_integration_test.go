package export_test

// WP-592 (TEC-14; F-315): a replica that dies while it exports leaves its
// job running; another replica queues it again after StaleAfter
// (ResumeStale). Earlier builds decided that by the start of the run alone,
// so an export that rendered longer than StaleAfter was queued again and ran
// a second time beside the first run. The worker now renews a heartbeat
// (started_at) while the export runs, and ResumeStale takes back only jobs
// without a heartbeat for StaleAfter. PostgreSQL integration with a slow
// renderer (a CI repository that blocks) and a fake clock both workers
// share.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestExportReclaimOnlyDeadWorkers' ./internal/export/

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// gatedCIs counts the renders (List calls) of a worker; with a gate each
// render waits for it to open or for the context to end (a slow renderer).
type gatedCIs struct {
	ci.Repository
	gate    chan struct{}
	started chan struct{}
	calls   atomic.Int32
}

func (g *gatedCIs) List(ctx context.Context, orgID string, filter ci.FilterParams, page api.PaginationParams) ([]ci.Item, int, error) {
	g.calls.Add(1)
	if g.gate != nil {
		select {
		case g.started <- struct{}{}:
		default:
		}
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	return g.Repository.List(ctx, orgID, filter, page)
}

// fakeClock is the clock of both workers; the test moves it on.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestExportReclaimOnlyDeadWorkers: while the first worker renders an export
// longer than StaleAfter, the second worker's ResumeStale returns 0 and the
// second worker does not render the export again; after the first worker
// crashed (no heartbeat, the job stays running), ResumeStale returns 1 and
// the second worker completes the export.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestExportReclaimOnlyDeadWorkers(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	f.CI(t, f.OrgA, "", "export-lease-ci")
	user := f.AppUser(t, f.OrgA, "export-lease-user")
	jobs := export.NewPGJobRepository(f.App)
	store := blob.NewFileStore(t.TempDir())
	cis := ci.NewPGRepository(f.App)

	// Running and queued jobs left by other tests are queued again and
	// finished first: ResumeStale and ClaimPending work across
	// organizations, the counts below are this test's job only.
	drain := export.NewJobWorker(jobs, cis, store)
	drain.Now = func() time.Time { return time.Now().UTC().Add(export.DefaultStaleAfter + time.Hour) }
	drain.ResumeStale(bg)
	for drain.ProcessOne(bg) {
	}

	scope := database.OrgWideScope(f.OrgA, user)
	job := &export.Job{InitiatedBy: user, Format: "json", Scope: export.SnapshotScope(&scope)}
	if err := jobs.CreateJob(database.ContextWithTenantScope(bg, &scope), f.OrgA, job); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM export_job WHERE id = $1`, job.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	clock := &fakeClock{now: time.Now().UTC()}
	first := &gatedCIs{Repository: cis, gate: make(chan struct{}), started: make(chan struct{}, 1)}
	worker1 := export.NewJobWorker(jobs, first, store)
	worker1.Now = clock.Now
	worker1.HeartbeatEvery = 20 * time.Millisecond
	second := &gatedCIs{Repository: cis}
	worker2 := export.NewJobWorker(jobs, second, store)
	worker2.Now = clock.Now

	ctx1, crash := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker1.ProcessOne(ctx1)
	}()
	defer func() {
		crash()
		<-done
	}()
	select {
	case <-first.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first worker does not render the export")
	}

	// The export renders longer than StaleAfter.
	clock.Advance(export.DefaultStaleAfter + time.Minute)
	// The first worker's heartbeat follows the clock; without heartbeat the
	// checks below fail.
	renewed := clock.Now().Add(-time.Second)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var started time.Time
		if err := f.Admin.QueryRow(bg, `SELECT started_at FROM export_job WHERE id = $1`, job.ID).Scan(&started); err == nil && started.After(renewed) {
			break
		}
	}
	if n := worker2.ResumeStale(bg); n != 0 {
		t.Errorf("second worker: ResumeStale = %d while the first worker renders, want 0", n)
	}
	if worker2.ProcessOne(bg) || second.calls.Load() != 0 {
		t.Errorf("second worker rendered the export beside the first worker (%d renders): double execution", second.calls.Load())
	}
	if s := status(); s != export.JobStatusRunning {
		t.Errorf("job while the first worker renders: %s, want running", s)
	}

	// The first worker crashes: no heartbeat any more, the job stays
	// running.
	crash()
	<-done
	if s := status(); s != export.JobStatusRunning {
		t.Fatalf("job after the crash: %s, want running", s)
	}
	clock.Advance(export.DefaultStaleAfter + time.Minute)
	if n := worker2.ResumeStale(bg); n != 1 {
		t.Errorf("second worker: ResumeStale = %d after the first worker crashed, want 1", n)
	}
	for worker2.ProcessOne(bg) {
	}
	if s := status(); s != export.JobStatusCompleted || second.calls.Load() == 0 {
		t.Errorf("job resumed by the second worker: %s after %d renders, want completed", s, second.calls.Load())
	}
}
