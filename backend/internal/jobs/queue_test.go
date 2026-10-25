package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// fakeDelivery records what the queue did with a message.
type fakeDelivery struct {
	data []byte
	acks int
	naks []time.Duration
}

func (d *fakeDelivery) Data() []byte { return d.data }
func (d *fakeDelivery) Ack() error   { d.acks++; return nil }
func (d *fakeDelivery) NakWithDelay(delay time.Duration) error {
	d.naks = append(d.naks, delay)
	return nil
}

func deliveryOf(t *testing.T, job *Job) *fakeDelivery {
	t.Helper()
	data, err := json.Marshal(Message{OrganizationID: job.OrganizationID, JobID: job.ID, Kind: job.Kind})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeDelivery{data: data}
}

// TestQueueRunsAJobOnce covers TEC-14 (WP-130): the database claim decides
// who runs a job, so a message delivered twice — to two workers or again
// after it ran — runs the job once and every copy is acknowledged.
func TestQueueRunsAJobOnce(t *testing.T) {
	s, _ := newService()
	q := NewQueue(s, nil)
	ctx := context.Background()
	runs := 0
	release := make(chan struct{})
	started := make(chan struct{})
	q.Handle(Kind{Name: "render", Timeout: time.Minute}, func(context.Context, *Job) (any, error) {
		runs++
		close(started)
		<-release
		return map[string]int{"rows": 3}, nil
	})
	job, _, err := q.Submit(ctx, "org-1", "render", nil, EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}

	first, second := deliveryOf(t, job), deliveryOf(t, job)
	done := make(chan Outcome)
	go func() { done <- q.Process(ctx, first) }()
	<-started
	// The second copy arrives while the first runs: it is not claimed and
	// comes back after the timeout of the running attempt.
	if out := q.Process(ctx, second); out.Ran || out.Acked || out.Delay < time.Minute-time.Second {
		t.Errorf("copy while running: %+v", out)
	}
	close(release)
	if out := <-done; !out.Ran || !out.Acked {
		t.Errorf("first delivery: %+v", out)
	}
	// Redelivered after the run: the job is done, the copy is dropped.
	if out := q.Process(ctx, second); out.Ran || !out.Acked {
		t.Errorf("copy after the run: %+v", out)
	}
	got, _ := s.Get(ctx, "org-1", job.ID)
	if runs != 1 || got.Status != StatusSucceeded || got.Attempts != 1 || string(got.Result) != `{"rows":3}` {
		t.Errorf("runs %d, job %+v", runs, got)
	}
	if first.acks != 1 || second.acks != 1 {
		t.Errorf("acks: %d, %d", first.acks, second.acks)
	}
}

// TestQueueRetriesAndRecoversLostWorkers covers the retry and restart
// paths of TEC-14: a failed attempt is redelivered after the backoff, a
// job whose worker died is taken over after its timeout, and a shutdown
// leaves the message unacknowledged.
func TestQueueRetriesAndRecoversLostWorkers(t *testing.T) {
	ctx := context.Background()

	t.Run("retry after backoff", func(t *testing.T) {
		s, c := newService()
		q := NewQueue(s, nil)
		fail := true
		q.Handle(Kind{Name: "sync", MaxAttempts: 2, Timeout: time.Minute, Backoff: func(int) time.Duration { return 20 * time.Second }},
			func(context.Context, *Job) (any, error) {
				if fail {
					return nil, errors.New("upstream down")
				}
				return nil, nil
			})
		job, _, _ := q.Submit(ctx, "org-1", "sync", nil, EnqueueOptions{})
		d := deliveryOf(t, job)
		if out := q.Process(ctx, d); !out.Ran || out.Acked || out.Delay != 20*time.Second {
			t.Errorf("failed attempt: %+v", out)
		}
		// Redelivered too early: not due yet.
		c.advance(5 * time.Second)
		if out := q.Process(ctx, d); out.Ran || out.Acked || out.Delay != 15*time.Second {
			t.Errorf("early redelivery: %+v", out)
		}
		c.advance(15 * time.Second)
		fail = false
		if out := q.Process(ctx, d); !out.Ran || !out.Acked {
			t.Errorf("second attempt: %+v", out)
		}
		if got, _ := s.Get(ctx, "org-1", job.ID); got.Status != StatusSucceeded || got.Attempts != 2 {
			t.Errorf("job: %+v", got)
		}
	})

	t.Run("exhausted attempts", func(t *testing.T) {
		s, _ := newService()
		q := NewQueue(s, nil)
		q.Handle(Kind{Name: "sync", MaxAttempts: 1, Timeout: time.Minute},
			func(context.Context, *Job) (any, error) { return nil, errors.New("broken") })
		job, _, _ := q.Submit(ctx, "org-1", "sync", nil, EnqueueOptions{})
		if out := q.Process(ctx, deliveryOf(t, job)); !out.Ran || !out.Acked {
			t.Errorf("last attempt: %+v", out)
		}
		if got, _ := s.Get(ctx, "org-1", job.ID); got.Status != StatusFailed || got.Error != "broken" {
			t.Errorf("job: %+v", got)
		}
	})

	t.Run("worker lost", func(t *testing.T) {
		s, c := newService()
		q := NewQueue(s, nil)
		runs := 0
		q.Handle(Kind{Name: "sync", MaxAttempts: 3, Timeout: time.Minute, Backoff: func(int) time.Duration { return 0 }},
			func(context.Context, *Job) (any, error) { runs++; return nil, nil })
		job, _, _ := q.Submit(ctx, "org-1", "sync", nil, EnqueueOptions{})
		// A worker claimed the job and died before it finished.
		if claimed, err := s.ClaimJob(ctx, "org-1", job.ID); err != nil || claimed == nil {
			t.Fatalf("claim: %+v %v", claimed, err)
		}
		d := deliveryOf(t, job)
		c.advance(30 * time.Second)
		if out := q.Process(ctx, d); out.Ran || out.Acked || out.Delay != 30*time.Second {
			t.Errorf("within the timeout: %+v", out)
		}
		c.advance(31 * time.Second)
		if out := q.Process(ctx, d); !out.Ran || !out.Acked {
			t.Errorf("after the timeout: %+v", out)
		}
		got, _ := s.Get(ctx, "org-1", job.ID)
		if runs != 1 || got.Status != StatusSucceeded || got.Attempts != 2 {
			t.Errorf("runs %d, job %+v", runs, got)
		}
	})

	t.Run("shutdown", func(t *testing.T) {
		s, _ := newService()
		q := NewQueue(s, nil)
		runCtx, cancel := context.WithCancel(ctx)
		q.Handle(Kind{Name: "sync", Timeout: time.Minute}, func(ctx context.Context, _ *Job) (any, error) {
			cancel()
			return nil, ctx.Err()
		})
		job, _, _ := q.Submit(ctx, "org-1", "sync", nil, EnqueueOptions{})
		d := deliveryOf(t, job)
		if out := q.Process(runCtx, d); d.acks != 0 || len(d.naks) != 0 || !out.Ran {
			t.Errorf("shutdown: %+v, delivery %+v", out, d)
		}
		if got, _ := s.Get(ctx, "org-1", job.ID); got.Status != StatusRunning {
			t.Errorf("job after shutdown: %+v", got)
		}
	})

	t.Run("unknown or malformed", func(t *testing.T) {
		s, _ := newService()
		q := NewQueue(s, nil)
		for _, d := range []*fakeDelivery{{data: []byte("{")}, {data: []byte(`{"organization_id":"org-1","job_id":"missing"}`)}} {
			if out := q.Process(ctx, d); out.Ran || !out.Acked {
				t.Errorf("%s: %+v", d.data, out)
			}
		}
	})
}

// TestQueueSweepAnnouncesOverdueJobs: Submit without a connection keeps
// the job queued; the sweep finds it once it is overdue.
func TestQueueSweepAnnouncesOverdueJobs(t *testing.T) {
	s, c := newService()
	q := NewQueue(s, nil)
	q.Handle(Kind{Name: "sync"}, func(context.Context, *Job) (any, error) { return nil, nil })
	job, _, err := q.Submit(context.Background(), "org-1", "sync", nil, EnqueueOptions{})
	if err != nil || job.Status != StatusQueued {
		t.Fatalf("submit without NATS: %+v %v", job, err)
	}
	if due, _ := s.repo.QueuedDue(context.Background(), c.now().Add(-q.SweepInterval), 10); len(due) != 0 {
		t.Errorf("a fresh job counts as overdue: %+v", due)
	}
	c.advance(2 * q.SweepInterval)
	if due, _ := s.repo.QueuedDue(context.Background(), c.now().Add(-q.SweepInterval), 10); len(due) != 1 || due[0].ID != job.ID {
		t.Errorf("overdue jobs: %+v", due)
	}
}

// fakeJetStream stands in for JetStream in Run: the stream answers unless
// down is set, consumers are created and consume nothing, announcements are
// accepted.
type fakeJetStream struct {
	jetstream.JetStream
	down atomic.Bool
}

func (f *fakeJetStream) Stream(context.Context, string) (jetstream.Stream, error) {
	if f.down.Load() {
		return nil, errors.New("nats: no responders available for request")
	}
	return nil, nil
}

func (f *fakeJetStream) CreateOrUpdateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return fakeConsumer{}, nil
}

func (f *fakeJetStream) Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return &jetstream.PubAck{}, nil
}

type fakeConsumer struct{ jetstream.Consumer }

func (fakeConsumer) Consume(jetstream.MessageHandler, ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	return fakeConsumeContext{}, nil
}

type fakeConsumeContext struct{ jetstream.ConsumeContext }

func (fakeConsumeContext) Stop() {}

// TestQueueSweepIsACycleOfTheJobsWorker covers WP-447 (OPS-05, F-133): each
// sweep of Run is a cycle of the worker jobs (reticora_worker_runs_total
// {worker="jobs"}). A sweep without due jobs succeeds while the stream
// answers; a failed attempt of a job since the previous sweep and a stream
// that does not answer are failures of the cycle, so an idle queue does not
// report success while NATS is down.
func TestQueueSweepIsACycleOfTheJobsWorker(t *testing.T) {
	runs := func() float64 { return testutil.ToFloat64(observability.WorkerRuns.WithLabelValues("jobs")) }
	errs := func() float64 { return testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("jobs")) }
	last := func() float64 { return testutil.ToFloat64(observability.WorkerLastSuccess.WithLabelValues("jobs")) }
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: runs %v, errors %v", what, runs(), errs())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	s, _ := newService()
	js := &fakeJetStream{}
	q := NewQueue(s, js)
	q.SweepInterval = 10 * time.Millisecond
	q.Handle(Kind{Name: "sync", MaxAttempts: 1, Timeout: time.Minute},
		func(context.Context, *Job) (any, error) { return nil, errors.New("upstream down") })
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	beforeRuns, beforeErrs := runs(), errs()
	observability.WorkerLastSuccess.WithLabelValues("jobs").Set(0)
	go func() {
		q.Run(ctx)
		close(stopped)
	}()
	defer func() {
		cancel()
		<-stopped
	}()

	waitFor("an idle sweep", func() bool { return runs() >= beforeRuns+2 })
	if errs() != beforeErrs || last() == 0 {
		t.Errorf("idle sweeps: errors %v -> %v, last success %v; want cycles without error", beforeErrs, errs(), last())
	}

	// A failed attempt counts in the next sweep.
	job, _, err := s.Enqueue(ctx, "org-1", "sync", nil, EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out := q.Process(ctx, deliveryOf(t, job)); !out.Ran {
		t.Fatalf("attempt: %+v", out)
	}
	waitFor("the failed attempt in a cycle", func() bool { return errs() >= beforeErrs+1 })

	// NATS does not answer: the idle sweep fails.
	failedBefore := errs()
	js.down.Store(true)
	waitFor("a sweep while the stream does not answer", func() bool { return errs() >= failedBefore+2 })
	js.down.Store(false)
	answered := runs()
	waitFor("a sweep after the stream answers again", func() bool { return runs() >= answered+2 })
}
