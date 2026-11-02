package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// TestQueueOverNATS covers TEC-14 (WP-130) against PostgreSQL and NATS
// JetStream:
//   - double delivery: every job is announced twice and two replicas
//     consume the stream; each job runs exactly once;
//   - restart: a replica dies while a job runs (unacknowledged message,
//     job running); after the timeout another replica receives the
//     redelivery, takes the job over and finishes it.
//
// It runs only with TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
func TestQueueOverNATS(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	f := scopetest.Seed(t, "93")
	bg := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	repo := jobs.NewPGRepository(f.App)

	// replica is one server process: its own connection, service and queue.
	type replica struct {
		nc    *nats.Conn
		queue *jobs.Queue
		stop  context.CancelFunc
	}
	start := func(name string, register func(q *jobs.Queue)) *replica {
		t.Helper()
		nc, err := nats.Connect(url, nats.Name(name))
		if err != nil {
			t.Fatal(err)
		}
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatal(err)
		}
		q := jobs.NewQueue(jobs.NewService(repo), js)
		q.Stream, q.SubjectPrefix = "JOBS_WP130_"+suffix, "wp130_"+suffix+"."
		q.AckMargin, q.SweepInterval = 500*time.Millisecond, time.Hour
		if err = q.Setup(bg); err != nil {
			t.Fatal(err)
		}
		register(q)
		ctx, cancel := context.WithCancel(bg)
		go q.Run(ctx)
		r := &replica{nc: nc, queue: q, stop: cancel}
		t.Cleanup(func() { cancel(); nc.Close() })
		return r
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Run("double delivery", func(t *testing.T) {
		var mu sync.Mutex
		runs := map[string]int{}
		handler := func(_ context.Context, j *jobs.Job) (any, error) {
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			runs[j.ID]++
			mu.Unlock()
			return nil, nil
		}
		kind := jobs.Kind{Name: "render", Timeout: 10 * time.Second}
		a := start("a", func(q *jobs.Queue) { q.Handle(kind, handler) })
		start("b", func(q *jobs.Queue) { q.Handle(kind, handler) })

		js, _ := jetstream.New(a.nc)
		var ids []string
		for i := range 20 {
			job, _, err := a.queue.Submit(bg, f.OrgA, "render", map[string]int{"n": i}, jobs.EnqueueOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, job.ID)
			// A second announcement with its own message id.
			data, _ := json.Marshal(jobs.Message{OrganizationID: f.OrgA, JobID: job.ID, Kind: "render"})
			if _, err = js.Publish(bg, a.queue.SubjectPrefix+"render", data, jetstream.WithMsgID(job.ID+"/dup")); err != nil {
				t.Fatal(err)
			}
		}
		waitFor("all jobs to succeed", func() bool {
			for _, id := range ids {
				if j, err := repo.Get(bg, f.OrgA, id); err != nil || j.Status != jobs.StatusSucceeded {
					return false
				}
			}
			return true
		})
		time.Sleep(300 * time.Millisecond) // let the duplicates arrive
		mu.Lock()
		defer mu.Unlock()
		for _, id := range ids {
			if runs[id] != 1 {
				t.Errorf("job %s ran %d times", id, runs[id])
			}
		}
	})

	t.Run("restart", func(t *testing.T) {
		kind := jobs.Kind{Name: "crash", MaxAttempts: 3, Timeout: 2 * time.Second, Backoff: func(int) time.Duration { return 0 }}
		running := make(chan struct{})
		dying := start("dying", func(q *jobs.Queue) {
			q.Handle(kind, func(ctx context.Context, _ *jobs.Job) (any, error) {
				close(running)
				<-ctx.Done()
				return nil, ctx.Err()
			})
		})
		job, _, err := dying.queue.Submit(bg, f.OrgA, "crash", nil, jobs.EnqueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		<-running
		// The process dies: no acknowledgement, the job stays running.
		dying.stop()
		dying.nc.Close()
		if j, _ := repo.Get(bg, f.OrgA, job.ID); j.Status != jobs.StatusRunning {
			t.Fatalf("job after the crash: %+v", j)
		}

		var took sync.Once
		tookOver := make(chan struct{})
		start("successor", func(q *jobs.Queue) {
			q.Handle(kind, func(context.Context, *jobs.Job) (any, error) {
				took.Do(func() { close(tookOver) })
				return "resumed", nil
			})
		})
		select {
		case <-tookOver:
		case <-time.After(20 * time.Second):
			t.Fatal("the successor never took the job over")
		}
		waitFor("the job to succeed", func() bool {
			j, err := repo.Get(bg, f.OrgA, job.ID)
			return err == nil && j.Status == jobs.StatusSucceeded
		})
		if j, _ := repo.Get(bg, f.OrgA, job.ID); j.Attempts != 2 || string(j.Result) != `"resumed"` {
			t.Errorf("job after the takeover: %+v", j)
		}
	})

	// Remove the test stream.
	nc, err := nats.Connect(url)
	if err == nil {
		if js, jerr := jetstream.New(nc); jerr == nil {
			_ = js.DeleteStream(bg, "JOBS_WP130_"+suffix)
		}
		nc.Close()
	}
}

// natsReplica is one server process of a WP-503 test: its own connection,
// service and queue on the stream of the test.
type natsReplica struct {
	nc    *nats.Conn
	queue *jobs.Queue
	stop  context.CancelFunc
}

// startNATSReplica starts a replica on stream/prefix whose kinds register
// registers; it consumes until stop or the end of the test.
func startNATSReplica(t *testing.T, url string, repo jobs.Repository, stream, prefix, name string,
	register func(q *jobs.Queue)) *natsReplica {
	t.Helper()
	nc, err := nats.Connect(url, nats.Name(name))
	if err != nil {
		t.Fatal(err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.NewQueue(jobs.NewService(repo), js)
	q.Stream, q.SubjectPrefix = stream, prefix
	q.AckMargin, q.SweepInterval = 500*time.Millisecond, time.Hour
	if err = q.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	register(q)
	ctx, cancel := context.WithCancel(context.Background())
	go q.Run(ctx)
	t.Cleanup(func() { cancel(); nc.Close() })
	return &natsReplica{nc: nc, queue: q, stop: cancel}
}

// waitForJob polls the job until cond holds (20 s at most).
func waitForJob(t *testing.T, repo jobs.Repository, orgID, id, what string, cond func(*jobs.Job) bool) *jobs.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		j, err := repo.Get(context.Background(), orgID, id)
		if err == nil && cond(j) {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %+v %v", what, j, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestQueueRedeliversAfterWorkerCrash covers WP-503 (TEC-14, JOB-01, F-147)
// against PostgreSQL and NATS JetStream with the settings of the two kinds
// the application submits (server/jobkinds.go): a replica claims a state
// snapshot (history.snapshot, MaxAttempts 3) and a bulk change (ci.bulk,
// MaxAttempts 1) and dies before acknowledging them. A second replica
// receives the redeliveries: it finishes the snapshot in a second attempt
// (attempts 2, succeeded) and fails the bulk change with "worker lost"
// without running it a second time, since a bulk applied in part must not
// be repeated.
//
// It runs only with TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
func TestQueueRedeliversAfterWorkerCrash(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	f := scopetest.Seed(t, "93")
	bg := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	stream, prefix := "JOBS_WP503_"+suffix, "wp503_"+suffix+"."
	t.Cleanup(func() {
		nc, err := nats.Connect(url)
		if err != nil {
			return
		}
		defer nc.Close()
		if js, jerr := jetstream.New(nc); jerr == nil {
			_ = js.DeleteStream(bg, stream)
		}
	})
	repo := jobs.NewPGRepository(f.App)
	noBackoff := func(int) time.Duration { return 0 }
	snapshot := jobs.Kind{Name: "history.snapshot", MaxAttempts: 3, Timeout: 2 * time.Second, Backoff: noBackoff}
	bulk := jobs.Kind{Name: "ci.bulk", MaxAttempts: 1, Timeout: 2 * time.Second, Backoff: noBackoff}

	// The first replica claims both jobs and dies in their handlers.
	var claimed sync.WaitGroup
	claimed.Add(2)
	dies := func(ctx context.Context, _ *jobs.Job) (any, error) {
		claimed.Done()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dying := startNATSReplica(t, url, repo, stream, prefix, "wp503-dying", func(q *jobs.Queue) {
		q.Handle(snapshot, dies)
		q.Handle(bulk, dies)
	})
	snapJob, _, err := dying.queue.Submit(f.OrgCtx(f.OrgA), f.OrgA, snapshot.Name, map[string]string{"entity_type": "ci"}, jobs.EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bulkJob, _, err := dying.queue.Submit(f.OrgCtx(f.OrgA), f.OrgA, bulk.Name, map[string]string{"action": "set_status"}, jobs.EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	claimed.Wait()
	dying.stop()
	dying.nc.Close()
	for _, id := range []string{snapJob.ID, bulkJob.ID} {
		if j, _ := repo.Get(bg, f.OrgA, id); j.Status != jobs.StatusRunning || j.Attempts != 1 {
			t.Fatalf("job after the crash: %+v", j)
		}
	}

	// The second replica takes both messages over after their AckWait.
	var snapshotRuns, bulkRuns atomic.Int32
	startNATSReplica(t, url, repo, stream, prefix, "wp503-successor", func(q *jobs.Queue) {
		q.Handle(snapshot, func(context.Context, *jobs.Job) (any, error) {
			snapshotRuns.Add(1)
			return map[string]string{"state": "replayed"}, nil
		})
		q.Handle(bulk, func(context.Context, *jobs.Job) (any, error) {
			bulkRuns.Add(1)
			return nil, nil
		})
	})

	done := waitForJob(t, repo, f.OrgA, snapJob.ID, "the snapshot to succeed", func(j *jobs.Job) bool { return j.Status == jobs.StatusSucceeded })
	if done.Attempts != 2 || snapshotRuns.Load() != 1 || !strings.Contains(string(done.Result), "replayed") {
		t.Errorf("snapshot after the takeover: attempts %d, runs %d, result %s; want 2 attempts, 1 run of the successor",
			done.Attempts, snapshotRuns.Load(), done.Result)
	}
	lost := waitForJob(t, repo, f.OrgA, bulkJob.ID, "the bulk job to end", func(j *jobs.Job) bool { return j.Status.Terminal() })
	if lost.Status != jobs.StatusFailed || !strings.Contains(lost.Error, "worker lost") || lost.Attempts != 1 {
		t.Errorf("bulk job after the lost worker: status %s, error %q, attempts %d; want failed with worker lost after 1 attempt",
			lost.Status, lost.Error, lost.Attempts)
	}
	// Negative: the bulk change ran once only, in the dead replica.
	time.Sleep(time.Second)
	if j, _ := repo.Get(bg, f.OrgA, bulkJob.ID); j.Status != jobs.StatusFailed || j.Attempts != 1 || bulkRuns.Load() != 0 {
		t.Errorf("bulk job ran again: %+v, runs of the successor %d", j, bulkRuns.Load())
	}
}

// testClock is a settable clock of the reaper tests.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// TestReapTimeoutsWithoutNATS covers WP-503 (JOB-01, F-147) on PostgreSQL
// as the runtime role: without the job queue the jobs of a request run in
// its process, and a process that ends leaves them running. The reaper
// (scheduler kind jobs.reap) fails a claimed job of every organization once
// its timeout and the grace period have passed — at once, with "worker
// lost", although attempts remain, since no executor would run it again —
// and leaves a job within its timeout alone.
func TestReapTimeoutsWithoutNATS(t *testing.T) {
	f := scopetest.Seed(t, "93")
	bg := context.Background()
	repo := jobs.NewPGRepository(f.App)
	clock := &testClock{t: time.Now().UTC()}
	svc := jobs.NewService(repo).WithClock(clock.now)
	svc.Register(jobs.Kind{Name: "history.snapshot", MaxAttempts: 3, Timeout: time.Minute})
	claim := func(orgID string) *jobs.Job {
		t.Helper()
		job, _, err := svc.Enqueue(f.OrgCtx(orgID), orgID, "history.snapshot", nil, jobs.EnqueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := svc.ClaimJob(bg, orgID, job.ID)
		if err != nil || claimed == nil {
			t.Fatalf("claim: %+v %v", claimed, err)
		}
		return claimed
	}
	lostA, lostB := claim(f.OrgA), claim(f.OrgB)
	clock.advance(time.Minute + jobs.DefaultReapGrace)
	fresh := claim(f.OrgA)
	clock.advance(time.Second)

	reaper := jobs.NewReaper(svc, nil)
	n, err := reaper.Reap(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range []*jobs.Job{lostA, lostB} {
		got, gerr := repo.Get(bg, j.OrganizationID, j.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if got.Status != jobs.StatusFailed || !strings.Contains(got.Error, "worker lost") || got.Attempts != 1 || got.FinishedAt == nil {
			t.Errorf("job of organization %s after the reaper: %+v; want failed with worker lost after 1 attempt", j.OrganizationID, got)
		}
	}
	if n < 2 {
		t.Errorf("reaped %d jobs, want at least the 2 lost ones", n)
	}
	if got, _ := repo.Get(bg, f.OrgA, fresh.ID); got.Status != jobs.StatusRunning {
		t.Errorf("job within its timeout reaped: %+v", got)
	}
}

// TestReaperRequeuesQueueKindsUntilMaxAttempts covers WP-503 (JOB-01) on
// PostgreSQL: with the job queue (NATS) the reaper puts the job of a lost
// worker of a queue kind back into the queue while attempts remain and
// fails it after max_attempts.
func TestReaperRequeuesQueueKindsUntilMaxAttempts(t *testing.T) {
	f := scopetest.Seed(t, "93")
	bg := context.Background()
	repo := jobs.NewPGRepository(f.App)
	clock := &testClock{t: time.Now().UTC()}
	svc := jobs.NewService(repo).WithClock(clock.now)
	svc.Register(jobs.Kind{Name: "history.snapshot", MaxAttempts: 2, Timeout: time.Minute, Backoff: func(int) time.Duration { return 0 }})
	reaper := jobs.NewReaper(svc, func(kind string) bool { return kind == "history.snapshot" })
	job, _, err := svc.Enqueue(f.OrgCtx(f.OrgA), f.OrgA, "history.snapshot", nil, jobs.EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if claimed, cerr := svc.ClaimJob(bg, f.OrgA, job.ID); cerr != nil || claimed == nil || claimed.Attempts != attempt {
			t.Fatalf("claim attempt %d: %+v %v", attempt, claimed, cerr)
		}
		clock.advance(time.Minute + jobs.DefaultReapGrace + time.Second)
		if _, err = reaper.Reap(bg); err != nil {
			t.Fatal(err)
		}
		got, gerr := repo.Get(bg, f.OrgA, job.ID)
		if gerr != nil {
			t.Fatal(gerr)
		}
		want := jobs.StatusQueued
		if attempt == 2 {
			want = jobs.StatusFailed
		}
		if got.Status != want || !strings.Contains(got.Error, "worker lost") {
			t.Errorf("after attempt %d: %+v; want %s with worker lost", attempt, got, want)
		}
	}
}
