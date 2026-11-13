package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Uplink of the probe spool (PRB-05, PRB-07, E-173, E-180, WP-955): it
// delivers the two queues of the Outbox over the edge channel of the
// collector (request/reply on collector.<org>.<id>.probe.state and
// ….probe.results) and removes an entry only after the server answered
// with the highest sequence number it persisted (WP-959: the reply follows
// the PubAck of the stream RETICORA_PROBE).
//
//   - State changes go one by one as soon as they are spooled
//     (NotifyState), without waiting for a batch.
//   - Results go every ResultsInterval (10 s) or as soon as 1,000 are
//     spooled, in sequence order, at most MaxUplinkMessageBytes (256 KiB)
//     and probewire.MaxBatchResults per message.
//   - Every queue has at most one message in flight; results go only while
//     the state queue is empty, so after a reconnect the state changes are
//     synchronized completely before the results (E-180), and the results
//     at most SyncRate messages per second.
//   - A refusal for good (4xx) puts the entries of the message into the
//     quarantine of the spool and counts them as loss; network errors and
//     5xx are repeated with exponential backoff (1 s to 60 s). 403
//     feature-not-entitled pauses the uplink like the metric samples of
//     the collector (monitoringPaused): the data stay in the spool and are
//     no loss. Without an edge connection (ErrNoEdge) everything stays in
//     the spool; there is no HTTP fallback (F-046).

// Kinds of message the uplink sends; the collector maps them to the edge
// subject suffixes .probe.state, .probe.results and .probe.alive.
const (
	KindState   = "state"
	KindResults = "results"
	KindAlive   = "alive"
)

// Defaults of the uplink.
const (
	// MaxUplinkMessageBytes is the largest message the edge takes on the
	// probe subjects (backend probe.MaxMessageBytes).
	MaxUplinkMessageBytes = 256 << 10
	// DefaultResultsInterval is the longest a spooled result waits for
	// its message (PRB-05: at most 10 s or 1,000 results).
	DefaultResultsInterval = 10 * time.Second
	// DefaultSyncRate is the default of RETICORA_PROBE_SYNC_RATE: the most
	// result messages per second.
	DefaultSyncRate = 5
	// DefaultPauseInterval is how long the uplink sends nothing after the
	// server refused monitoring as not licensed; then one message probes
	// whether it is licensed by now.
	DefaultPauseInterval = time.Hour
	// uplinkBackoffMin and uplinkBackoffMax bound the backoff after a
	// network error or a 5xx answer.
	uplinkBackoffMin = time.Second
	uplinkBackoffMax = 60 * time.Second
	// uplinkPoll is how often an idle queue is looked at again (also the
	// wait while the edge is not connected).
	uplinkPoll = time.Second
	// uplinkPeekBundles is the most result bundles read for one message.
	uplinkPeekBundles = 100
	// uplinkPeekStates is the most state changes read in one round.
	uplinkPeekStates = 64
)

// ProblemFeatureNotEntitled is the problem type of the refusal of a
// feature the plan of the organization does not include.
const ProblemFeatureNotEntitled = "https://reticora.io/problems/feature-not-entitled"

// ErrNoEdge reports that the edge channel cannot take a message (not
// connected, no responder); the uplink keeps the data and tries again.
var ErrNoEdge = errors.New("probe: edge channel unavailable")

// ErrTooLarge reports a message above the message size of the edge.
var ErrTooLarge = errors.New("probe: message exceeds the message size of the edge")

// SendFunc sends one message of a kind (KindState, KindResults, KindAlive)
// over the edge and returns the reply.
type SendFunc func(ctx context.Context, kind string, data []byte) ([]byte, error)

// UplinkReply is the server's answer on a probe subject (backend
// probe.Reply).
type UplinkReply struct {
	Status     int        `json:"status"`
	Type       string     `json:"type,omitempty"`
	Error      string     `json:"error,omitempty"`
	Seq        *uint64    `json:"seq,omitempty"`
	Duplicate  bool       `json:"duplicate,omitempty"`
	ServerTime *time.Time `json:"server_time,omitempty"`
}

// UplinkStats are the counters of the uplink.
type UplinkStats struct {
	StatesSent     uint64
	ResultMessages uint64
	ResultsSent    uint64
	Quarantined    uint64
	Retries        uint64
	Paused         bool
}

// Uplink delivers the probe spool over the edge. The zero values of the
// tunables take the defaults.
type Uplink struct {
	Outbox *Outbox
	Send   SendFunc
	// ResultsInterval is the longest a result waits (DefaultResultsInterval).
	ResultsInterval time.Duration
	// SyncRate is the most result messages per second (DefaultSyncRate).
	SyncRate int
	// BackoffMin and BackoffMax bound the backoff (1 s, 60 s).
	BackoffMin, BackoffMax time.Duration
	// PauseInterval is the pause after a refusal as not licensed
	// (DefaultPauseInterval).
	PauseInterval time.Duration
	// Poll is how often an idle queue is looked at again (1 s).
	Poll time.Duration
	// OnNotLicensed and OnLicensed report the start and the end of a pause
	// (the collector shares them with its metric samples).
	OnNotLicensed func()
	OnLicensed    func()

	once        sync.Once
	stateKick   chan struct{}
	resultKick  chan struct{}
	pausedUntil atomic.Int64

	statesSent, resultMessages, resultsSent, quarantined, retries atomic.Uint64
}

// NewUplink returns the uplink of the spool o over send.
func NewUplink(o *Outbox, send SendFunc) *Uplink {
	return &Uplink{Outbox: o, Send: send}
}

func (u *Uplink) init() {
	u.once.Do(func() {
		u.stateKick = make(chan struct{}, 1)
		u.resultKick = make(chan struct{}, 1)
	})
}

func durationOr(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// NotifyState wakes the state queue: a state change was spooled.
func (u *Uplink) NotifyState() {
	u.init()
	select {
	case u.stateKick <- struct{}{}:
	default:
	}
}

// NotifyResults wakes the result queue: results were spooled.
func (u *Uplink) NotifyResults() {
	u.init()
	select {
	case u.resultKick <- struct{}{}:
	default:
	}
}

// Stats returns the counters of the uplink.
func (u *Uplink) Stats() UplinkStats {
	return UplinkStats{StatesSent: u.statesSent.Load(), ResultMessages: u.resultMessages.Load(),
		ResultsSent: u.resultsSent.Load(), Quarantined: u.quarantined.Load(), Retries: u.retries.Load(),
		Paused: u.paused(time.Now())}
}

// Run delivers both queues until ctx ends.
func (u *Uplink) Run(ctx context.Context) {
	u.init()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); u.loop(ctx, u.stateKick, (&stateSender{u: u, bo: u.newBackoff()}).round) }()
	go func() { defer wg.Done(); u.loop(ctx, u.resultKick, newResultsSender(u).round) }()
	wg.Wait()
}

// loop runs round until ctx ends; round returns how long to wait before
// the next round (0: at once), a kick ends the wait early.
func (u *Uplink) loop(ctx context.Context, kick <-chan struct{}, round func(context.Context) time.Duration) {
	for ctx.Err() == nil {
		wait := round(ctx)
		if wait <= 0 {
			continue
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
		case <-kick:
		case <-t.C:
		}
		t.Stop()
	}
}

// paused reports whether the uplink is paused as not licensed at now.
func (u *Uplink) paused(now time.Time) bool {
	return now.UnixNano() < u.pausedUntil.Load()
}

// pauseLeft is the rest of a pause (0 when not paused).
func (u *Uplink) pauseLeft(now time.Time) time.Duration {
	return max(time.Duration(u.pausedUntil.Load()-now.UnixNano()), 0)
}

// notLicensed starts a pause; it is logged once, not per message.
func (u *Uplink) notLicensed() {
	until := time.Now().Add(durationOr(u.PauseInterval, DefaultPauseInterval)).UnixNano()
	if u.pausedUntil.Swap(until) == 0 {
		slog.Warn("monitoring not licensed; probe data stay in the spool", "event", "collector.probe.not_licensed",
			"hint", "the plan of the organization does not include monitoring; the collector checks again every "+
				durationOr(u.PauseInterval, DefaultPauseInterval).String())
		if u.OnNotLicensed != nil {
			u.OnNotLicensed()
		}
	}
}

// licensed ends a pause after an accepted message.
func (u *Uplink) licensed() {
	if u.pausedUntil.Swap(0) != 0 {
		slog.Info("monitoring licensed; probe data are sent again", "event", "collector.probe.resumed")
	}
	if u.OnLicensed != nil {
		u.OnLicensed()
	}
}

// outcome of one message.
type sendOutcome int

const (
	sentOK sendOutcome = iota
	sentNoEdge
	sentRetry
	sentRefused
	sentNotLicensed
	sentTooLarge
)

// sendOne sends a message and classifies the answer: 2xx sent, 403
// feature-not-entitled a pause, 401, 403 (other), 408, 429 and 5xx a
// repetition (the collector is disabled or the server is busy: no loss),
// 413 too large, every other 4xx a refusal for good.
func (u *Uplink) sendOne(ctx context.Context, kind string, data []byte) (sendOutcome, UplinkReply) {
	var reply UplinkReply
	if len(data) > MaxUplinkMessageBytes {
		return sentTooLarge, reply
	}
	raw, err := u.Send(ctx, kind, data)
	switch {
	case errors.Is(err, ErrNoEdge):
		return sentNoEdge, reply
	case errors.Is(err, ErrTooLarge):
		return sentTooLarge, reply
	case err != nil:
		if ctx.Err() == nil {
			slog.Warn("probe uplink: request failed; repeated", "kind", kind, "error", err)
		}
		return sentRetry, reply
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		slog.Warn("probe uplink: invalid reply; repeated", "kind", kind, "error", err)
		return sentRetry, reply
	}
	switch s := reply.Status; {
	case s >= 200 && s < 300:
		u.licensed()
		return sentOK, reply
	case s == http.StatusForbidden && reply.Type == ProblemFeatureNotEntitled:
		u.notLicensed()
		return sentNotLicensed, reply
	case s == http.StatusRequestEntityTooLarge:
		return sentTooLarge, reply
	case s == http.StatusUnauthorized, s == http.StatusForbidden, s == http.StatusRequestTimeout,
		s == http.StatusTooManyRequests, s >= 500, s < 200:
		slog.Warn("probe uplink: message not taken; repeated", "kind", kind, "status", s, "error", reply.Error)
		return sentRetry, reply
	default:
		return sentRefused, reply
	}
}

// backoff is the exponential backoff of one queue.
type backoff struct {
	min, max, cur time.Duration
}

func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = b.min
	} else {
		b.cur = min(2*b.cur, b.max)
	}
	return b.cur
}

func (b *backoff) reset() { b.cur = 0 }

func (u *Uplink) newBackoff() *backoff {
	return &backoff{min: durationOr(u.BackoffMin, uplinkBackoffMin), max: durationOr(u.BackoffMax, uplinkBackoffMax)}
}

// seqOf reads a sequence number from the metadata of an entry.
func seqOf(m *buffer.Message, key string) (uint64, bool) {
	n, err := strconv.ParseUint(m.Metadata[key], 10, 64)
	return n, err == nil
}

// stateSender sends the state queue: the spooled state changes in
// sequence order, one per request; every entry up to the seq of the reply
// is acknowledged.
type stateSender struct {
	u  *Uplink
	bo *backoff
}

func (s *stateSender) round(ctx context.Context) time.Duration {
	u := s.u
	poll := durationOr(u.Poll, uplinkPoll)
	if left := u.pauseLeft(time.Now()); left > 0 {
		return min(left, poll)
	}
	q := u.Outbox.States()
	msgs, err := q.PeekBatch(ctx, uplinkPeekStates)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("probe uplink: state queue unreadable", "error", err)
		}
		return poll
	}
	if len(msgs) == 0 {
		u.NotifyResults() // the results may go now
		return poll
	}
	for i := 0; i < len(msgs); i++ {
		m := &msgs[i]
		seq, ok := seqOf(m, MetaSeqFrom)
		if !ok {
			// An entry without a readable seq (another format) can never be
			// acknowledged and would block the queue: quarantine.
			u.quarantine(ctx, q, KindState, UplinkReply{Error: "entry without sequence number"}, m.ID)
			continue
		}
		out, reply := u.sendOne(ctx, KindState, m.Payload)
		switch out {
		case sentOK:
			if reply.Seq != nil && *reply.Seq < seq {
				// The server persisted less than it was sent: repeat.
				u.retries.Add(1)
				return s.bo.next()
			}
			upTo := seq
			if reply.Seq != nil {
				upTo = *reply.Seq
			}
			var ids []string
			for j := i; j < len(msgs); j++ {
				if sj, ok := seqOf(&msgs[j], MetaSeqFrom); ok && sj <= upTo {
					ids = append(ids, msgs[j].ID)
				}
			}
			if err := q.Ack(ctx, ids); err != nil {
				slog.Warn("probe uplink: state changes not acknowledged in the spool", "error", err)
				return poll
			}
			s.bo.reset()
			u.statesSent.Add(1)
			i += max(len(ids)-1, 0)
		case sentRefused, sentTooLarge:
			s.bo.reset()
			u.quarantine(ctx, q, KindState, reply, m.ID)
		case sentNotLicensed:
			return min(u.pauseLeft(time.Now()), poll)
		case sentNoEdge:
			return poll
		default:
			u.retries.Add(1)
			return s.bo.next()
		}
	}
	return 0
}

// quarantine moves refused entries into the quarantine of the queue; the
// Outbox counts them as quarantined.
func (u *Uplink) quarantine(ctx context.Context, q *buffer.DiskBuffer, kind string, reply UplinkReply, ids ...string) {
	slog.Error("probe data refused by the server; moved to the quarantine", "event", "collector.probe.refused",
		"kind", kind, "status", reply.Status, "entries", len(ids), "error", reply.Error)
	for _, id := range ids {
		if err := q.Reject(ctx, id); err != nil {
			slog.Warn("probe uplink: quarantine failed", "error", err)
			continue
		}
		u.quarantined.Add(1)
	}
}

// resultsSender sends the result queue.
type resultsSender struct {
	u         *Uplink
	bo        *backoff
	lastSent  time.Time
	firstSeen time.Time
}

func newResultsSender(u *Uplink) *resultsSender {
	return &resultsSender{u: u, bo: u.newBackoff()}
}

// bundleOf is a decoded result bundle of the queue.
type bundleOf struct {
	id      string
	seqTo   uint64
	created time.Time
	results []probewire.Result
}

// round sends at most one result message: when the state queue is empty
// and the bundles hold 1,000 results or the oldest waits ResultsInterval
// (or the queue holds a backlog), at most SyncRate messages per second.
func (s *resultsSender) round(ctx context.Context) time.Duration {
	u := s.u
	poll := durationOr(u.Poll, uplinkPoll)
	now := time.Now()
	if left := u.pauseLeft(now); left > 0 {
		return min(left, poll)
	}
	if n, err := u.Outbox.States().Len(ctx); err != nil || n > 0 {
		// State changes first (PRB-07); the state queue wakes this one
		// when it is empty.
		return poll
	}
	msgs, err := u.Outbox.Results().PeekBatch(ctx, uplinkPeekBundles)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("probe uplink: result queue unreadable", "error", err)
		}
		return poll
	}
	if len(msgs) == 0 {
		s.firstSeen = time.Time{}
		return poll
	}
	if s.firstSeen.IsZero() {
		s.firstSeen = now
	}
	bundles := make([]bundleOf, 0, len(msgs))
	total := 0
	for i := range msgs {
		batch, _, derr := DecodeBundle(&msgs[i])
		if derr != nil {
			u.quarantine(ctx, u.Outbox.Results(), KindResults, UplinkReply{Error: "undecodable bundle"}, msgs[i].ID)
			return 0
		}
		to, ok := seqOf(&msgs[i], MetaSeqTo)
		if !ok && len(batch.Results) > 0 {
			to = batch.Results[len(batch.Results)-1].Seq
		}
		bundles = append(bundles, bundleOf{id: msgs[i].ID, seqTo: to, created: msgs[i].CreatedAt, results: batch.Results})
		total += len(batch.Results)
	}
	interval := durationOr(u.ResultsInterval, DefaultResultsInterval)
	oldest := s.firstSeen
	if c := bundles[0].created; !c.IsZero() && c.Before(oldest) {
		oldest = c
	}
	due := total >= probewire.MaxBatchResults || len(msgs) == uplinkPeekBundles || now.Sub(oldest) >= interval
	if !due {
		return min(interval-now.Sub(oldest), poll)
	}
	if gap := s.gap(); !s.lastSent.IsZero() && now.Sub(s.lastSent) < gap {
		return gap - now.Sub(s.lastSent)
	}
	return s.send(ctx, bundles)
}

// gap is the shortest time between two result messages (SyncRate).
func (s *resultsSender) gap() time.Duration {
	rate := s.u.SyncRate
	if rate <= 0 {
		rate = DefaultSyncRate
	}
	return time.Second / time.Duration(rate)
}

// send sends the first bundles that fit into one message (at most
// probewire.MaxBatchResults results and MaxUplinkMessageBytes); a bundle
// that does not fit alone goes in parts. It acknowledges the bundles up to
// the seq of the reply.
func (s *resultsSender) send(ctx context.Context, bundles []bundleOf) time.Duration {
	u := s.u
	n, count := 0, 0
	for n < len(bundles) && count+len(bundles[n].results) <= probewire.MaxBatchResults {
		count += len(bundles[n].results)
		n++
	}
	if n == 0 {
		n = 1
	}
	if n == 1 && len(bundles[0].results) > probewire.MaxBatchResults {
		return s.sendInParts(ctx, &bundles[0])
	}
	for {
		var results []probewire.Result
		for i := range bundles[:n] {
			results = append(results, bundles[i].results...)
		}
		data, err := encodeResults(results)
		if err != nil {
			u.quarantine(ctx, u.Outbox.Results(), KindResults, UplinkReply{Error: "unencodable bundle"}, bundles[0].id)
			return 0
		}
		if len(data) <= MaxUplinkMessageBytes {
			return s.deliver(ctx, bundles[:n], data, len(results))
		}
		if n == 1 {
			return s.sendInParts(ctx, &bundles[0])
		}
		n--
	}
}

// deliver sends one encoded message of the given bundles.
func (s *resultsSender) deliver(ctx context.Context, bundles []bundleOf, data []byte, count int) time.Duration {
	u := s.u
	poll := durationOr(u.Poll, uplinkPoll)
	s.lastSent = time.Now()
	out, reply := u.sendOne(ctx, KindResults, data)
	maxSeq := bundles[len(bundles)-1].seqTo
	switch out {
	case sentOK:
		upTo := maxSeq
		if reply.Seq != nil {
			upTo = *reply.Seq
		}
		var ids []string
		for i := range bundles {
			if bundles[i].seqTo <= upTo {
				ids = append(ids, bundles[i].id)
			}
		}
		if len(ids) == 0 {
			u.retries.Add(1)
			return s.bo.next()
		}
		if err := u.Outbox.Results().Ack(ctx, ids); err != nil {
			slog.Warn("probe uplink: result bundles not acknowledged in the spool", "error", err)
			return poll
		}
		s.bo.reset()
		s.firstSeen = time.Time{}
		u.resultMessages.Add(1)
		u.resultsSent.Add(uint64(count))
		return 0
	case sentRefused, sentTooLarge:
		s.bo.reset()
		ids := make([]string, 0, len(bundles))
		for i := range bundles {
			ids = append(ids, bundles[i].id)
		}
		u.quarantine(ctx, u.Outbox.Results(), KindResults, reply, ids...)
		return 0
	case sentNotLicensed:
		return min(u.pauseLeft(time.Now()), poll)
	case sentNoEdge:
		return poll
	default:
		u.retries.Add(1)
		return s.bo.next()
	}
}

// sendInParts sends a bundle that does not fit into one message in parts
// of decreasing size; the bundle is acknowledged after its last part.
func (s *resultsSender) sendInParts(ctx context.Context, b *bundleOf) time.Duration {
	u := s.u
	poll := durationOr(u.Poll, uplinkPoll)
	rest := b.results
	size := min(len(rest), probewire.MaxBatchResults)
	for len(rest) > 0 {
		part := rest[:min(size, len(rest))]
		data, err := encodeResults(part)
		if err != nil || (len(data) > MaxUplinkMessageBytes && len(part) == 1) {
			u.quarantine(ctx, u.Outbox.Results(), KindResults, UplinkReply{Error: "result larger than a message"}, b.id)
			return 0
		}
		if len(data) > MaxUplinkMessageBytes {
			size = max(len(part)/2, 1)
			continue
		}
		if wait := s.gap() - time.Since(s.lastSent); !s.lastSent.IsZero() && wait > 0 {
			// The parts count as messages of the sync rate.
			select {
			case <-ctx.Done():
				return poll
			case <-time.After(wait):
			}
		}
		s.lastSent = time.Now()
		out, reply := u.sendOne(ctx, KindResults, data)
		switch out {
		case sentOK:
			s.bo.reset()
			u.resultMessages.Add(1)
			u.resultsSent.Add(uint64(len(part)))
			rest = rest[len(part):]
		case sentRefused, sentTooLarge:
			u.quarantine(ctx, u.Outbox.Results(), KindResults, reply, b.id)
			return 0
		case sentNotLicensed:
			return min(u.pauseLeft(time.Now()), poll)
		case sentNoEdge:
			return poll
		default:
			u.retries.Add(1)
			return s.bo.next()
		}
	}
	if err := u.Outbox.Results().Ack(ctx, []string{b.id}); err != nil {
		slog.Warn("probe uplink: result bundle not acknowledged in the spool", "error", err)
		return poll
	}
	s.firstSeen = time.Time{}
	return 0
}

// encodeResults encodes results as a probewire.ResultBatch.
func encodeResults(results []probewire.Result) ([]byte, error) {
	data, err := probewire.Encode(&probewire.ResultBatch{Results: results})
	if err != nil {
		return nil, fmt.Errorf("probe: encode result message: %w", err)
	}
	return data, nil
}
