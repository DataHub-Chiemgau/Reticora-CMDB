package probe

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// TestEngineEndToEndWithFakeExecutor covers AK5: the engine runs the
// scheduler with a fake executor, the state machine evaluates the
// results, the state changes and results go through the spool and the
// uplink to the (fake) edge and leave the spool after the reply; a removed
// probe reports nothing more.
func TestEngineEndToEndWithFakeExecutor(t *testing.T) {
	f := newFakeEdge()
	var calls atomic.Int64
	run := func(_ context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
		n := calls.Add(1)
		r := probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: time.Now().UTC()}
		if n <= 3 {
			r.Status, r.Code = probewire.StatusFail, probewire.ReasonTimeout
			return r
		}
		rtt := 1.5
		r.Status, r.RTTMS = probewire.StatusOK, &rtt
		return r
	}
	dir := t.TempDir()
	e, err := NewEngine(&EngineConfig{SpoolDir: dir, Send: f.send, SyncRate: 100, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	e.Uplink.Poll = 20 * time.Millisecond
	e.Uplink.ResultsInterval = 200 * time.Millisecond
	e.Upsert(probewire.ProbeSpec{ID: "probe-e2e", Type: "icmp", Target: "192.0.2.1", IntervalMS: 100, TimeoutMS: 50,
		DownCount: 2, UpCount: 1, Epoch: 7})
	if e.Probes() != 1 {
		t.Fatalf("engine has %d probes, want 1", e.Probes())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	defer func() { cancel(); <-done }()

	got := f.waitFor(t, 10*time.Second, func(c []edgeCall) bool {
		states := 0
		for _, x := range c {
			if x.kind == KindState {
				states++
			}
		}
		return states >= 2 && countResults(c) >= 6
	})
	var changes []probewire.StateChange
	var lastSeq uint64
	for _, c := range got {
		switch c.kind {
		case KindState:
			changes = append(changes, c.states...)
		case KindResults:
			for _, s := range c.seqs {
				if s <= lastSeq {
					t.Fatalf("result seq %d after %d", s, lastSeq)
				}
				lastSeq = s
			}
		}
	}
	if changes[0].From != probewire.StateUnknown || changes[0].To != probewire.StateDown ||
		changes[1].From != probewire.StateDown || changes[1].To != probewire.StateUp {
		t.Fatalf("state changes %+v, want unknown→down, down→up", changes[:2])
	}
	for i := range changes[:2] {
		c := &changes[i]
		if c.Epoch != 7 || c.ProbeID != "probe-e2e" || c.LastResult == nil || c.Seq == 0 {
			t.Fatalf("state change %+v lacks epoch, probe, seq or last result", c)
		}
	}

	e.Remove("probe-e2e")
	before := calls.Load()
	time.Sleep(300 * time.Millisecond)
	if after := calls.Load(); after > before+1 {
		t.Fatalf("%d checks after Remove, want at most the one in flight", after-before)
	}
	deadline := time.Now().Add(5 * time.Second)
	for queueLen(t, e.Outbox.States()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("state changes still in the spool")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEngineStopKeepsPendingResults: at the end of Run the results of the
// checks still running at the cancellation and the results not yet
// bundled are in the spool, and the counters are saved (PRB-07).
func TestEngineStopKeepsPendingResults(t *testing.T) {
	noEdge := func(context.Context, string, []byte) ([]byte, error) { return nil, ErrNoEdge }
	var calls atomic.Int64
	started := make(chan struct{}, 64)
	run := func(ctx context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
		n := calls.Add(1)
		started <- struct{}{}
		if n >= 3 {
			// Still running at the cancellation; reports after it.
			<-ctx.Done()
			time.Sleep(50 * time.Millisecond)
		}
		rtt := 1.0
		return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: time.Now().UTC(),
			Status: probewire.StatusOK, RTTMS: &rtt}
	}
	dir := t.TempDir()
	e, err := NewEngine(&EngineConfig{SpoolDir: dir, Send: noEdge, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	e.Upsert(probewire.ProbeSpec{ID: "probe-stop", Type: "icmp", Target: "192.0.2.1", IntervalMS: 100, TimeoutMS: 50,
		DownCount: 2, UpCount: 1, Epoch: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("checks not started")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	msgs, err := e.Outbox.Results().PeekBatch(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	spooled := 0
	for i := range msgs {
		batch, _, derr := DecodeBundle(&msgs[i])
		if derr != nil {
			t.Fatal(derr)
		}
		spooled += len(batch.Results)
	}
	if want := int(calls.Load()); spooled != want {
		t.Fatalf("%d results in the spool, want %d (every check, the one running at the cancellation included)", spooled, want)
	}
	reopened, err := OpenStateMachine(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Counters("probe-stop"); !ok {
		t.Fatal("counters of the probe not saved at the end of Run")
	}
}

// TestEngineRemoveDuringObserveLeavesNoCounters: a check that finishes
// while its probe is removed does not recreate the counters of the probe.
func TestEngineRemoveDuringObserveLeavesNoCounters(t *testing.T) {
	noEdge := func(context.Context, string, []byte) ([]byte, error) { return nil, ErrNoEdge }
	e, err := NewEngine(&EngineConfig{SpoolDir: t.TempDir(), Send: noEdge})
	if err != nil {
		t.Fatal(err)
	}
	spec := probewire.ProbeSpec{ID: "probe-race", Type: "icmp", Target: "192.0.2.1", IntervalMS: 1000, TimeoutMS: 500,
		DownCount: 2, UpCount: 1, Epoch: 1}
	for i := range 300 {
		e.Upsert(spec)
		rtt := 1.0
		r := probewire.Result{ProbeID: spec.ID, Epoch: 1, ObservedAt: time.Now().UTC(), Status: probewire.StatusOK, RTTMS: &rtt}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); e.observe(r) }()
		go func() { defer wg.Done(); e.Remove(spec.ID) }()
		wg.Wait()
		if _, ok := e.States.Counters(spec.ID); ok {
			t.Fatalf("round %d: counters of the removed probe recreated", i)
		}
	}
}

func trapEvent(kind, source string, at time.Time) probewire.Event {
	return probewire.Event{ID: "0193f1a2-7b3c-7d4e-8f50-0123456789ab", Kind: kind, SourceIP: source,
		TrapOID: "1.3.6.1.6.3.1.1.5.3", Varbinds: map[string]string{"1.3.6.1.2.1.2.2.1.1.7": "7"}, ObservedAt: at}
}

// TestLinkDownTrapRechecksWithinTwoSeconds covers WP-979 AK4 and AK5: a
// linkDown trap checks the probe of its sender at once; with the simulated
// clock the result lies at most 2 s after the receipt of the trap.
func TestLinkDownTrapRechecksWithinTwoSeconds(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	var mu sync.Mutex
	var observed []time.Time
	run := func(_ context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
		return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: clock.Now(), Status: probewire.StatusOK}
	}
	e, err := NewEngine(&EngineConfig{SpoolDir: t.TempDir(), Send: trapNoEdge, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	e.Scheduler.Clock = clock
	e.Scheduler.Go = inline
	e.Scheduler.Limits = unlimited()
	sink := e.Scheduler.Sink
	e.Scheduler.Sink = func(r probewire.Result) {
		mu.Lock()
		observed = append(observed, r.ObservedAt)
		mu.Unlock()
		sink(r)
	}
	e.Upsert(probewire.ProbeSpec{ID: "p-trap", Type: TypeTCP, Target: "192.0.2.20", IntervalMS: 300_000, TimeoutMS: 1000,
		DownCount: 3, UpCount: 1, Epoch: 1, Params: []byte(`{"port":22}`)})
	e.Scheduler.Advance(clock.Now())
	mu.Lock()
	observed = nil
	mu.Unlock()
	received := clock.Now().Add(30 * time.Second)
	clock.Set(received)
	e.OnTrap(trapEvent(probewire.EventLinkDown, "192.0.2.20", received))
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 1 || observed[0].Sub(received) > 2*time.Second || observed[0].Before(received) {
		t.Fatalf("results after the trap at %v: %v", received, observed)
	}
	// An authenticationFailure trap triggers nothing.
	clock.Set(received.Add(time.Minute))
	mu.Unlock()
	e.OnTrap(trapEvent(probewire.EventAuthFailure, "192.0.2.20", clock.Now()))
	mu.Lock()
	if len(observed) != 1 {
		t.Fatalf("authFailure triggered a check: %v", observed)
	}
}

// TestTrapEventQueuedWithResults covers WP-979 AK2: the event goes with the
// next result bundle; an invalid event is dropped.
func TestTrapEventQueuedWithResults(t *testing.T) {
	e, err := NewEngine(&EngineConfig{SpoolDir: t.TempDir(), Send: trapNoEdge})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	e.OnTrap(trapEvent(probewire.EventLinkUp, "192.0.2.30", at))
	bad := trapEvent(probewire.EventLinkUp, "not-an-ip", at)
	e.OnTrap(bad)
	ctx := context.Background()
	r := probewire.Result{ProbeID: "p-1", Seq: 1, ObservedAt: at, Status: probewire.StatusOK}
	if aerr := e.Outbox.AddResult(ctx, &r, false); aerr != nil {
		t.Fatal(aerr)
	}
	if ferr := e.Outbox.Flush(ctx); ferr != nil {
		t.Fatal(ferr)
	}
	msgs, err := e.Outbox.Results().PeekBatch(ctx, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("bundles %d, %v", len(msgs), err)
	}
	batch, _, err := DecodeBundle(&msgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || len(batch.Events) != 1 || batch.Events[0].Kind != probewire.EventLinkUp ||
		batch.Events[0].SourceIP != "192.0.2.30" {
		t.Fatalf("batch = %+v", batch)
	}
	// The encoded uplink message carries the events.
	data, err := encodeResults(batch.Results, batch.Events...)
	if err != nil {
		t.Fatal(err)
	}
	var sent probewire.ResultBatch
	if err := probewire.Decode(data, &sent); err != nil || len(sent.Events) != 1 {
		t.Fatalf("uplink message %+v, %v", sent, err)
	}
}

func trapNoEdge(context.Context, string, []byte) ([]byte, error) { return nil, ErrNoEdge }
