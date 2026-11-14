package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Engine connects the parts of the probe execution of a collector
// (WP-955 AK5): executor register (Run), Scheduler, StateMachine, the
// persistent spool (Outbox) and the Uplink. Every result of the scheduler
// goes through the state machine; a state change is spooled first and
// then handed to the uplink at once (NotifyState), the result goes into
// the current bundle of the result queue. The metric buffer of the
// collector is not involved.
//
// Run also sends the monitoring alive message every 10 s (Alive, WP-957).
//
// The assignment set (WP-956) feeds Apply, the diff into Upsert and
// Remove; a removed probe reports no more results from then on. The
// version of the applied set is the configuration version of the alive
// message (ConfigVersion, WP-957).
type Engine struct {
	Scheduler *Scheduler
	States    *StateMachine
	Outbox    *Outbox
	Uplink    *Uplink

	mu    sync.RWMutex
	specs map[string]probewire.ProbeSpec

	applyMu       sync.Mutex
	configVersion atomic.Int64

	// Alive sends the monitoring alive message (WP-957).
	Alive *AliveSender
	// Capabilities is the capability report every alive message carries
	// (WP-966); the collector sets it from DetectCapabilities.
	Capabilities *Capabilities

	icmp       *Mux
	observed   atomic.Uint64 // results evaluated (results_per_s)
	rate       resultRate
	lossMu     sync.Mutex
	prevLosses map[string]int64
}

// EngineConfig configures an engine.
type EngineConfig struct {
	// SpoolDir holds the spool, the sequence counters and the counters of
	// the state machine (the spool directory of the collector).
	SpoolDir string
	// SpoolMaxBytes is the budget of the probe spool
	// (RETICORA_PROBE_SPOOL_MAX_BYTES; 0: DefaultOutboxMaxBytes).
	SpoolMaxBytes int64
	// Send sends a message over the edge (KindState, KindResults).
	Send SendFunc
	// SyncRate is the most result messages per second
	// (RETICORA_PROBE_SYNC_RATE; 0: DefaultSyncRate).
	SyncRate int
	// Run executes a check; nil uses Run of this package (tests replace it).
	Run RunFunc
	// Creds returns the resolved credentials of a probe; nil passes none.
	Creds func(spec *probewire.ProbeSpec) map[string]string
	// ICMP is the multiplexer whose sockets the alive message reports;
	// nil uses DefaultMux.
	ICMP *Mux
}

// NewEngine opens sequence, state machine and spool below cfg.SpoolDir.
func NewEngine(cfg *EngineConfig) (*Engine, error) {
	if cfg.SpoolDir == "" {
		return nil, errors.New("probe: engine needs a spool directory")
	}
	if cfg.Send == nil {
		return nil, errors.New("probe: engine needs a sender")
	}
	if err := os.MkdirAll(cfg.SpoolDir, 0o700); err != nil {
		return nil, fmt.Errorf("probe: create spool directory: %w", err)
	}
	seq, err := OpenSequence(cfg.SpoolDir)
	if err != nil {
		return nil, err
	}
	states, err := OpenStateMachine(cfg.SpoolDir, seq)
	if err != nil {
		return nil, err
	}
	outbox, err := OpenOutbox(cfg.SpoolDir, cfg.SpoolMaxBytes)
	if err != nil {
		return nil, err
	}
	// The loss counts persist across restarts; the first alive message
	// compares with them, not with zero, so old losses are no overflow
	// "since the last message" (WP-957 AK2).
	e := &Engine{States: states, Outbox: outbox, specs: map[string]probewire.ProbeSpec{}, prevLosses: outbox.lossCounts()}
	e.Uplink = NewUplink(outbox, cfg.Send)
	e.Uplink.SyncRate = cfg.SyncRate
	e.Scheduler = &Scheduler{Run: cfg.Run, Creds: cfg.Creds, Sink: e.observe}
	e.icmp = cfg.ICMP
	if e.icmp == nil {
		e.icmp = DefaultMux()
	}
	e.Alive = &AliveSender{Send: cfg.Send, Snapshot: e.aliveSnapshot}
	e.Capabilities = &Capabilities{}
	return e, nil
}

// Upsert adds or changes a probe.
func (e *Engine) Upsert(spec probewire.ProbeSpec) { //nolint:gocritic // the spec is copied into the engine on purpose
	e.mu.Lock()
	e.specs[spec.ID] = spec
	e.mu.Unlock()
	e.Scheduler.Upsert(spec)
}

// Remove stops a probe: it is not started again, and a check still
// running is not reported.
func (e *Engine) Remove(id string) {
	// The counters go under the same lock that observe holds from the
	// look-up of the spec to the spooling, so that a check finishing
	// meanwhile neither recreates them nor spools its result.
	e.mu.Lock()
	delete(e.specs, id)
	e.States.Remove(id)
	e.mu.Unlock()
	e.Scheduler.Remove(id)
}

// ApplyStats counts the probes a diff of Apply added, changed and removed.
type ApplyStats struct {
	Added, Changed, Removed int
}

// Apply makes probes the set of the engine (WP-956 AK3, AK4): a probe
// missing from it is removed (a check in progress is cancelled and not
// reported), a new one is added, a changed one is upserted with its hash
// phase kept; an unchanged one is left alone. A change of epoch takes
// effect with the next check; the counters of the state machine stay.
// version becomes the configuration version (ConfigVersion).
func (e *Engine) Apply(version int64, probes []probewire.ProbeSpec) ApplyStats {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	next := make(map[string]*probewire.ProbeSpec, len(probes))
	for i := range probes {
		next[probes[i].ID] = &probes[i]
	}
	e.mu.RLock()
	old := make(map[string]probewire.ProbeSpec, len(e.specs))
	for id := range e.specs {
		old[id] = e.specs[id]
	}
	e.mu.RUnlock()
	var st ApplyStats
	for id := range old {
		if _, ok := next[id]; !ok {
			e.Remove(id)
			st.Removed++
		}
	}
	for id, spec := range next {
		prev, ok := old[id]
		switch {
		case !ok:
			st.Added++
		case reflect.DeepEqual(prev, *spec):
			continue
		default:
			st.Changed++
		}
		e.Upsert(*spec)
	}
	e.configVersion.Store(version)
	return st
}

// ConfigVersion is the version of the assignment set applied last (0:
// none yet).
func (e *Engine) ConfigVersion() int64 { return e.configVersion.Load() }

// Probes returns the number of probes of the engine.
func (e *Engine) Probes() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.specs)
}

// Run runs scheduler, spool, state persistence and uplink until ctx ends.
// At the end the scheduler stops first and the running checks are awaited;
// only then are the pending results bundled into the spool and the
// counters saved, so that no result of a check that was still running is
// lost (PRB-07).
func (e *Engine) Run(ctx context.Context) {
	// Spool and persistence end after the scheduler, not with ctx.
	tail, stopTail := context.WithCancel(context.WithoutCancel(ctx))
	defer stopTail()
	var wg sync.WaitGroup
	run := func(c context.Context, f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(c) }()
	}
	run(tail, e.Outbox.Run)
	run(ctx, e.Uplink.Run)
	run(ctx, e.Alive.Run)
	run(tail, func(c context.Context) {
		if err := e.States.RunPersist(c, PersistInterval); err != nil {
			slog.Warn("probe engine: state counters not saved", "error", err)
		}
	})
	e.Scheduler.RunLoop(ctx)
	e.Scheduler.Wait()
	stopTail() // final Outbox.Flush and StateMachine.Save
	wg.Wait()
}

// observe evaluates one result of the scheduler (Scheduler.Sink): the
// state machine assigns its seq; a state change is spooled and sent at
// once, the result goes into the result queue.
func (e *Engine) observe(r probewire.Result) { //nolint:gocritic // Sink signature of the scheduler
	// Held until the result is spooled: Remove waits for it (see Remove).
	e.mu.RLock()
	defer e.mu.RUnlock()
	spec, ok := e.specs[r.ProbeID]
	if !ok {
		return // removed meanwhile: no result any more (WP-956 AK3)
	}
	if r.Epoch != 0 {
		// The result keeps the epoch its check started under; a newer
		// epoch of the set takes effect with the next check (WP-956 AK4).
		spec.Epoch = r.Epoch
	}
	if err := r.Validate(); err != nil {
		// A value the wire cannot carry (NaN, ±Inf, unknown code) must not
		// make the bundle unencodable: the check counts as not executable.
		slog.Warn("probe engine: invalid result of the executor", "type", spec.Type, "error", err)
		r = probewire.Result{ProbeID: r.ProbeID, Epoch: r.Epoch, ObservedAt: r.ObservedAt,
			Status: probewire.StatusError, Code: probewire.ReasonConfig}
	}
	ctx := context.Background()
	res, change, err := e.States.Observe(&spec, r)
	if err != nil {
		slog.Error("probe engine: no sequence number; result dropped", "error", err)
		return
	}
	if change != nil {
		if err := e.Outbox.EnqueueState(ctx, change); err != nil {
			slog.Error("probe engine: state change not spooled", "probe_id", change.ProbeID,
				"spool_full", errors.Is(err, buffer.ErrSpoolFull), "error", err)
		} else {
			e.Uplink.NotifyState()
		}
	}
	e.observed.Add(1)
	if err := e.Outbox.AddResult(ctx, &res, change != nil); err != nil && !errors.Is(err, buffer.ErrSpoolFull) {
		slog.Warn("probe engine: result bundle not written; kept", "error", err)
	}
}

// aliveSnapshot returns the figures of the alive message (WP-957 AK1):
// configuration version, probes, results per second over 60 s, the
// entries of both spools, the losses per reason, the skipped and overrun
// checks, the ICMP socket per family and the capability report (WP-966).
func (e *Engine) aliveSnapshot(ctx context.Context, now time.Time) probewire.Alive {
	sched := e.Scheduler.Stats()
	a := probewire.Alive{
		ConfigVersion:     e.ConfigVersion(),
		Probes:            e.Probes(),
		ResultsPerS:       e.rate.observe(now, e.observed.Load()),
		QueueState:        probewire.QueueOK,
		Skipped:           int64(sched.Skipped), //nolint:gosec // a counter of checks, far below 2^63
		Overrun:           int64(sched.Overrun), //nolint:gosec // a counter of checks, far below 2^63
		ICMPCapability:    e.icmp.Capability(4),
		ICMPCapabilityV6:  e.icmp.Capability(6),
		ProbeCapabilities: e.Capabilities.Report(),
	}
	st, err := e.Outbox.Stats(ctx)
	if err != nil {
		slog.Warn("probe engine: spool state for the alive message unavailable", "error", err)
		return a
	}
	a.QueueResults, a.QueueStates = int64(st.ResultBundles), int64(st.StateMessages)
	a.Dropped = st.Lost()
	if len(st.Losses) > 0 {
		a.DroppedByReason = st.Losses
	}
	e.lossMu.Lock()
	a.QueueState = queueState(&st, e.prevLosses)
	e.prevLosses = st.Losses
	e.lossMu.Unlock()
	return a
}
