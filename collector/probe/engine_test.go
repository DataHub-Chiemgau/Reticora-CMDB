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
