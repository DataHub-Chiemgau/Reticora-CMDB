package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The worker probe_ingest (NFR-11, PRB-07, OPS-02, E-171, E-173, WP-960):
// a durable pull consumer of the stream RETICORA_PROBE (filter
// probe.results.>) fetches up to 500 result batches or what arrived within
// 1 s, groups them by organization and writes each organization in one
// tenant transaction (IngestStore, ingest_repository.go): the results with
// INSERT INTO probe_result_v … SELECT … FROM unnest(…) ON CONFLICT DO
// NOTHING, the ingest cursor and the credential usage. A message is
// acknowledged only after that commit, so a crash redelivers it and the
// idempotency key of probe_result makes the repetition a no-op. A message
// of an organization whose write failed is redelivered (Nak) and given up
// after IngestMaxDeliver deliveries (Term, dead letter); an undecodable one
// at once.

// Names and limits of the consumer.
const (
	// IngestConsumer is the durable consumer of the result batches.
	IngestConsumer = "reticora-probe-results"
	// IngestBatch is the most messages one fetch takes.
	IngestBatch = 500
	// IngestWait is the longest a fetch waits for IngestBatch messages.
	IngestWait = time.Second
	// IngestMaxDeliver gives a message up after this many deliveries.
	IngestMaxDeliver = 5
	// ingestNakDelay delays the redelivery of a failed message.
	ingestNakDelay = 2 * time.Second
)

// Reasons of the counters of the ingest.
const (
	DiscardUnknownProbe     = "unknown_probe"
	DiscardForeignCollector = "foreign_collector"
	DeadLetterDecode        = "undecodable"
	DeadLetterMaxDelive     = "max_deliver"
)

var (
	ingestDiscarded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "probe_ingest_discarded_total",
		Help: "Probe results the ingest dropped, by reason (unknown_probe: the probe does not exist or was deleted; foreign_collector: the sending collector is bound to another client than the probe, or unknown).",
	}, []string{"reason"})
	ingestDeadLetters = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "probe_ingest_dead_letters_total",
		Help: "Probe result batches the ingest gave up (terminated), by reason: undecodable or max_deliver.",
	}, []string{"reason"})
	ingestRows = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "reticora", Name: "probe_ingest_rows_total",
		Help: "Probe result rows the ingest wrote (repeated deliveries not counted).",
	})
)

// RegisterMetrics registers the metrics of the probe ingest and the clock
// offset of the collectors (WP-962).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(ingestDiscarded, ingestDeadLetters, ingestRows, stateDropped, stateDeadLetters, bridgeDiscarded, clockOffset)
}

// ResultsMessage is one result batch of a collector as the stream holds it.
type ResultsMessage struct {
	CollectorID string
	Results     []probewire.Result
}

// IngestOutcome counts what a write did.
type IngestOutcome struct {
	// Written rows; a repeated result is no row.
	Written int
	// UnknownProbe results were dropped: their probe does not exist.
	UnknownProbe int
	// ForeignCollector results were dropped: the sending collector is not
	// in the organization, or bound to another client than the probe.
	ForeignCollector int
	// Stale results carried another epoch than the assignment, or came
	// from another collector than the assigned one.
	Stale int
	// Samples are the metric samples the bridge wrote (WP-976).
	Samples int
}

// IngestStore writes the result batches of one organization in one
// transaction.
type IngestStore interface {
	WriteResults(ctx context.Context, orgID string, msgs []ResultsMessage) (IngestOutcome, error)
}

// ingestDelivery is a fetched message (jetstream.Msg).
type ingestDelivery interface {
	Subject() string
	Data() []byte
	Ack() error
	NakWithDelay(delay time.Duration) error
	Term() error
	Metadata() (*jetstream.MsgMetadata, error)
}

// IngestBacklog is the backlog of the consumer as the worker read it last
// (/readyz detail).
type IngestBacklog struct {
	Pending   uint64    `json:"pending"`
	SampledAt time.Time `json:"sampled_at"`
}

// ErrNoIngestBacklog: the worker has not read the backlog yet. It wraps
// discovery.ErrNoBacklogSample, which /readyz reports as "unavailable"
// without a warning (an expected state after the start).
var ErrNoIngestBacklog = fmt.Errorf("probe ingest: %w", discovery.ErrNoBacklogSample)

// Ingester is the worker probe_ingest.
type Ingester struct {
	js         jetstream.JetStream
	stream     StreamConfig
	store      IngestStore
	maxDeliver int
	batch      int
	wait       time.Duration

	mu       sync.Mutex
	consumer jetstream.Consumer
	sample   atomic.Pointer[IngestBacklog]
	// states applies the state changes in the same worker (WP-961); nil
	// runs the results only.
	states *StateApplier
}

// WithStates runs the consumer of the state changes in the worker
// probe_ingest (WP-961): it starts with Run, its backlog joins Backlog.
func (i *Ingester) WithStates(a *StateApplier) *Ingester {
	i.states = a
	return i
}

// NewIngester returns the worker on the stream cfg of js writing to store.
func NewIngester(js jetstream.JetStream, cfg StreamConfig, store IngestStore) *Ingester {
	return &Ingester{js: js, stream: cfg, store: store, maxDeliver: IngestMaxDeliver, batch: IngestBatch, wait: IngestWait}
}

// ConsumerConfig is the configuration of the durable consumer.
func (i *Ingester) ConsumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       IngestConsumer,
		FilterSubject: i.stream.SubjectPrefix + ".results.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    i.maxDeliver,
		MaxAckPending: 4 * i.batch,
	}
}

// setup creates or updates the consumer (once per process; again after an
// error).
func (i *Ingester) setup(ctx context.Context) (jetstream.Consumer, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.consumer != nil {
		return i.consumer, nil
	}
	c, err := i.js.CreateOrUpdateConsumer(ctx, i.stream.Name, i.ConsumerConfig())
	if err != nil {
		return nil, fmt.Errorf("probe ingest consumer: %w", err)
	}
	i.consumer = c
	return c, nil
}

// reset drops the consumer handle after a failure.
func (i *Ingester) reset() {
	i.mu.Lock()
	i.consumer = nil
	i.mu.Unlock()
}

// Run fetches and writes until ctx ends; every fetch is a cycle of the
// worker (reticora_worker_runs_total, a failed one counts as error).
func (i *Ingester) Run(ctx context.Context) {
	if i.states != nil {
		go i.states.Run(ctx)
	}
	for ctx.Err() == nil {
		if err := i.RunOnce(ctx); err != nil && ctx.Err() == nil {
			observability.ObserveWorkerRun(config.WorkerProbeIngest, 1)
			slog.Warn("probe ingest fetch failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		observability.ObserveWorkerRun(config.WorkerProbeIngest, 0)
	}
}

// RunOnce fetches one batch and writes it.
func (i *Ingester) RunOnce(ctx context.Context) error {
	c, err := i.setup(ctx)
	if err != nil {
		return err
	}
	batch, err := c.Fetch(i.batch, jetstream.FetchMaxWait(i.wait))
	if err != nil {
		i.reset()
		return err
	}
	msgs := make([]ingestDelivery, 0, i.batch)
	for m := range batch.Messages() {
		msgs = append(msgs, m)
	}
	if ferr := batch.Error(); ferr != nil && !errors.Is(ferr, nats.ErrTimeout) && !errors.Is(ferr, context.DeadlineExceeded) &&
		len(msgs) == 0 {
		i.reset()
		return ferr
	}
	i.process(ctx, msgs)
	return nil
}

// orgBatch is the messages of one organization in a fetch.
type orgBatch struct {
	org        string
	msgs       []ResultsMessage
	deliveries []ingestDelivery
}

// groupByOrganization decodes the fetched messages and groups them by the
// organization of their subject (<prefix>.results.<org>.<collector>), in
// the order of their first message. Undecodable messages come back apart.
func (i *Ingester) groupByOrganization(msgs []ingestDelivery) (groups []*orgBatch, bad []ingestDelivery) {
	byOrg := map[string]*orgBatch{}
	prefix := i.stream.SubjectPrefix + ".results."
	for _, d := range msgs {
		rest, ok := strings.CutPrefix(d.Subject(), prefix)
		org, collector, found := strings.Cut(rest, ".")
		if !ok || !found || !api.IsUUID(org) || !api.IsUUID(collector) {
			bad = append(bad, d)
			continue
		}
		var rb probewire.ResultBatch
		if err := probewire.Decode(d.Data(), &rb); err != nil || len(rb.Results) == 0 {
			bad = append(bad, d)
			continue
		}
		g := byOrg[org]
		if g == nil {
			g = &orgBatch{org: org}
			byOrg[org] = g
			groups = append(groups, g)
		}
		g.msgs = append(g.msgs, ResultsMessage{CollectorID: strings.ToLower(collector), Results: rb.Results})
		g.deliveries = append(g.deliveries, d)
	}
	return groups, bad
}

// process writes the fetched messages per organization and acknowledges
// them after the commit; see the package comment.
func (i *Ingester) process(ctx context.Context, msgs []ingestDelivery) {
	groups, bad := i.groupByOrganization(msgs)
	for _, d := range bad {
		_ = d.Term()
		ingestDeadLetters.WithLabelValues(DeadLetterDecode).Inc()
		slog.Warn("probe ingest: undecodable result batch given up", "subject", d.Subject())
	}
	for _, g := range groups {
		out, err := i.store.WriteResults(ctx, g.org, g.msgs)
		if err != nil {
			slog.Warn("probe ingest: write failed", "organization_id", g.org, "messages", len(g.msgs), "error", err)
			for _, d := range g.deliveries {
				i.retry(d)
			}
			continue
		}
		ingestRows.Add(float64(out.Written))
		if out.UnknownProbe > 0 {
			ingestDiscarded.WithLabelValues(DiscardUnknownProbe).Add(float64(out.UnknownProbe))
		}
		if out.ForeignCollector > 0 {
			ingestDiscarded.WithLabelValues(DiscardForeignCollector).Add(float64(out.ForeignCollector))
		}
		for _, d := range g.deliveries {
			_ = d.Ack()
		}
	}
}

// retry redelivers a failed message, or gives it up after maxDeliver
// deliveries.
func (i *Ingester) retry(d ingestDelivery) {
	meta, err := d.Metadata()
	if err == nil && meta.NumDelivered >= uint64(i.maxDeliver) { //nolint:gosec // maxDeliver is small and positive
		_ = d.Term()
		ingestDeadLetters.WithLabelValues(DeadLetterMaxDelive).Inc()
		slog.Error("probe ingest: result batch given up after the last delivery", "subject", d.Subject(),
			"deliveries", meta.NumDelivered)
		return
	}
	_ = d.NakWithDelay(ingestNakDelay)
}

// Backlog reads the messages of the consumer not yet acknowledged
// (pending plus delivered and unacknowledged) and keeps the sample for
// /readyz.
func (i *Ingester) Backlog(ctx context.Context) (int, error) {
	c, err := i.setup(ctx)
	if err != nil {
		return 0, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		i.reset()
		return 0, err
	}
	n := info.NumPending + uint64(info.NumAckPending) //nolint:gosec // a count
	if i.states != nil {
		states, serr := i.states.Backlog(ctx)
		if serr != nil {
			return 0, serr
		}
		n += uint64(states) //nolint:gosec // a count
	}
	i.sample.Store(&IngestBacklog{Pending: n, SampledAt: time.Now().UTC()})
	return int(min(n, uint64(1<<31-1))), nil //nolint:gosec // bounded above
}

// ReadinessDetail is the /readyz detail: the backlog Backlog read last. It
// never asks NATS (the endpoint is unauthenticated).
func (i *Ingester) ReadinessDetail(context.Context) (any, error) {
	b := i.sample.Load()
	if b == nil {
		return nil, ErrNoIngestBacklog
	}
	return b, nil
}

// IngestDiscarded returns the counter of dropped results of reason (tests
// and diagnostics).
func IngestDiscarded(reason string) prometheus.Counter {
	return ingestDiscarded.WithLabelValues(reason)
}

// IngestDeadLetters returns the counter of given up batches of reason.
func IngestDeadLetters(reason string) prometheus.Counter {
	return ingestDeadLetters.WithLabelValues(reason)
}

// IngestRows returns the counter of written rows (tests and diagnostics).
func IngestRows() prometheus.Counter { return ingestRows }
