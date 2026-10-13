package jobs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
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
