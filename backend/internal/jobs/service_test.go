package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// clock is a settable test clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newService() (*Service, *clock) {
	c := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	s := NewService(NewMemoryRepository()).WithClock(c.now)
	s.Register(Kind{Name: "export", MaxAttempts: 3, Timeout: time.Minute, Backoff: ExponentialBackoff(10*time.Second, time.Minute)})
	return s, c
}

// TestJobLifecycle covers JOB-01 and REP-01 (WP-129) on the generic job
// model: queued → running → succeeded with progress and result; the
// allowed transitions; idempotent creation; unknown kinds.
func TestJobLifecycle(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()

	job, created, err := s.Enqueue(ctx, "org-1", "export", map[string]string{"format": "csv"}, EnqueueOptions{IdempotencyKey: "k1", CreatedBy: "u1"})
	if err != nil || !created || job.Status != StatusQueued || job.MaxAttempts != 3 || job.TimeoutSeconds != 60 || string(job.Params) != `{"format":"csv"}` {
		t.Fatalf("enqueue: %+v created=%v err=%v", job, created, err)
	}
	again, created, err := s.Enqueue(ctx, "org-1", "export", map[string]string{"format": "json"}, EnqueueOptions{IdempotencyKey: "k1"})
	if err != nil || created || again.ID != job.ID || string(again.Params) != `{"format":"csv"}` {
		t.Errorf("same idempotency key: %+v created=%v err=%v", again, created, err)
	}
	if other, created, _ := s.Enqueue(ctx, "org-2", "export", nil, EnqueueOptions{IdempotencyKey: "k1"}); !created || other.ID == job.ID {
		t.Error("the idempotency key of another organization was shared")
	}
	if _, _, err = s.Enqueue(ctx, "org-1", "nope", nil, EnqueueOptions{}); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unknown kind: %v", err)
	}

	claimed, err := s.Claim(ctx, "org-1", "export")
	if err != nil || claimed == nil || claimed.ID != job.ID || claimed.Status != StatusRunning || claimed.Attempts != 1 || claimed.StartedAt == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if next, _ := s.Claim(ctx, "org-1", "export"); next != nil {
		t.Errorf("a running job was claimed twice: %+v", next)
	}
	if err = s.Progress(ctx, "org-1", job.ID, 140); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "org-1", job.ID); got.Progress != 100 {
		t.Errorf("progress is capped at 100: %d", got.Progress)
	}
	done, err := s.Succeed(ctx, "org-1", job.ID, map[string]int{"rows": 42})
	if err != nil || done.Status != StatusSucceeded || done.FinishedAt == nil || string(done.Result) != `{"rows":42}` {
		t.Fatalf("succeed: %+v %v", done, err)
	}
	if _, err = s.Cancel(ctx, "org-1", job.ID); !errors.Is(err, ErrTransition) {
		t.Errorf("cancel a finished job: %v", err)
	}
	if _, err = s.Get(ctx, "org-2", job.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization read the job: %v", err)
	}

	for _, tc := range []struct {
		from, to Status
		ok       bool
	}{
		{StatusQueued, StatusRunning, true}, {StatusQueued, StatusCancelled, true}, {StatusQueued, StatusSucceeded, false},
		{StatusRunning, StatusQueued, true}, {StatusRunning, StatusFailed, true}, {StatusSucceeded, StatusRunning, false},
		{StatusFailed, StatusQueued, false}, {StatusCancelled, StatusRunning, false},
	} {
		if CanTransition(tc.from, tc.to) != tc.ok {
			t.Errorf("%s → %s: %v, want %v", tc.from, tc.to, !tc.ok, tc.ok)
		}
	}
	list, total, _ := s.List(ctx, "org-1", Filter{Status: StatusSucceeded}, api.PaginationParams{Limit: 10})
	if total != 1 || len(list) != 1 {
		t.Errorf("list succeeded: %d %+v", total, list)
	}
}

// TestJobRetryTimeoutAndCancel: a failed attempt goes back to the queue
// after the backoff of its kind until the attempts are used up; an attempt
// beyond its timeout fails; a queued or running job can be cancelled.
func TestJobRetryTimeoutAndCancel(t *testing.T) {
	s, c := newService()
	ctx := context.Background()
	job, _, _ := s.Enqueue(ctx, "org-1", "export", nil, EnqueueOptions{})

	for attempt, wait := range []time.Duration{10 * time.Second, 20 * time.Second} {
		if _, err := s.Claim(ctx, "org-1", "export"); err != nil {
			t.Fatal(err)
		}
		failed, err := s.Fail(ctx, "org-1", job.ID, errors.New("disk full"))
		if err != nil || failed.Status != StatusQueued || failed.Error != "disk full" || !failed.RunAfter.Equal(c.now().Add(wait)) {
			t.Fatalf("attempt %d: %+v %v", attempt+1, failed, err)
		}
		if early, _ := s.Claim(ctx, "org-1", "export"); early != nil {
			t.Fatalf("attempt %d: claimed before the backoff", attempt+1)
		}
		c.advance(wait)
	}
	if _, err := s.Claim(ctx, "org-1", "export"); err != nil {
		t.Fatal(err)
	}
	final, err := s.Fail(ctx, "org-1", job.ID, errors.New("disk full"))
	if err != nil || final.Status != StatusFailed || final.Attempts != 3 || final.FinishedAt == nil {
		t.Fatalf("last attempt: %+v %v", final, err)
	}

	slow, _, _ := s.Enqueue(ctx, "org-1", "export", nil, EnqueueOptions{})
	if _, err = s.Claim(ctx, "org-1", "export"); err != nil {
		t.Fatal(err)
	}
	c.advance(30 * time.Second)
	if n, _ := s.ReapTimeouts(ctx, "org-1"); n != 0 {
		t.Errorf("reaped %d jobs within their timeout", n)
	}
	c.advance(time.Minute)
	if n, err := s.ReapTimeouts(ctx, "org-1"); n != 1 || err != nil {
		t.Errorf("reap: %d %v", n, err)
	}
	if got, _ := s.Get(ctx, "org-1", slow.ID); got.Status != StatusQueued || got.Error == "" {
		t.Errorf("timed-out job: %+v, want queued for a retry with the timeout as error", got)
	}

	queued, _, _ := s.Enqueue(ctx, "org-1", "export", nil, EnqueueOptions{RunAfter: c.now().Add(time.Hour)})
	if got, err := s.Cancel(ctx, "org-1", queued.ID); err != nil || got.Status != StatusCancelled || got.FinishedAt == nil {
		t.Errorf("cancel queued: %+v %v", got, err)
	}
}

// TestTrackerMirrorsSpecialisedJobs: an export or discovery row registers a
// generic job and the generic job follows the row's status (adapter).
func TestTrackerMirrorsSpecialisedJobs(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	var tracker Tracker = s
	if err := tracker.Track(ctx, "org-1", "export", "export_job", "ref-1", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Track(ctx, "org-1", "export", "export_job", "ref-1", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.List(ctx, "org-1", Filter{}, api.PaginationParams{Limit: 10}); total != 1 {
		t.Errorf("tracking twice created %d jobs", total)
	}
	for _, step := range []struct {
		status Status
		msg    string
	}{{StatusRunning, ""}, {StatusFailed, "renderer crashed"}} {
		if err := tracker.Mirror(ctx, "org-1", "export_job", "ref-1", step.status, step.msg); err != nil {
			t.Fatal(err)
		}
	}
	job, _ := s.repo.GetByRef(ctx, "org-1", "export_job", "ref-1")
	if job.Status != StatusFailed || job.Error != "renderer crashed" || job.Attempts != 1 || job.FinishedAt == nil || job.Kind != "export" {
		t.Errorf("mirrored job: %+v", job)
	}
	// An untracked row is tracked on its first status change; a change the
	// model does not allow is ignored.
	if err := tracker.Mirror(ctx, "org-1", "discovery_job", "ref-2", StatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Mirror(ctx, "org-1", "discovery_job", "ref-2", StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	if job, _ = s.repo.GetByRef(ctx, "org-1", "discovery_job", "ref-2"); job.Status != StatusSucceeded || job.Progress != 100 {
		t.Errorf("late tracked job: %+v", job)
	}
}

// TestCancelEndsTheRunContext covers WP-514 (JOB-03, F-162) on the
// in-process path: the context RunDetached hands to the run ends with
// ErrCancelled when the job is cancelled, at once in this process and
// within the poll interval when the cancel comes from another replica (the
// table changes under it); a result or an error arriving after the cancel
// does not overwrite the status. A running job of a kind that cannot be
// cancelled is refused and keeps running.
func TestCancelEndsTheRunContext(t *testing.T) {
	s := NewService(NewMemoryRepository()).WithCancelPoll(10 * time.Millisecond)
	s.Register(Kind{Name: "snap", MaxAttempts: 1, Cancellable: true})
	s.Register(Kind{Name: "merge", MaxAttempts: 1})
	ctx := context.Background()
	waitRunning := func(id string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if j, _ := s.Get(ctx, "org-1", id); j != nil && j.Status == StatusRunning {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s did not start", id)
			}
			time.Sleep(time.Millisecond)
		}
	}
	// keeps checks that the job stays cancelled without result or error
	// after its run returned.
	keeps := func(name, id string, returned <-chan struct{}) {
		t.Helper()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the run did not end after the cancel", name)
		}
		time.Sleep(50 * time.Millisecond) // let RunDetached record what it would
		if j, _ := s.Get(ctx, "org-1", id); j.Status != StatusCancelled || len(j.Result) != 0 || j.Error != "" {
			t.Errorf("%s: job after the late end: %+v, want cancelled without result or error", name, j)
		}
	}

	for _, tc := range []struct {
		name   string
		cancel func(id string)
		fail   bool
	}{
		{"cancel in this process", func(id string) {
			if _, err := s.Cancel(ctx, "org-1", id); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"cancel on another replica", func(id string) {
			now := time.Now()
			if _, err := s.repo.Transition(ctx, "org-1", id, []Status{StatusRunning}, &Update{Status: StatusCancelled, FinishedAt: &now}); err != nil {
				t.Fatal(err)
			}
		}, true},
	} {
		returned := make(chan struct{})
		var cause error
		job, err := s.RunDetached(ctx, "org-1", "snap", nil, "u1", func(run context.Context) (any, error) {
			defer close(returned)
			<-run.Done()
			cause = context.Cause(run)
			if tc.fail {
				return nil, run.Err()
			}
			return map[string]int{"late": 1}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		waitRunning(job.ID)
		tc.cancel(job.ID)
		keeps(tc.name, job.ID, returned)
		if !errors.Is(cause, ErrCancelled) {
			t.Errorf("%s: cause of the run context = %v, want ErrCancelled", tc.name, cause)
		}
	}

	// A run that ignores its context: its result after the cancel is
	// dropped.
	release, returned := make(chan struct{}), make(chan struct{})
	job, err := s.RunDetached(ctx, "org-1", "snap", nil, "u1", func(context.Context) (any, error) {
		defer close(returned)
		<-release
		return map[string]int{"late": 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(job.ID)
	if _, err = s.Cancel(ctx, "org-1", job.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	keeps("late result", job.ID, returned)
	if _, err = s.Succeed(ctx, "org-1", job.ID, "late"); !errors.Is(err, ErrTransition) {
		t.Errorf("succeed a cancelled job: %v, want ErrTransition", err)
	}

	// Not cancellable while running: refused, the run goes on and ends.
	release, returned = make(chan struct{}), make(chan struct{})
	merge, err := s.RunDetached(ctx, "org-1", "merge", nil, "u1", func(run context.Context) (any, error) {
		defer close(returned)
		select {
		case <-release:
		case <-run.Done():
			return nil, run.Err()
		}
		return "merged", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(merge.ID)
	if _, err = s.Cancel(ctx, "org-1", merge.ID); !errors.Is(err, ErrNotCancellable) {
		t.Errorf("cancel a running merge: %v, want ErrNotCancellable", err)
	}
	close(release)
	<-returned
	deadline := time.Now().Add(5 * time.Second)
	for j, _ := s.Get(ctx, "org-1", merge.ID); j.Status != StatusSucceeded; j, _ = s.Get(ctx, "org-1", merge.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("merge after the refused cancel: %+v, want succeeded", j)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = s.SaveCancelledResult(ctx, "org-1", merge.ID, "x"); !errors.Is(err, ErrTransition) {
		t.Errorf("keep a result on a succeeded job: %v, want ErrTransition", err)
	}
}

// TestQueueHandlerOfACancelledJob covers WP-514 (JOB-03, F-162) on the job
// queue: the handler's context ends when the job is cancelled; whatever the
// handler returns then, the message is acknowledged without redelivery and
// the job keeps its status cancelled without result or error.
func TestQueueHandlerOfACancelledJob(t *testing.T) {
	s := NewService(NewMemoryRepository()).WithCancelPoll(10 * time.Millisecond)
	q := NewQueue(s, nil)
	ctx := context.Background()
	fail := false
	running := make(chan string, 1)
	q.Handle(Kind{Name: "history.snapshot", MaxAttempts: 3, Timeout: time.Minute, Cancellable: true},
		func(run context.Context, job *Job) (any, error) {
			running <- job.ID
			<-run.Done()
			if fail {
				return nil, run.Err()
			}
			return map[string]string{"state": "late"}, nil
		})
	for _, fail = range []bool{false, true} {
		job, _, err := q.Submit(ctx, "org-1", "history.snapshot", nil, EnqueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		d := deliveryOf(t, job)
		out := make(chan Outcome)
		go func() { out <- q.Process(ctx, d) }()
		if _, err = s.Cancel(ctx, "org-1", <-running); err != nil {
			t.Fatal(err)
		}
		if o := <-out; !o.Ran || !o.Acked || d.acks != 1 || len(d.naks) != 0 {
			t.Errorf("failing %v: outcome %+v, acks %d, naks %v; want acknowledged once", fail, o, d.acks, d.naks)
		}
		if got, _ := s.Get(ctx, "org-1", job.ID); got.Status != StatusCancelled || len(got.Result) != 0 || got.Error != "" || got.Attempts != 1 {
			t.Errorf("failing %v: job %+v, want cancelled after one attempt without result or error", fail, got)
		}
	}
}
