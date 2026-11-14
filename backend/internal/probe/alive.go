package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Clock skew of a collector (PRB-09, PRB-10; WP-962, E-175). Every
// monitoring alive message (.probe.alive, every 10 s) yields
// offset = collector_time − received_at; its error is the one-way delay of
// the message (typically below 100 ms), negligible against the thresholds.
// The median of the last ClockSkewWindow offsets of a collector is its clock
// offset: collector_group_member.clock_offset_ms and the gauge
// reticora_collector_clock_offset_seconds. The window lies in the memory of
// the instance; after a restart or on another instance the smoothing begins
// anew. The rest of the evaluation (open warning, degraded, the counters of
// the hysteresis) lies in the member row (clock_skew_warned,
// clock_skew_calm, clock_skew_settle, member_status) and is read under its
// row lock with every alive message: the instances of the queue group share
// it, so every event is emitted exactly once whichever instance receives
// the alive message, and a restart loses no open warning.
//
//   - |median| > 2 s: a warning (log at most every 5 min per collector) and,
//     on crossing, exactly one event collector.clock_skew (degraded false);
//   - |median| > 60 s: the member becomes degraded (it stays assignable)
//     and, on crossing, exactly one more event (degraded true). While
//     degraded the ingest and the state worker correct observed_at of the
//     collector's results and changes by −offset (clock_offset_ms of the
//     row);
//   - a median below 30 s for ClockSkewWindow offsets in a row makes the
//     member healthy again; below 2 s for ClockSkewWindow offsets in a row
//     ends the warning with an event resolved true.
//
// Every alive message also writes last_alive_at (the liveness of the group
// members, WP-963, builds on it).
const (
	// ClockSkewWindow is the number of offsets the median is taken over.
	ClockSkewWindow = 6
	// ClockSkewWarnMS is the offset from which a collector is warned about.
	ClockSkewWarnMS = 2_000
	// ClockSkewDegradeMS is the offset from which a member is degraded and
	// its timestamps are corrected.
	ClockSkewDegradeMS = 60_000
	// ClockSkewRecoverMS is the offset below which a degraded member
	// becomes healthy again (hysteresis).
	ClockSkewRecoverMS = 30_000
	// clockSkewLogEvery throttles the warning in the log per collector.
	clockSkewLogEvery = 5 * time.Minute
	// clockSkewTimeout bounds the database work of one alive message.
	clockSkewTimeout = 5 * time.Second
)

var clockOffset = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "reticora", Name: "collector_clock_offset_seconds",
	Help: "Clock offset of a collector (collector − server, median of the last six monitoring alive messages) in seconds (PRB-09).",
}, []string{"collector_id"})

// clockCorrectionSQL selects the clock offset a collector's timestamps are
// corrected by from its group member m: the stored offset while the member
// is degraded for its clock, else NULL (AK4). Only the clock evaluation sets
// a member degraded.
const clockCorrectionSQL = `CASE WHEN m.member_status = 'degraded' THEN m.clock_offset_ms END`

// ClockCorrectionHorizon is how long after its last alive message as a
// member degraded for its clock the results of a collector are checked for
// a repeated delivery stored under another correction (WP-962): a repeated
// delivery (JetStream redelivery, IngestMaxDeliver × AckWait, or a repeated
// upload of the collector's spool) meets an offset that changed in between.
const ClockCorrectionHorizon = 24 * time.Hour

// clockCorrectionWindow bounds the offset by which a stored result can have
// been corrected (the time range the repetition check reads). A collector
// clock off by more than a day is outside the correction's purpose.
const clockCorrectionWindow = 24 * time.Hour

// CorrectObservedAt corrects a timestamp of a collector by its clock offset
// (collector − server, ms): at − offset; nil leaves it unchanged.
func CorrectObservedAt(at time.Time, offsetMS *int32) time.Time {
	if offsetMS == nil {
		return at
	}
	return at.Add(-time.Duration(*offsetMS) * time.Millisecond)
}

// skewState is the state of the clock evaluation of one collector. offsets
// and lastLog are the instance's; warned, degraded, below30 and below2 come
// from the member row with every alive message (Observe).
type skewState struct {
	offsets  []int64 // the last ClockSkewWindow offsets, oldest first
	warned   bool    // a warning event is open (not resolved yet)
	degraded bool    // the member is degraded for its clock
	below30  int     // medians below ClockSkewRecoverMS in a row while degraded
	below2   int     // medians below ClockSkewWarnMS in a row while warned
	lastLog  time.Time
}

// skewStep is the outcome of one offset.
type skewStep struct {
	median  int64
	warn    bool // emit the warning event (degraded false)
	degrade bool // the member becomes degraded: emit degraded true
	recover bool // the member becomes healthy again
	resolve bool // the warning ends: emit resolved true
	log     bool // log the warning now
	next    skewState
}

// median returns the median of the offsets (the mean of the two middle
// values for an even count, rounded towards zero).
func median(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	s := slices.Clone(values)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// step evaluates the offset of one alive message at now. It does not
// change st: the caller adopts step.next once the step is stored.
func (st *skewState) step(offsetMS int64, now time.Time) skewStep {
	next := *st
	next.offsets = append(slices.Clone(st.offsets), offsetMS)
	if len(next.offsets) > ClockSkewWindow {
		next.offsets = next.offsets[len(next.offsets)-ClockSkewWindow:]
	}
	m := median(next.offsets)
	out := skewStep{median: m}
	a := abs64(m)
	if a > ClockSkewWarnMS {
		if !next.warned {
			next.warned, out.warn = true, true
		}
		if now.Sub(next.lastLog) >= clockSkewLogEvery {
			next.lastLog, out.log = now, true
		}
	}
	if a > ClockSkewDegradeMS && !next.degraded {
		next.degraded, out.degrade = true, true
	}
	switch {
	case next.degraded && !out.degrade && a < ClockSkewRecoverMS:
		next.below30++
		if next.below30 >= ClockSkewWindow {
			next.degraded, next.below30, out.recover = false, 0, true
		}
	case next.degraded:
		next.below30 = 0
	}
	switch {
	case next.warned && !out.warn && !next.degraded && a < ClockSkewWarnMS:
		// The warning ends only once the member is healthy again: a
		// degraded member always has an open warning.
		next.below2++
		if next.below2 >= ClockSkewWindow {
			next.warned, next.below2, out.resolve = false, 0, true
		}
	case next.warned:
		next.below2 = 0
	}
	out.next = next
	return out
}

// ClockSkew evaluates the monitoring alive messages of the collectors:
// last_alive_at, clock offset, member status and the event
// collector.clock_skew (see the constants).
type ClockSkew struct {
	pool *pgxpool.Pool
	now  func() time.Time
	// returned learns after the commit that a down member became
	// assignable again (WP-963): its group is assigned at once.
	returned func(ctx context.Context, orgID, collectorID string)

	mu    sync.Mutex
	state map[string]*skewEntry // collector → state
}

type skewEntry struct {
	mu sync.Mutex
	st skewState
}

// NewClockSkew returns the evaluation on pool (the application pool).
func NewClockSkew(pool *pgxpool.Pool) *ClockSkew {
	return &ClockSkew{pool: pool, now: time.Now, state: map[string]*skewEntry{}}
}

// WithClock replaces the clock of the log throttling (tests).
func (c *ClockSkew) WithClock(now func() time.Time) *ClockSkew {
	c.now = now
	return c
}

// OnMemberReturned calls f after the commit of the alive message that made
// a down member assignable again (WP-963 AK2: a status change assigns the
// group at once).
func (c *ClockSkew) OnMemberReturned(f func(ctx context.Context, orgID, collectorID string)) *ClockSkew {
	c.returned = f
	return c
}

// Hook is the alive hook of the probe edge (Edge.OnAlive). An error is
// logged; the alive message is answered all the same.
func (c *ClockSkew) Hook() AliveHook {
	return func(orgID, collectorID string, alive probewire.Alive, receivedAt time.Time) { //nolint:gocritic // signature of AliveHook
		ctx, cancel := context.WithTimeout(context.Background(), clockSkewTimeout)
		defer cancel()
		if err := c.Observe(ctx, orgID, collectorID, &alive, receivedAt); err != nil {
			slog.Error("probe alive: clock offset not stored", "organization_id", orgID, "collector_id", collectorID, "error", err)
		}
	}
}

func (c *ClockSkew) entry(collectorID string) *skewEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.state[collectorID]
	if !ok {
		e = &skewEntry{}
		c.state[collectorID] = e
	}
	return e
}

// member is the group membership of a collector as the evaluation reads it.
type member struct {
	group, client, site string
	status              string
}

// Observe evaluates one alive message of the collector received at
// receivedAt (AK1–AK3). A collector without group membership only updates
// the gauge. The events go through the outbox as the last write of the
// transaction; a failed transaction leaves the evaluation unchanged.
func (c *ClockSkew) Observe(ctx context.Context, orgID, collectorID string, alive *probewire.Alive, receivedAt time.Time) error {
	if alive.CollectorTime.IsZero() {
		return errors.New("probe alive: no collector time")
	}
	offset := alive.CollectorTime.Sub(receivedAt).Milliseconds()
	e := c.entry(collectorID)
	e.mu.Lock()
	defer e.mu.Unlock()
	var step skewStep
	var m member
	returned := false
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, c.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var client, site *string
		var calm, settle int16
		var prevAlive *time.Time
		var timeoutS float64
		returned = false
		st := e.st
		err := tx.QueryRow(ctx, `SELECT m.group_id::text, m.client_id::text, m.site_id::text, m.member_status,
				m.clock_skew_warned, m.clock_skew_calm, m.clock_skew_settle, m.last_alive_at, extract(epoch FROM g.liveness_timeout)::float8
			  FROM collector_group_member m
			  JOIN collector_group g ON g.organization_id = m.organization_id AND g.id = m.group_id
			 WHERE m.organization_id = $1 AND m.collector_id = $2 FOR UPDATE OF m`, orgID, collectorID).
			Scan(&m.group, &client, &site, &m.status, &st.warned, &calm, &settle, &prevAlive, &timeoutS)
		if errors.Is(err, pgx.ErrNoRows) {
			// No member: only the gauge, no event (nothing to degrade).
			st.warned, st.degraded, st.below2, st.below30 = false, false, 0, 0
			step = st.step(offset, c.now())
			return nil
		}
		if err != nil {
			return fmt.Errorf("read group member: %w", err)
		}
		if client != nil {
			m.client = *client
		}
		if site != nil {
			m.site = *site
		}
		// The shared state of the row; only the window is the instance's.
		st.degraded = m.status == MemberDegraded
		st.below2, st.below30 = int(calm), int(settle)
		step = st.step(offset, c.now())
		status := m.status
		// A down member returns with the second alive message within one
		// timeout of its group (WP-963 AK1): healthy, or degraded below
		// when its clock still deviates by more than 60 s.
		if status == MemberDown && MemberReturns(prevAlive, receivedAt, time.Duration(timeoutS*float64(time.Second))) {
			status, returned = MemberHealthy, true
		}
		// The first alive message of a member makes it assignable
		// (groupCandidates): its group is assigned at once as well.
		if prevAlive == nil && status != MemberDown {
			returned = true
		}
		switch {
		case step.degrade && status == MemberHealthy:
			status = MemberDegraded
		case step.degrade:
			// A member down (liveness) is not degraded for its clock.
			step.degrade, step.next.degraded = false, false
		case step.recover && status == MemberDegraded:
			status = MemberHealthy
		}
		if _, err := tx.Exec(ctx, `UPDATE collector_group_member
			   SET last_alive_at = GREATEST(COALESCE(last_alive_at, $3), $3), clock_offset_ms = $4, member_status = $5,
			       clock_skew_warned = $6, clock_skew_calm = $7, clock_skew_settle = $8,
			       clock_corrected_at = CASE WHEN $5 = 'degraded' THEN GREATEST(COALESCE(clock_corrected_at, $3), $3)
			                                 ELSE clock_corrected_at END
			 WHERE organization_id = $1 AND collector_id = $2`,
			orgID, collectorID, receivedAt.UTC(), int32(max(min(step.median, 1<<31-1), -(1<<31))), status, //nolint:gosec // clamped to int32
			step.next.warned, int16(min(step.next.below2, ClockSkewWindow)), int16(min(step.next.below30, ClockSkewWindow))); err != nil { //nolint:gosec // at most ClockSkewWindow
			return fmt.Errorf("write group member: %w", err)
		}
		if returned && c.returned != nil {
			notify := c.returned
			database.AfterCommit(database.ContextWithTx(ctx, tx), func(ctx context.Context) { notify(ctx, orgID, collectorID) })
		}
		var emit []events.CollectorClockSkewData
		base := events.CollectorClockSkewData{ID: collectorID, CollectorID: collectorID, CollectorGroupID: m.group,
			ClientID: m.client, SiteID: m.site, SkewMS: step.median, OffsetMS: step.median}
		if step.warn {
			emit = append(emit, base)
		}
		if step.degrade {
			d := base
			d.Degraded = true
			emit = append(emit, d)
		}
		if step.resolve {
			r := base
			r.Resolved = true
			emit = append(emit, r)
		}
		if len(emit) == 0 {
			return nil
		}
		return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
			outbox := events.NewOutbox()
			for i := range emit {
				if _, err := outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CollectorClockSkew, EntityID: collectorID, Data: emit[i]}); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	e.st = step.next
	clockOffset.WithLabelValues(collectorID).Set(float64(step.median) / 1000)
	if returned {
		slog.Info("collector group member assignable (first alive message or return)", "organization_id", orgID, "collector_id", collectorID,
			"collector_group_id", m.group)
	}
	if step.log {
		slog.Warn("collector clock deviates from the server; synchronize it with NTP or chrony",
			"organization_id", orgID, "collector_id", collectorID, "offset_ms", step.median, "degraded", e.st.degraded)
	}
	return nil
}
