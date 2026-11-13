package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

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
// The assignment set (WP-956) feeds Upsert and Remove; a removed probe
// reports no more results from then on.
type Engine struct {
	Scheduler *Scheduler
	States    *StateMachine
	Outbox    *Outbox
	Uplink    *Uplink

	mu    sync.RWMutex
	specs map[string]probewire.ProbeSpec
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
	e := &Engine{States: states, Outbox: outbox, specs: map[string]probewire.ProbeSpec{}}
	e.Uplink = NewUplink(outbox, cfg.Send)
	e.Uplink.SyncRate = cfg.SyncRate
	e.Scheduler = &Scheduler{Run: cfg.Run, Creds: cfg.Creds, Sink: e.observe}
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
	if err := e.Outbox.AddResult(ctx, &res, change != nil); err != nil && !errors.Is(err, buffer.ErrSpoolFull) {
		slog.Warn("probe engine: result bundle not written; kept", "error", err)
	}
}
