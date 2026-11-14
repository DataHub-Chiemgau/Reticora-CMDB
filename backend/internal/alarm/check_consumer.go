package alarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Alarms from the probe states (ALR-02, MON-04, WP-986, E-177). The
// durable consumer reticora-alarm-check of the stream RETICORA_EVENTS
// (filter events.*.probe.state_changed, explicit ack) runs in the worker
// alarm_check. It spreads the fetched events over lanes by probe id, so the
// events of one probe are handled one after the other. Every event is
// reconciled with the current probe_state in one tenant transaction of the
// event's organization (taken from the envelope, never from the payload):
//
//   - down → Raise with severity critical, degraded → Raise with severity
//     warning (a later down raises it to critical); a down or degraded
//     opens an alarm only when the event notifies (notify true), an open
//     alarm counts the occurrence in any case;
//   - up → Clear;
//   - unknown → nothing: no new alarm, an open one stays open (E-177: a
//     failing check is no outage; a network down reports through the root
//     event collector_group.unreachable, WP-964).
//
// The dedup key is check:<probe_id>:state (source_kind check, source_id the
// probe). opened_at and resolved_at are probe_state.since, the collector's
// time. A late change (late true) changes the alarm only when its target
// state is the current state. Because the consumer applies the current
// state instead of the event's content, a repeated or reordered delivery of
// the same changes gives the same alarm state and transitions.

// Names and limits of the consumer.
const (
	CheckConsumerName = "reticora-alarm-check"
	CheckSeries       = "state"
	checkBatch        = 2000
	checkWait         = 500 * time.Millisecond
	checkMaxDeliver   = 5
	checkAckWait      = time.Minute
	checkLanes        = 16
	checkChunk        = 100
	checkNakDelay     = 2 * time.Second
	// ReasonCheckUp is the reason of the transition that resolves a check
	// alarm.
	ReasonCheckUp = "check_up"
)

var (
	checkLag = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "reticora", Name: "alarm_check_lag_seconds",
		Help:    "Time from the receipt of a probe state change to the commit of its alarm (ALR-02, p95 at most 10 s).",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 60},
	})
	checkDead = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "alarm_check_dead_letters_total",
		Help: "Probe state events the alarm check consumer gave up, by reason (undecodable, max_deliver).",
	}, []string{"reason"})
)

// RegisterCheckMetrics registers the metrics of the check consumer.
func RegisterCheckMetrics(reg prometheus.Registerer) { reg.MustRegister(checkLag, checkDead) }

// CheckDeadLetters returns the counter of given up events of reason (tests).
func CheckDeadLetters(reason string) prometheus.Counter { return checkDead.WithLabelValues(reason) }

// CheckReader reads the current state of a probe in the context's
// transaction; nil without such a probe.
type CheckReader interface {
	ReadCheck(ctx context.Context, orgID, probeID string) (*probe.CheckView, error)
}

// PGCheckReader is the CheckReader on PostgreSQL: it joins the context's
// transaction.
type PGCheckReader struct{ Pool *pgxpool.Pool }

// ReadCheck implements CheckReader. It first takes the transaction lock
// alarm_check:<probe_id>: the reconciliations of one probe on every
// instance (a redelivery to another instance, the replicas of the worker)
// run one after the other, and the state is read after the lock, so a
// reconciliation sees the committed raise or clear of the one before it and
// the probe's state that is current then.
func (r PGCheckReader) ReadCheck(ctx context.Context, orgID, probeID string) (*probe.CheckView, error) {
	var out *probe.CheckView
	err := database.WithRequestTenant(ctx, r.Pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('alarm_check:' || $1, 0))`, probeID); err != nil {
			return fmt.Errorf("lock probe alarm: %w", err)
		}
		var err error
		out, err = probe.ReadCheck(ctx, tx, orgID, probeID)
		return err
	})
	return out, err
}

// checkEvent is what the consumer reads of probe.state_changed.
type checkEvent struct {
	ID      string `json:"id"`
	ProbeID string `json:"probe_id"`
	To      string `json:"to"`
	Late    bool   `json:"late"`
	Notify  *bool  `json:"notify"`
}

// CheckConsumer turns probe state changes into alarms.
type CheckConsumer struct {
	svc    *Service
	reader CheckReader
	js     jetstream.JetStream
	now    func() time.Time
	// Stream, Durable and Filter name the consumer (tests use a stream of
	// their own).
	Stream, Durable, Filter string
	// lag observes the delay of every committed event (tests); nil only
	// the histogram.
	lag func(time.Duration)
}

// NewCheckConsumer returns the consumer on js writing through svc.
func NewCheckConsumer(svc *Service, reader CheckReader, js jetstream.JetStream) *CheckConsumer {
	return &CheckConsumer{svc: svc, reader: reader, js: js, now: time.Now, Stream: events.StreamName, Durable: CheckConsumerName,
		Filter: events.SubjectPrefix + "*." + events.ProbeStateChanged}
}

// WithLagObserver calls f with the delay of every committed event (tests).
func (c *CheckConsumer) WithLagObserver(f func(time.Duration)) *CheckConsumer {
	c.lag = f
	return c
}

// ConsumerConfig is the configuration of the durable consumer (AK1).
func (c *CheckConsumer) ConsumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       c.Durable,
		FilterSubject: c.Filter,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       checkAckWait,
		MaxDeliver:    checkMaxDeliver,
		MaxAckPending: 4 * checkBatch,
	}
}

// Run fetches and handles the events until ctx ends; a failed setup is
// retried every five seconds (the stream may not exist yet).
func (c *CheckConsumer) Run(ctx context.Context) {
	var consumer jetstream.Consumer
	for ctx.Err() == nil {
		var err error
		if consumer == nil {
			consumer, err = c.setup(ctx)
		}
		if err == nil {
			err = c.fetch(ctx, consumer)
		}
		if err == nil || ctx.Err() != nil {
			continue
		}
		consumer = nil
		slog.Warn("alarm check consumer", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *CheckConsumer) setup(ctx context.Context) (jetstream.Consumer, error) {
	if c.js == nil {
		return nil, errors.New("no JetStream connection")
	}
	consumer, err := c.js.CreateOrUpdateConsumer(ctx, c.Stream, c.ConsumerConfig())
	if err != nil {
		return nil, fmt.Errorf("ensure consumer %s: %w", c.Durable, err)
	}
	return consumer, nil
}

// RunOnce fetches one batch and handles it (tests).
func (c *CheckConsumer) RunOnce(ctx context.Context) error {
	consumer, err := c.setup(ctx)
	if err != nil {
		return err
	}
	return c.fetch(ctx, consumer)
}

// Backlog reads the events of the consumer not yet acknowledged.
func (c *CheckConsumer) Backlog(ctx context.Context) (int, error) {
	consumer, err := c.setup(ctx)
	if err != nil {
		return 0, err
	}
	info, err := consumer.Info(ctx)
	if err != nil {
		return 0, err
	}
	n := info.NumPending + uint64(info.NumAckPending) //nolint:gosec // a count
	return int(min(n, uint64(1<<31-1))), nil          //nolint:gosec // bounded above
}

func (c *CheckConsumer) fetch(ctx context.Context, consumer jetstream.Consumer) error {
	batch, err := consumer.Fetch(checkBatch, jetstream.FetchMaxWait(checkWait))
	if err != nil {
		return err
	}
	var msgs []jetstream.Msg
	for m := range batch.Messages() {
		msgs = append(msgs, m)
	}
	if ferr := batch.Error(); ferr != nil && !errors.Is(ferr, nats.ErrTimeout) && !errors.Is(ferr, context.DeadlineExceeded) &&
		len(msgs) == 0 {
		return ferr
	}
	c.process(ctx, msgs)
	return nil
}

// checkMessage is a decoded event with its delivery.
type checkMessage struct {
	env   events.Envelope
	event checkEvent
	msg   jetstream.Msg
}

// process handles the messages: one goroutine per lane, the messages of a
// lane in the order of delivery. The consecutive events of one
// organization in a lane share one transaction (up to checkChunk); when it
// fails they are handled one by one, and a failed event of a probe sends
// its later events of the fetch back unhandled.
func (c *CheckConsumer) process(ctx context.Context, msgs []jetstream.Msg) {
	lanes := make([][]*checkMessage, checkLanes)
	for _, m := range msgs {
		cm := &checkMessage{msg: m}
		if err := json.Unmarshal(m.Data(), &cm.env); err != nil || cm.env.OrgID == "" || cm.env.Type != events.ProbeStateChanged ||
			json.Unmarshal(cm.env.Data, &cm.event) != nil {
			_ = m.Term()
			checkDead.WithLabelValues("undecodable").Inc()
			continue
		}
		l := checkLane(cm.probeID())
		lanes[l] = append(lanes[l], cm)
	}
	var wg sync.WaitGroup
	for _, l := range lanes {
		if len(l) == 0 {
			continue
		}
		wg.Add(1)
		go func(l []*checkMessage) {
			defer wg.Done()
			for start := 0; start < len(l); {
				end := start + 1
				for end < len(l) && end-start < checkChunk && l[end].env.OrgID == l[start].env.OrgID {
					end++
				}
				c.handleChunk(ctx, l[start:end])
				start = end
			}
		}(l)
	}
	wg.Wait()
}

// handleChunk handles events of one organization in one transaction and
// acknowledges them after the commit; on a failure each is handled alone.
func (c *CheckConsumer) handleChunk(ctx context.Context, chunk []*checkMessage) {
	if len(chunk) > 1 {
		outcomes := make([]checkOutcome, len(chunk))
		err := c.inOrgTx(ctx, chunk[0].env.OrgID, func(ctx context.Context) error {
			for i, m := range chunk {
				o, err := c.handleIn(ctx, &m.env, &m.event)
				if err != nil {
					return err
				}
				outcomes[i] = o
			}
			return nil
		})
		if err == nil {
			for i, m := range chunk {
				c.observe(outcomes[i])
				_ = m.msg.Ack()
			}
			return
		}
		slog.Warn("alarm check: chunk failed, handling its events one by one", "organization_id", chunk[0].env.OrgID, "error", err)
	}
	failed := map[string]bool{}
	for _, m := range chunk {
		id := m.probeID()
		if failed[id] {
			c.retry(m.msg)
			continue
		}
		if err := c.Handle(ctx, &m.env); err != nil {
			slog.Warn("alarm check: event not handled", "organization_id", m.env.OrgID, "probe_id", id, "error", err)
			failed[id] = true
			c.retry(m.msg)
			continue
		}
		_ = m.msg.Ack()
	}
}

func (m *checkMessage) probeID() string {
	return m.event.probeID(m.env.EntityID)
}

func (e *checkEvent) probeID(entity string) string {
	if e.ProbeID != "" {
		return e.ProbeID
	}
	if e.ID != "" {
		return e.ID
	}
	return entity
}

func (c *CheckConsumer) retry(m jetstream.Msg) {
	if meta, err := m.Metadata(); err == nil && meta.NumDelivered >= checkMaxDeliver {
		_ = m.Term()
		checkDead.WithLabelValues("max_deliver").Inc()
		return
	}
	_ = m.NakWithDelay(checkNakDelay)
}

func checkLane(id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % checkLanes)
}

// checkOutcome is what the handling of one event committed: the receipt
// of the change whose state it applied.
type checkOutcome struct {
	committed bool
	received  time.Time
}

// inOrgTx runs fn in one transaction of the organization in the org-wide
// scope (the organization of the envelope, never of the payload).
func (c *CheckConsumer) inOrgTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	return c.svc.InTx(ctx, orgID, fn)
}

// observe records the lag of a committed event.
func (c *CheckConsumer) observe(o checkOutcome) {
	if !o.committed || o.received.IsZero() {
		return
	}
	d := max(c.now().Sub(o.received), 0)
	checkLag.Observe(d.Seconds())
	if c.lag != nil {
		c.lag(d)
	}
}

// Handle reconciles the alarm of the probe of one probe.state_changed event
// with the probe's current state (see the package comment) in a
// transaction of its own; an error asks for a repeated delivery.
func (c *CheckConsumer) Handle(ctx context.Context, env *events.Envelope) error {
	var ev checkEvent
	if err := json.Unmarshal(env.Data, &ev); err != nil {
		return nil //nolint:nilerr // an undecodable payload is given up, not repeated
	}
	var o checkOutcome
	err := c.inOrgTx(ctx, env.OrgID, func(ctx context.Context) error {
		var err error
		o, err = c.handleIn(ctx, env, &ev)
		return err
	})
	if err != nil {
		return err
	}
	c.observe(o)
	return nil
}

// handleIn reconciles one event in the context's transaction.
func (c *CheckConsumer) handleIn(ctx context.Context, env *events.Envelope, ev *checkEvent) (checkOutcome, error) {
	probeID := ev.probeID(env.EntityID)
	if probeID == "" {
		return checkOutcome{}, nil
	}
	notify := ev.Notify == nil || *ev.Notify
	v, err := c.reader.ReadCheck(ctx, env.OrgID, probeID)
	if err != nil || v == nil || !v.HasState {
		return checkOutcome{}, err
	}
	// A late change changes the alarm only when it is the current state
	// (AK3).
	if ev.Late && ev.To != v.State {
		return checkOutcome{}, nil
	}
	if err := c.reconcile(ctx, env.OrgID, v, notify); err != nil {
		return checkOutcome{}, err
	}
	return checkOutcome{committed: true, received: v.ReceivedAt}, nil
}

// checkSeverity maps a probe state to the severity of its alarm; "" for a
// state that raises none.
func checkSeverity(state string) string {
	switch probewire.State(state) {
	case probewire.StateDown:
		return SeverityCritical
	case probewire.StateDegraded:
		return SeverityWarning
	}
	return ""
}

// CheckDedupKey is the dedup_key of the alarm of a probe,
// check:<probe_id>:state (DedupKey of source_kind check, the probe and the
// series state, WP-982/WP-986); the correlation and every later consumer
// take it from here instead of composing it again.
func CheckDedupKey(probeID string) string { return DedupKey(SourceCheck, probeID, CheckSeries) }

// reconcile applies the current state v of a probe to its alarm.
func (c *CheckConsumer) reconcile(ctx context.Context, orgID string, v *probe.CheckView, notify bool) error {
	key := CheckDedupKey(v.ProbeID)
	switch probewire.State(v.State) {
	case probewire.StateUp:
		_, err := c.svc.Clear(ctx, orgID, key, v.Since, ReasonCheckUp)
		return err
	case probewire.StateDown, probewire.StateDegraded:
		if !notify {
			// An ended outage (notify false) opens nothing; an open
			// alarm counts the occurrence.
			if _, err := c.svc.Store().LockOpenByKey(ctx, orgID, key); errors.Is(err, ErrNotFound) {
				return nil
			} else if err != nil {
				return err
			}
		}
		_, err := c.svc.Raise(ctx, orgID, checkRaise(v))
		return err
	}
	return nil
}

// checkRaise is the raise of a probe in state down or degraded (AK5).
func checkRaise(v *probe.CheckView) *RaiseInput {
	in := &RaiseInput{SourceKind: SourceCheck, SourceID: v.ProbeID, SeriesKey: CheckSeries,
		Labels: map[string]string{"probe_id": v.ProbeID, "probe_type": v.Type}, Severity: checkSeverity(v.State),
		Summary: checkSummary(v), ObservedAt: v.Since,
		Details: map[string]any{"probe_id": v.ProbeID, "probe_type": v.Type, "target": v.Target, "state": v.State,
			"reason": v.Reason, "interval_s": v.IntervalS, "timeout_s": v.TimeoutS, "down_count": v.DownCount,
			"up_count": v.UpCount}}
	if len(v.Thresholds) > 0 {
		in.Details["thresholds"] = v.Thresholds
	}
	if v.CIID != "" {
		ci := v.CIID
		in.CIID = &ci
	} else if v.ClientID != "" {
		client := v.ClientID
		in.ClientID = &client
	}
	return in
}

func checkSummary(v *probe.CheckView) string {
	s := fmt.Sprintf("%s %s %s", v.Type, v.Target, v.State)
	if v.Reason != "" {
		s += " (" + v.Reason + ")"
	}
	if r := []rune(s); len(r) > SummaryMaxLen {
		s = string(r[:SummaryMaxLen])
	}
	return s
}
