package probe

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The state changes of the probes on the server (PRB-05, PRB-07, PRB-11,
// WP-961, E-175, E-180): a durable consumer reticora-probe-state of the
// stream RETICORA_PROBE (filter probe.state.>) runs in the worker
// probe_ingest. It spreads the fetched changes over lanes by probe id, so
// the changes of one probe are applied one after the other in the order of
// delivery. Each change is written in one tenant transaction:
// probe_state_change (ON CONFLICT DO NOTHING on (probe_id, collector_id,
// epoch, seq): a repeated delivery writes nothing and is counted), the
// probe_state row when the change applies, the ingest cursor of the stream
// state and, when it applied, the event probe.state_changed as the last
// write (Konvention 20). The message is acknowledged after the commit.
//
// Rule (E-175): a change applies when its epoch is the epoch of the probe's
// assignment, it comes from the assigned collector and its epoch is above
// the state's or, in the same epoch, its sequence is above the state's last
// one. A change of an older epoch, of another collector or without an
// assignment is history only (stale, notify false, no event); a change of a
// newer epoch than the assignment is refused (counted, not written); an
// older sequence of the current epoch is history only.
//
// late (E-180) is observed_at < received_at − 2 × effective interval; a
// late change is notify false when a younger change of the same probe
// follows in the same fetch that the rule would apply (same epoch as the
// assignment, from the assigned collector), otherwise it is the current
// state and notifies. notify is stored for every change that is not stale,
// also for one that only enters the history, so the same sequence of
// changes gives the same history in any order of delivery; only an
// applied change is published.
//
// The changes of one probe are serialized by the transaction lock
// probe_state:<probe> (also the first one, before a probe_state row
// exists), and the upsert of probe_state never moves the state back.
// When a change of a probe fails in a lane, the later changes of that
// probe in the same fetch go back unapplied (NakWithDelay), so a probe's
// changes are never applied out of the order of delivery.

// Names and limits of the consumer.
const (
	StateConsumer     = "reticora-probe-state"
	StateBatch        = 500
	StateWait         = 500 * time.Millisecond
	StateMaxDeliver   = 5
	stateLanes        = 8
	stateNakDelay     = 2 * time.Second
	stateLateInterval = 2
)

// Reasons of the counters of the state changes.
const (
	StateDuplicate    = "duplicate"
	StateFutureEpoch  = "future_epoch"
	StateUnknownProbe = "unknown_probe"
)

var (
	stateDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "probe_state_dropped_total",
		Help: "Probe state changes the worker did not write, by reason (duplicate, future_epoch, unknown_probe).",
	}, []string{"reason"})
	stateDeadLetters = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "probe_state_dead_letters_total",
		Help: "Probe state changes the worker gave up (terminated), by reason: undecodable or max_deliver.",
	}, []string{"reason"})
)

// StateDropped returns the counter of dropped changes of reason (tests).
func StateDropped(reason string) prometheus.Counter { return stateDropped.WithLabelValues(reason) }

// Decision is what the rule makes of a change.
type Decision int

// Decisions of the rule.
const (
	// DecisionApply: the change sets the state and is published.
	DecisionApply Decision = iota
	// DecisionHistory: kept in the history only (an older sequence of the
	// current epoch).
	DecisionHistory
	// DecisionStale: kept in the history as stale (older epoch, another
	// collector, no assignment).
	DecisionStale
	// DecisionRefuse: a newer epoch than the assignment, not written.
	DecisionRefuse
)

// ApplyInput is what the rule compares.
type ApplyInput struct {
	Assigned          bool
	AssignedEpoch     int64
	AssignedCollector string
	HasState          bool
	StateEpoch        int64
	StateSeq          int64
	Epoch             int64
	Seq               int64
	Collector         string
}

// Decide applies the rule of E-175 to a change.
func Decide(in *ApplyInput) Decision {
	switch {
	case !in.Assigned:
		return DecisionStale
	case in.Epoch > in.AssignedEpoch:
		return DecisionRefuse
	case in.Epoch < in.AssignedEpoch, in.Collector != in.AssignedCollector:
		return DecisionStale
	case !in.HasState, in.Epoch > in.StateEpoch, in.Seq > in.StateSeq:
		return DecisionApply
	}
	return DecisionHistory
}

// Late reports whether a change was delivered after its state had ended:
// observed before received minus twice the interval.
func Late(observedAt, receivedAt time.Time, interval time.Duration) bool {
	return interval > 0 && observedAt.Before(receivedAt.Add(-stateLateInterval*interval))
}

// Notify reports whether a change notifies: not when it is late and a
// younger change of the probe follows (E-180).
func Notify(late, followed bool) bool { return !late || !followed }

// follower is a younger change of the same probe in the same fetch.
type follower struct {
	epoch, seq int64
	collector  string
}

// followedBy reports whether one of the younger changes would apply under
// the assignment: same epoch as the assignment and from its collector (a
// newer epoch is refused, an older one or another collector is stale).
func followedBy(followers []follower, in *ApplyInput) bool {
	if !in.Assigned {
		return false
	}
	for _, f := range followers {
		if f.epoch == in.AssignedEpoch && f.collector == in.AssignedCollector {
			return true
		}
	}
	return false
}

// stateDelivery is a fetched message (jetstream.Msg).
type stateDelivery interface {
	Subject() string
	Data() []byte
	Ack() error
	NakWithDelay(delay time.Duration) error
	Term() error
	Metadata() (*jetstream.MsgMetadata, error)
}

// stateMessage is a decoded state change with its delivery.
type stateMessage struct {
	org, collector string
	change         probewire.StateChange
	receivedAt     time.Time
	followers      []follower
	d              stateDelivery
}

// StateApplier is the consumer of the state changes.
type StateApplier struct {
	js     jetstream.JetStream
	stream StreamConfig
	pool   *pgxpool.Pool
	now    func() time.Time
	// write applies one change (apply; replaced in tests).
	write func(ctx context.Context, m *stateMessage) error

	mu       sync.Mutex
	consumer jetstream.Consumer
}

// NewStateApplier returns the consumer on the stream cfg of js writing to
// pool.
func NewStateApplier(js jetstream.JetStream, cfg StreamConfig, pool *pgxpool.Pool) *StateApplier {
	a := &StateApplier{js: js, stream: cfg, pool: pool, now: time.Now}
	a.write = a.apply
	return a
}

// ConsumerConfig is the configuration of the durable consumer.
func (a *StateApplier) ConsumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       StateConsumer,
		FilterSubject: a.stream.SubjectPrefix + ".state.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       time.Minute,
		MaxDeliver:    StateMaxDeliver,
		MaxAckPending: 4 * StateBatch,
	}
}

func (a *StateApplier) setup(ctx context.Context) (jetstream.Consumer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.consumer != nil {
		return a.consumer, nil
	}
	c, err := a.js.CreateOrUpdateConsumer(ctx, a.stream.Name, a.ConsumerConfig())
	if err != nil {
		return nil, fmt.Errorf("probe state consumer: %w", err)
	}
	a.consumer = c
	return c, nil
}

func (a *StateApplier) reset() {
	a.mu.Lock()
	a.consumer = nil
	a.mu.Unlock()
}

// Run fetches and applies until ctx ends.
func (a *StateApplier) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := a.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("probe state fetch failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// RunOnce fetches one batch and applies it.
func (a *StateApplier) RunOnce(ctx context.Context) error {
	c, err := a.setup(ctx)
	if err != nil {
		return err
	}
	batch, err := c.Fetch(StateBatch, jetstream.FetchMaxWait(StateWait))
	if err != nil {
		a.reset()
		return err
	}
	var msgs []stateDelivery
	for m := range batch.Messages() {
		msgs = append(msgs, m)
	}
	if ferr := batch.Error(); ferr != nil && !errors.Is(ferr, nats.ErrTimeout) && !errors.Is(ferr, context.DeadlineExceeded) &&
		len(msgs) == 0 {
		a.reset()
		return ferr
	}
	a.process(ctx, msgs)
	return nil
}

// Backlog reads the messages of the consumer not yet acknowledged.
func (a *StateApplier) Backlog(ctx context.Context) (int, error) {
	c, err := a.setup(ctx)
	if err != nil {
		return 0, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		a.reset()
		return 0, err
	}
	n := info.NumPending + uint64(info.NumAckPending) //nolint:gosec // a count
	return int(min(n, uint64(1<<31-1))), nil          //nolint:gosec // bounded above
}

// decode reads the fetched messages; undecodable ones are given up.
func (a *StateApplier) decode(msgs []stateDelivery) []*stateMessage {
	prefix := a.stream.SubjectPrefix + ".state."
	out := make([]*stateMessage, 0, len(msgs))
	for _, d := range msgs {
		rest, ok := strings.CutPrefix(d.Subject(), prefix)
		org, collector, found := strings.Cut(rest, ".")
		var change probewire.StateChange
		if !ok || !found || !api.IsUUID(org) || !api.IsUUID(collector) || probewire.Decode(d.Data(), &change) != nil ||
			!api.IsUUID(change.ProbeID) {
			_ = d.Term()
			stateDeadLetters.WithLabelValues(DeadLetterDecode).Inc()
			continue
		}
		received := a.now()
		if meta, err := d.Metadata(); err == nil && !meta.Timestamp.IsZero() {
			received = meta.Timestamp
		}
		out = append(out, &stateMessage{org: org, collector: strings.ToLower(collector), change: change, receivedAt: received, d: d})
	}
	markFollowed(out)
	return out
}

// markFollowed notes for every change the younger changes of the same
// probe in the same fetch (larger epoch, or same epoch and larger seq);
// apply decides which of them really follow (followedBy).
func markFollowed(msgs []*stateMessage) {
	byProbe := map[string][]*stateMessage{}
	for _, m := range msgs {
		byProbe[m.change.ProbeID] = append(byProbe[m.change.ProbeID], m)
	}
	for _, m := range msgs {
		m.followers = nil
		seq := int64(m.change.Seq) //nolint:gosec // sequence numbers stay below 2^63
		for _, o := range byProbe[m.change.ProbeID] {
			oseq := int64(o.change.Seq) //nolint:gosec // as above
			if o.change.Epoch > m.change.Epoch || (o.change.Epoch == m.change.Epoch && oseq > seq) {
				m.followers = append(m.followers, follower{epoch: o.change.Epoch, seq: oseq, collector: o.collector})
			}
		}
	}
}

// lane returns the lane of a probe.
func lane(probeID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(probeID))
	return int(h.Sum32() % stateLanes)
}

// process applies the messages: one goroutine per lane, the messages of a
// lane in the order of delivery.
func (a *StateApplier) process(ctx context.Context, delivered []stateDelivery) {
	msgs := a.decode(delivered)
	lanes := make([][]*stateMessage, stateLanes)
	for _, m := range msgs {
		l := lane(m.change.ProbeID)
		lanes[l] = append(lanes[l], m)
	}
	var wg sync.WaitGroup
	for _, l := range lanes {
		if len(l) == 0 {
			continue
		}
		wg.Add(1)
		go func(l []*stateMessage) {
			defer wg.Done()
			// A failed change of a probe sends its later changes of this
			// fetch back unapplied: they come again after it (AK1).
			failed := map[string]bool{}
			for _, m := range l {
				if failed[m.change.ProbeID] {
					a.retry(m.d)
					continue
				}
				if err := a.write(ctx, m); err != nil {
					slog.Warn("probe state: write failed", "organization_id", m.org, "probe_id", m.change.ProbeID, "error", err)
					failed[m.change.ProbeID] = true
					a.retry(m.d)
					continue
				}
				_ = m.d.Ack()
			}
		}(l)
	}
	wg.Wait()
}

func (a *StateApplier) retry(d stateDelivery) {
	meta, err := d.Metadata()
	if err == nil && meta.NumDelivered >= StateMaxDeliver {
		_ = d.Term()
		stateDeadLetters.WithLabelValues(DeadLetterMaxDelive).Inc()
		return
	}
	_ = d.NakWithDelay(stateNakDelay)
}

// stateChangedData is the data of probe.state_changed: the frame of WP-867
// with the collector, the business time and late/notify.
type stateChangedData struct {
	events.ProbeStateChangedData
	ProbeID     string    `json:"probe_id"`
	CollectorID string    `json:"collector_id"`
	ObservedAt  time.Time `json:"observed_at"`
	Late        bool      `json:"late"`
	Notify      bool      `json:"notify"`
}

// Apply writes one state change of the collector as the consumer does,
// without a stream (tests, replays): no successor in its fetch.
func (a *StateApplier) Apply(ctx context.Context, orgID, collectorID string, c *probewire.StateChange, receivedAt time.Time) error {
	return a.apply(ctx, &stateMessage{org: orgID, collector: strings.ToLower(collectorID), change: *c, receivedAt: receivedAt})
}

// apply writes one change in a tenant transaction (see the package
// comment).
func (a *StateApplier) apply(ctx context.Context, m *stateMessage) error {
	scope := database.OrgWideScope(m.org, "")
	c := &m.change
	seq := int64(c.Seq) //nolint:gosec // sequence numbers stay below 2^63
	return database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		// The changes of one probe on every instance wait for each other,
		// also before its first state row exists.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('probe_state:' || $1, 0))`, c.ProbeID); err != nil {
			return fmt.Errorf("lock probe state: %w", err)
		}
		p, err := scanProbe(tx.QueryRow(ctx, `SELECT `+probeColumns+` FROM probe p WHERE p.organization_id = $1 AND p.id = $2`,
			m.org, c.ProbeID))
		if errors.Is(err, pgx.ErrNoRows) {
			stateDropped.WithLabelValues(StateUnknownProbe).Inc()
			return nil
		}
		if err != nil {
			return fmt.Errorf("read probe: %w", err)
		}
		in := ApplyInput{Epoch: c.Epoch, Seq: seq, Collector: m.collector}
		err = tx.QueryRow(ctx, `SELECT epoch, collector_id::text FROM probe_assignment WHERE organization_id = $1 AND probe_id = $2`,
			m.org, c.ProbeID).Scan(&in.AssignedEpoch, &in.AssignedCollector)
		switch {
		case err == nil:
			in.Assigned = true
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read assignment: %w", err)
		}
		err = tx.QueryRow(ctx, `SELECT epoch, last_seq FROM probe_state WHERE organization_id = $1 AND probe_id = $2 FOR UPDATE`,
			m.org, c.ProbeID).Scan(&in.StateEpoch, &in.StateSeq)
		switch {
		case err == nil:
			in.HasState = true
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read probe state: %w", err)
		}
		decision := Decide(&in)
		if decision == DecisionRefuse {
			stateDropped.WithLabelValues(StateFutureEpoch).Inc()
			return nil
		}
		// A collector degraded for its clock: observed_at corrected by
		// −offset (PRB-09, WP-962); the order stays (epoch, seq).
		var offset *int32
		err = tx.QueryRow(ctx, `SELECT `+clockCorrectionSQL+` FROM collector_group_member m
			WHERE m.organization_id = $1 AND m.collector_id = $2`, m.org, m.collector).Scan(&offset)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read clock offset: %w", err)
		}
		observedAt := CorrectObservedAt(c.ObservedAt, offset)
		layers, err := (&pgWriteTx{tx: tx, orgID: m.org}).Layers(ctx, &p)
		if err != nil {
			return err
		}
		interval := time.Duration(Resolve(&p, layers).IntervalS.Int()) * time.Second
		late := Late(observedAt, m.receivedAt, interval)
		notify := decision != DecisionStale && Notify(late, followedBy(m.followers, &in))
		tag, err := tx.Exec(ctx, `INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, reason,
				observed_at, received_at, epoch, seq, collector_id, late, notify, stale, clock_offset_ms)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT ON CONSTRAINT probe_state_change_idempotency_key DO NOTHING`,
			m.org, c.ProbeID, string(c.From), string(c.To), c.Reason, observedAt, m.receivedAt, c.Epoch, seq, m.collector,
			late, notify, decision == DecisionStale, offset)
		if err != nil {
			return fmt.Errorf("write state change: %w", err)
		}
		if tag.RowsAffected() == 0 {
			stateDropped.WithLabelValues(StateDuplicate).Inc()
			return nil
		}
		if _, err = tx.Exec(ctx, `INSERT INTO probe_ingest_cursor AS c (organization_id, collector_id, stream, epoch, last_seq)
				VALUES ($1, $2, 'state', $3, $4)
			ON CONFLICT (collector_id, stream) DO UPDATE
			SET epoch = GREATEST(c.epoch, EXCLUDED.epoch),
			    last_seq = CASE WHEN EXCLUDED.epoch > c.epoch THEN EXCLUDED.last_seq ELSE GREATEST(c.last_seq, EXCLUDED.last_seq) END`,
			m.org, m.collector, c.Epoch, seq); err != nil {
			return fmt.Errorf("write ingest cursor: %w", err)
		}
		if decision != DecisionApply {
			return nil
		}
		// Never back: the row moves only to a larger (epoch, seq).
		tag, err = tx.Exec(ctx, `INSERT INTO probe_state AS s (probe_id, organization_id, state, reason, since, last_seq, epoch, collector_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (probe_id) DO UPDATE SET state = EXCLUDED.state, reason = EXCLUDED.reason, since = EXCLUDED.since,
				last_seq = EXCLUDED.last_seq, epoch = EXCLUDED.epoch, collector_id = EXCLUDED.collector_id
			WHERE EXCLUDED.epoch > s.epoch OR (EXCLUDED.epoch = s.epoch AND EXCLUDED.last_seq > s.last_seq)`,
			c.ProbeID, m.org, string(c.To), c.Reason, observedAt, seq, c.Epoch, m.collector)
		if err != nil {
			return fmt.Errorf("write probe state: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		data := stateChangedData{
			ProbeStateChangedData: events.ProbeStateChangedData{ID: p.ID, CIID: p.CIID, ClientID: p.ClientID, SiteID: p.SiteID,
				From: string(c.From), To: string(c.To), Reason: c.Reason},
			ProbeID: p.ID, CollectorID: m.collector, ObservedAt: observedAt.UTC(), Late: late, Notify: notify,
		}
		return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
			_, aerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: m.org, Type: events.ProbeStateChanged, EntityID: p.ID, Data: data})
			return aerr
		})
	})
}
