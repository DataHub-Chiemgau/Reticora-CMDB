package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// Distribution over NATS (TEC-14): a job is stored in the job table and
// announced on jobs.<kind> in a JetStream work-queue stream; one durable
// consumer per kind hands each message to one worker, which acknowledges
// it once the job has ended and leaves it unacknowledged if the process
// dies. The database claim (queued → running in one statement) decides who
// runs a job, so a message delivered twice — after a redelivery, a
// republish or to two replicas — runs the job once. A job whose worker died
// stays running until its timeout; the redelivered message then fails the
// attempt and claims the job again.

// Stream defaults.
const (
	DefaultStream        = "JOBS"
	DefaultSubjectPrefix = "jobs."
	// DefaultAckMargin is added to the timeout of a kind for the AckWait of
	// its consumer.
	DefaultAckMargin = 30 * time.Second
	// DefaultSweepInterval is how often due queued jobs are announced again.
	DefaultSweepInterval = time.Minute
	// retryDelay is the redelivery delay after a storage error.
	retryDelay = 5 * time.Second
)

// Handler runs one attempt of a job; its result is stored on success.
type Handler func(ctx context.Context, job *Job) (result any, err error)

// Message announces a job on the stream.
type Message struct {
	OrganizationID string `json:"organization_id"`
	JobID          string `json:"job_id"`
	Kind           string `json:"kind"`
}

// Delivery is the part of a JetStream message the queue uses.
type Delivery interface {
	Data() []byte
	Ack() error
	NakWithDelay(delay time.Duration) error
}

// Outcome is what Process did with a delivery.
type Outcome struct {
	// Ran reports whether the handler ran.
	Ran bool
	// Acked is true when the message was acknowledged; otherwise it is
	// redelivered after Delay.
	Acked bool
	Delay time.Duration
}

// Queue distributes the jobs of a Service over NATS.
type Queue struct {
	svc           *Service
	js            jetstream.JetStream
	Stream        string
	SubjectPrefix string
	AckMargin     time.Duration
	SweepInterval time.Duration

	mu       sync.RWMutex
	handlers map[string]Handler
	// failures counts what failed since the last sweep: storage errors,
	// failed attempts and errors of the consumers (OPS-05).
	failures atomic.Int64
}

// NewQueue creates a queue for svc on js; js may be nil when only Process
// is used (tests).
func NewQueue(svc *Service, js jetstream.JetStream) *Queue {
	return &Queue{
		svc: svc, js: js, Stream: DefaultStream, SubjectPrefix: DefaultSubjectPrefix,
		AckMargin: DefaultAckMargin, SweepInterval: DefaultSweepInterval, handlers: map[string]Handler{},
	}
}

// Handle registers the kind with the service and its handler with the
// queue.
func (q *Queue) Handle(k Kind, h Handler) {
	q.svc.Register(k)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[k.Name] = h
}

func (q *Queue) handler(kind string) (Handler, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	h, ok := q.handlers[kind]
	return h, ok
}

// Handles reports whether the queue runs jobs of kind.
func (q *Queue) Handles(kind string) bool {
	_, ok := q.handler(kind)
	return ok
}

// Setup creates or updates the work-queue stream.
func (q *Queue) Setup(ctx context.Context) error {
	_, err := q.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      q.Stream,
		Subjects:  []string{q.SubjectPrefix + ">"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.FileStorage,
	})
	if err != nil {
		return fmt.Errorf("ensure stream %s: %w", q.Stream, err)
	}
	return nil
}

// Submit stores a job and announces it. A job that exists already (same
// idempotency key) is announced again while it is queued; the claim keeps
// it from running twice. A failed announcement leaves the job queued for
// the sweep. A kind without a handler (Handle) is refused (ErrUnknownKind):
// its job would stay queued, since no worker consumes it.
func (q *Queue) Submit(ctx context.Context, orgID, kind string, params any, opts EnqueueOptions) (*Job, bool, error) {
	if !q.Handles(kind) {
		return nil, false, fmt.Errorf("%w: %s has no handler on the job queue", ErrUnknownKind, kind)
	}
	job, created, err := q.svc.Enqueue(ctx, orgID, kind, params, opts)
	if err != nil {
		return nil, false, err
	}
	if job.Status == StatusQueued {
		if perr := q.publish(ctx, job); perr != nil {
			slog.Warn("announce job", "job", job.ID, "kind", kind, "error", perr)
		}
	}
	return job, created, nil
}

// publish announces a job; the job id is the message id, so JetStream
// drops a repeated announcement within its duplicate window.
func (q *Queue) publish(ctx context.Context, job *Job) error {
	if q.js == nil {
		return errors.New("no JetStream connection")
	}
	data, err := json.Marshal(Message{OrganizationID: job.OrganizationID, JobID: job.ID, Kind: job.Kind})
	if err != nil {
		return err
	}
	_, err = q.js.Publish(ctx, q.SubjectPrefix+job.Kind, data, jetstream.WithMsgID(job.ID+"/"+fmt.Sprint(job.Attempts)))
	return err
}

// Process handles one delivery: it claims the job, runs its handler and
// records the result. It acknowledges the message once the job needs no
// further run from it and asks for a redelivery otherwise.
func (q *Queue) Process(ctx context.Context, d Delivery) Outcome {
	var m Message
	if err := json.Unmarshal(d.Data(), &m); err != nil || m.OrganizationID == "" || m.JobID == "" {
		slog.Warn("drop malformed job message", "error", err)
		return q.ack(d, false)
	}
	return q.process(ctx, d, &m, true)
}

func (q *Queue) process(ctx context.Context, d Delivery, m *Message, mayReclaim bool) Outcome {
	job, err := q.svc.ClaimJob(ctx, m.OrganizationID, m.JobID)
	if errors.Is(err, ErrNotFound) {
		return q.ack(d, false)
	}
	if err != nil {
		q.failures.Add(1)
		slog.Error("claim job", "job", m.JobID, "error", err)
		return q.nak(d, retryDelay)
	}
	if job == nil {
		return q.notClaimed(ctx, d, m, mayReclaim)
	}

	h, ok := q.handler(job.Kind)
	if !ok {
		q.failures.Add(1)
		_, _ = q.svc.Fail(ctx, job.OrganizationID, job.ID, fmt.Errorf("%w: %s", ErrUnknownKind, job.Kind))
		return q.nak(d, retryDelay)
	}
	timeout := time.Duration(job.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	result, herr := h(runCtx, job)
	cancel()
	if ctx.Err() != nil {
		// Shutdown: the job stays running and the message unacknowledged;
		// after the timeout another worker takes the job over.
		return Outcome{Ran: true}
	}
	if herr == nil {
		if _, err = q.svc.Succeed(ctx, job.OrganizationID, job.ID, result); err != nil && !errors.Is(err, ErrTransition) {
			q.failures.Add(1)
			slog.Error("record job success", "job", job.ID, "error", err)
		}
		return q.ack(d, true)
	}
	// A failed attempt counts as a failure of the worker, as in the export
	// worker; the job is retried after its backoff or fails.
	q.failures.Add(1)
	failed, err := q.svc.Fail(ctx, job.OrganizationID, job.ID, herr)
	if err != nil {
		q.failures.Add(1)
		slog.Error("record job failure", "job", job.ID, "error", err)
		out := q.nak(d, retryDelay)
		out.Ran = true
		return out
	}
	if failed.Status == StatusQueued {
		out := q.nak(d, max(0, failed.RunAfter.Sub(q.svc.now())))
		out.Ran = true
		return out
	}
	return q.ack(d, true)
}

// notClaimed decides about a message whose job could not be claimed.
func (q *Queue) notClaimed(ctx context.Context, d Delivery, m *Message, mayReclaim bool) Outcome {
	job, err := q.svc.Get(ctx, m.OrganizationID, m.JobID)
	if errors.Is(err, ErrNotFound) {
		return q.ack(d, false)
	}
	if err != nil {
		q.failures.Add(1)
		slog.Error("read job", "job", m.JobID, "error", err)
		return q.nak(d, retryDelay)
	}
	now := q.svc.now()
	switch {
	case job.Status.Terminal():
		// Already done: a duplicate delivery.
		return q.ack(d, false)
	case job.Status == StatusQueued:
		return q.nak(d, max(0, job.RunAfter.Sub(now)))
	}
	// Running: elsewhere, or its worker died. Past the timeout the attempt
	// is failed and the job taken over.
	timeout := time.Duration(job.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if job.StartedAt == nil || now.Sub(*job.StartedAt) <= timeout {
		started := now
		if job.StartedAt != nil {
			started = *job.StartedAt
		}
		return q.nak(d, max(retryDelay, started.Add(timeout).Sub(now)))
	}
	failed, err := q.svc.Fail(ctx, job.OrganizationID, job.ID, errWorkerLost(timeout))
	if err != nil {
		// Taken over or ended meanwhile (ErrTransition) or a storage error.
		if !errors.Is(err, ErrTransition) {
			q.failures.Add(1)
			slog.Error("take over job", "job", job.ID, "error", err)
		}
		return q.nak(d, retryDelay)
	}
	if failed.Status != StatusQueued {
		return q.ack(d, false)
	}
	if mayReclaim && !failed.RunAfter.After(now) {
		return q.process(ctx, d, m, false)
	}
	return q.nak(d, max(0, failed.RunAfter.Sub(now)))
}

func (q *Queue) ack(d Delivery, ran bool) Outcome {
	if err := d.Ack(); err != nil {
		slog.Warn("ack job message", "error", err)
	}
	return Outcome{Ran: ran, Acked: true}
}

func (q *Queue) nak(d Delivery, delay time.Duration) Outcome {
	if err := d.NakWithDelay(delay); err != nil {
		slog.Warn("nak job message", "error", err)
	}
	return Outcome{Delay: delay}
}

// Run consumes the registered kinds and announces due queued jobs again
// every SweepInterval (jobs whose announcement was lost), until ctx ends.
func (q *Queue) Run(ctx context.Context) {
	if err := q.consume(ctx); err != nil {
		slog.Error("job queue", "error", err)
		return
	}
	ticker := time.NewTicker(q.SweepInterval)
	defer ticker.Stop()
	for {
		q.cycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cycle is one cycle of the worker jobs (OPS-05): a sweep, recorded with
// its failures and those since the previous sweep.
func (q *Queue) cycle(ctx context.Context) {
	failures := q.Sweep(ctx)
	if ctx.Err() != nil {
		return
	}
	observability.ObserveWorkerRun("jobs", failures+int(q.failures.Swap(0)))
}

// consume starts one durable consumer per registered kind.
func (q *Queue) consume(ctx context.Context) error {
	q.mu.RLock()
	kinds := make([]string, 0, len(q.handlers))
	for k := range q.handlers {
		kinds = append(kinds, k)
	}
	q.mu.RUnlock()
	for _, name := range kinds {
		k, err := q.svc.kind(name)
		if err != nil {
			return err
		}
		consumer, err := q.js.CreateOrUpdateConsumer(ctx, q.Stream, jetstream.ConsumerConfig{
			Durable:       "jobs-" + consumerToken(name),
			FilterSubject: q.SubjectPrefix + name,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       k.Timeout + q.AckMargin,
			MaxDeliver:    -1,
		})
		if err != nil {
			return fmt.Errorf("ensure consumer for %s: %w", name, err)
		}
		cc, err := consumer.Consume(func(msg jetstream.Msg) { q.Process(ctx, msg) },
			jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, cerr error) {
				q.failures.Add(1)
				slog.Warn("consume jobs", "kind", name, "error", cerr)
			}))
		if err != nil {
			return fmt.Errorf("consume %s: %w", name, err)
		}
		go func() {
			<-ctx.Done()
			cc.Stop()
		}()
	}
	return nil
}

// Sweep announces the registered kinds' queued jobs again that have been
// due for longer than SweepInterval: their announcement was lost or the
// queue is behind; the claim discards the duplicates of the latter. It
// also asks for the stream, so that a sweep without due jobs fails while
// JetStream does not answer, and returns its failures.
func (q *Queue) Sweep(ctx context.Context) (failures int) {
	if q.js == nil {
		failures++
		slog.Warn("job stream", "stream", q.Stream, "error", "no JetStream connection")
	} else if _, err := q.js.Stream(ctx, q.Stream); err != nil {
		failures++
		slog.Warn("job stream", "stream", q.Stream, "error", err)
	}
	due, err := q.svc.repo.QueuedDue(ctx, q.svc.now().Add(-q.SweepInterval), 500)
	if err != nil {
		slog.Warn("list due jobs", "error", err)
		return failures + 1
	}
	for i := range due {
		if _, ok := q.handler(due[i].Kind); !ok {
			continue
		}
		if err := q.publish(ctx, &due[i]); err != nil {
			failures++
			slog.Warn("announce job", "job", due[i].ID, "error", err)
		}
	}
	return failures
}

// consumerToken keeps a kind usable in a consumer name.
func consumerToken(kind string) string {
	out := []rune(kind)
	for i, r := range out {
		if r == '.' || r == '*' || r == '>' || r == ' ' {
			out[i] = '_'
		}
	}
	return string(out)
}
