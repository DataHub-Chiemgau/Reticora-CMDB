package probe

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The monitoring alive message (PRB-10, E-176, WP-957): every 10 s the
// engine sends a probewire.Alive on ….probe.alive with the collector's
// clock, the version of the assignment set, the load figures and the state
// of the probe spool. It goes even without assigned probes; it is never
// spooled: without the edge it is left out and retried every second, so
// the first one after a reconnect is a current one. The heartbeat of
// COL-05 (60 s) stays as it is.
const (
	// AliveInterval is the interval of the alive message.
	AliveInterval = 10 * time.Second
	// AliveRetry is the wait while the edge is not connected.
	AliveRetry = time.Second
	// aliveWindow is the window of results_per_s.
	aliveWindow = 60 * time.Second
)

// AliveSender sends the alive message of an engine.
type AliveSender struct {
	// Send sends the message (KindAlive).
	Send SendFunc
	// Snapshot returns the figures of the message; collector_time is set
	// by the sender.
	Snapshot func(ctx context.Context, now time.Time) probewire.Alive
	// Clock is the time source; nil uses the wall clock.
	Clock Clock
	// Wait waits d or until ctx ends (false); nil uses a timer. Tests pass
	// a fake that advances their clock.
	Wait func(ctx context.Context, d time.Duration) bool
	// Interval and Retry default to AliveInterval and AliveRetry.
	Interval, Retry time.Duration
	// Logger receives the answers at debug level; nil uses slog.Default.
	Logger *slog.Logger
}

// aliveOutcome is the outcome of one alive message.
type aliveOutcome int

const (
	aliveSent aliveOutcome = iota
	aliveNoEdge
	aliveFailed
)

func (a *AliveSender) now() time.Time {
	if a.Clock == nil {
		return time.Now()
	}
	return a.Clock.Now()
}

func (a *AliveSender) log() *slog.Logger {
	if a.Logger == nil {
		return slog.Default()
	}
	return a.Logger
}

func (a *AliveSender) wait(ctx context.Context, d time.Duration) bool {
	if a.Wait != nil {
		return a.Wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Run sends the alive message every Interval until ctx ends; while the
// edge is not connected it tries again every Retry with a fresh message.
func (a *AliveSender) Run(ctx context.Context) {
	for ctx.Err() == nil {
		wait := durationOr(a.Interval, AliveInterval)
		if a.SendOnce(ctx) == aliveNoEdge {
			wait = durationOr(a.Retry, AliveRetry)
		}
		if !a.wait(ctx, wait) {
			return
		}
	}
}

// SendOnce builds and sends one alive message. The round trip time (send
// to answer) and the server's clock go to the log at debug level; the
// offset is evaluated on the server (WP-962).
func (a *AliveSender) SendOnce(ctx context.Context) aliveOutcome {
	now := a.now()
	msg := probewire.Alive{}
	if a.Snapshot != nil {
		msg = a.Snapshot(ctx, now)
	}
	msg.CollectorTime = now.UTC()
	data, err := probewire.Encode(&msg)
	if err != nil {
		a.log().Warn("probe alive message not encoded", "error", err)
		return aliveFailed
	}
	raw, err := a.Send(ctx, KindAlive, data)
	rtt := a.now().Sub(now)
	switch {
	case errors.Is(err, ErrNoEdge):
		return aliveNoEdge
	case err != nil:
		a.log().Debug("probe alive message not answered", "error", err)
		return aliveFailed
	}
	var status UplinkReply
	var reply probewire.AliveReply
	if json.Unmarshal(raw, &status) != nil || json.Unmarshal(raw, &reply) != nil {
		a.log().Debug("probe alive answer unreadable")
		return aliveFailed
	}
	if status.Status >= http.StatusMultipleChoices {
		a.log().Debug("probe alive message refused", "status", status.Status, "type", status.Type)
		return aliveFailed
	}
	a.log().Debug("probe alive answered", "server_time", reply.ServerTime, "collector_time", msg.CollectorTime,
		"rtt_ms", float64(rtt.Microseconds())/1000)
	return aliveSent
}

// resultRate is the results per second over the last aliveWindow, from
// samples of a counter taken with every alive message.
type resultRate struct {
	mu      sync.Mutex
	samples []rateSample
}

type rateSample struct {
	at    time.Time
	count uint64
}

// observe adds a sample and returns the rate over the window (0 with a
// single sample).
func (r *resultRate) observe(now time.Time, count uint64) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples = append(r.samples, rateSample{at: now, count: count})
	// Keep the newest sample at or before the start of the window.
	for len(r.samples) > 2 && !r.samples[1].at.After(now.Add(-aliveWindow)) {
		r.samples = r.samples[1:]
	}
	first := r.samples[0]
	span := now.Sub(first.at).Seconds()
	if span <= 0 || count < first.count {
		return 0
	}
	return float64(count-first.count) / span
}

// Capability names the ICMP socket of a family for the alive message:
// raw, unprivileged (datagram socket) or unavailable.
func (m *Mux) Capability(family int) string {
	f := m.family(family)
	if _, err := f.socket(); err != nil {
		return probewire.ICMPUnavailable
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unpriv {
		return probewire.ICMPUnprivileged
	}
	return probewire.ICMPRaw
}

// queueState derives the state of the probe spool: overflow when results
// were compacted or refused at the size limit since the last message,
// backlog when state changes or more than one result bundle wait, ok else.
func queueState(st *OutboxStats, prevLosses map[string]int64) string {
	switch {
	case st.Losses[OutboxLossCompacted] > prevLosses[OutboxLossCompacted],
		st.Losses[OutboxLossMaxBytes] > prevLosses[OutboxLossMaxBytes]:
		return probewire.QueueOverflow
	case st.StateMessages > 0 || st.ResultBundles > 1:
		return probewire.QueueBacklog
	}
	return probewire.QueueOK
}
