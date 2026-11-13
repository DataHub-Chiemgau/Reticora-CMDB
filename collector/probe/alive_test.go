package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The monitoring alive message (PRB-10, PRB-09, WP-957) with a fake clock
// and a fake edge.

// aliveEdge is a fake edge for the alive message: it records every
// message it accepted and is disconnected while down is set.
type aliveEdge struct {
	clock *fakeClock
	down  atomic.Bool

	mu       sync.Mutex
	accepted []probewire.Alive
	attempts int
}

func (f *aliveEdge) send(_ context.Context, kind string, data []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if kind != KindAlive {
		return nil, errors.New("unexpected kind " + kind)
	}
	if f.down.Load() {
		return nil, ErrNoEdge
	}
	var a probewire.Alive
	if err := probewire.Decode(data, &a); err != nil {
		return nil, err
	}
	f.accepted = append(f.accepted, a)
	server := f.clock.Now().Add(2 * time.Second).UTC()
	return json.Marshal(UplinkReply{Status: http.StatusOK, ServerTime: &server})
}

func (f *aliveEdge) messages() []probewire.Alive {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]probewire.Alive(nil), f.accepted...)
}

// fakeWait advances the clock by every wait, records it and stops after
// rounds waits; before each wait it calls step with the wait's number.
type fakeWait struct {
	clock  *fakeClock
	rounds int
	step   func(n int)

	waits []time.Duration
}

func (w *fakeWait) wait(_ context.Context, d time.Duration) bool {
	w.waits = append(w.waits, d)
	if len(w.waits) > w.rounds {
		return false
	}
	w.clock.Set(w.clock.Now().Add(d))
	if w.step != nil {
		w.step(len(w.waits))
	}
	return true
}

var aliveStart = time.Date(2026, 11, 13, 8, 0, 0, 0, time.FixedZone("CET", 3600))

func aliveEngine(t *testing.T, edge *aliveEdge) *Engine {
	t.Helper()
	var calls atomic.Int32
	mux := NewMux(fakeListen(&calls, func(family int) (*fakeICMP, error) {
		if family == 6 {
			return nil, ErrICMPUnavailable
		}
		return newFakeICMP(family, true), nil
	}))
	t.Cleanup(mux.Close)
	e, err := NewEngine(&EngineConfig{SpoolDir: t.TempDir(), Send: edge.send, ICMP: mux})
	if err != nil {
		t.Fatal(err)
	}
	e.Alive.Clock = edge.clock
	return e
}

// TestAliveEveryTenSeconds covers AK1: the alive message goes every 10 s
// with the collector's clock at sending (UTC) and the version of the
// applied assignment set.
func TestAliveEveryTenSeconds(t *testing.T) {
	clock := newFakeClock(aliveStart)
	edge := &aliveEdge{clock: clock}
	e := aliveEngine(t, edge)
	e.Apply(17, []probewire.ProbeSpec{{ID: "p1", Type: "icmp", Target: "192.0.2.1", IntervalMS: 30000, TimeoutMS: 10000,
		DownCount: 5, UpCount: 1, Epoch: 1}})
	w := &fakeWait{clock: clock, rounds: 3}
	e.Alive.Wait = w.wait
	e.Alive.Run(context.Background())

	got := edge.messages()
	if len(got) != 4 {
		t.Fatalf("%d alive messages, want 4", len(got))
	}
	for i, a := range got {
		want := aliveStart.Add(time.Duration(i) * AliveInterval).UTC()
		if !a.CollectorTime.Equal(want) || a.CollectorTime.Location() != time.UTC {
			t.Fatalf("message %d at %v, want %v (UTC)", i, a.CollectorTime, want)
		}
		if a.ConfigVersion != 17 || a.Probes != 1 || a.SchemaVersion != probewire.SchemaVersion {
			t.Fatalf("message %d: version %d, probes %d, schema %q", i, a.ConfigVersion, a.Probes, a.SchemaVersion)
		}
	}
	for _, d := range w.waits {
		if d != AliveInterval {
			t.Fatalf("waits %v, want every %v", w.waits, AliveInterval)
		}
	}
}

// TestAliveWithoutProbes covers AK2: without an assignment set the alive
// message goes all the same, with version 0 and no probes.
func TestAliveWithoutProbes(t *testing.T) {
	clock := newFakeClock(aliveStart)
	edge := &aliveEdge{clock: clock}
	e := aliveEngine(t, edge)
	if out := e.Alive.SendOnce(context.Background()); out != aliveSent {
		t.Fatalf("outcome %v, want sent", out)
	}
	got := edge.messages()
	if len(got) != 1 || got[0].Probes != 0 || got[0].ConfigVersion != 0 || got[0].QueueState != probewire.QueueOK {
		t.Fatalf("messages %+v, want one without probes", got)
	}
	if err := got[0].Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestAliveNotBufferedWhileDisconnected covers AK2: without the edge the
// message is not spooled; the collector tries again every second, and the
// first message after the reconnect carries the current clock. Nothing of
// the alive message is in the probe spool.
func TestAliveNotBufferedWhileDisconnected(t *testing.T) {
	clock := newFakeClock(aliveStart)
	edge := &aliveEdge{clock: clock}
	edge.down.Store(true)
	e := aliveEngine(t, edge)
	// Waits 1–4 while disconnected; before the fifth attempt the edge is back.
	w := &fakeWait{clock: clock, rounds: 6, step: func(n int) {
		if n == 4 {
			edge.down.Store(false)
		}
	}}
	e.Alive.Wait = w.wait
	e.Alive.Run(context.Background())

	got := edge.messages()
	if len(got) != 3 {
		t.Fatalf("%d messages accepted, want 3 (no replay of the missed ones)", len(got))
	}
	if want := aliveStart.Add(4 * AliveRetry).UTC(); !got[0].CollectorTime.Equal(want) {
		t.Fatalf("first message after the reconnect at %v, want the current clock %v", got[0].CollectorTime, want)
	}
	wantWaits := []time.Duration{AliveRetry, AliveRetry, AliveRetry, AliveRetry, AliveInterval, AliveInterval, AliveInterval}
	for i := range wantWaits {
		if w.waits[i] != wantWaits[i] {
			t.Fatalf("waits %v, want %v", w.waits, wantWaits)
		}
	}
	st, err := e.Outbox.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.StateMessages != 0 || st.ResultBundles != 0 || st.Pending != 0 {
		t.Fatalf("alive message spooled: %+v", st)
	}
}

// TestAliveCarriesQueueAndLossCounters covers AK1: the entries of both
// spools, the losses per reason, skipped and overrun checks, results per
// second over 60 s and the ICMP socket per family.
func TestAliveCarriesQueueAndLossCounters(t *testing.T) {
	clock := newFakeClock(aliveStart)
	edge := &aliveEdge{clock: clock}
	e := aliveEngine(t, edge)
	ctx := context.Background()

	// First message: the start of the rate window.
	e.Alive.SendOnce(ctx)

	spec := probewire.ProbeSpec{ID: "p1", Type: "icmp", Target: "192.0.2.1", IntervalMS: 30000, TimeoutMS: 10000,
		DownCount: 1, UpCount: 1, Epoch: 1}
	e.Apply(3, []probewire.ProbeSpec{spec})
	for i := range 600 {
		r := probewire.Result{ProbeID: spec.ID, Epoch: 1, ObservedAt: aliveStart.Add(time.Duration(i) * time.Millisecond),
			Status: probewire.StatusFail, Code: probewire.ReasonTimeout}
		e.observe(r)
	}
	if err := e.Outbox.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	e.Outbox.count(OutboxLossMaxBytes, 4)
	e.Outbox.count(OutboxLossCompacted, 120)
	e.Outbox.count(OutboxLossQuarantined, 2)
	e.Scheduler.nSkipped.Add(5)
	e.Scheduler.nOverrun.Add(7)

	clock.Set(aliveStart.Add(60 * time.Second))
	e.Alive.SendOnce(ctx)
	got := edge.messages()
	if len(got) != 2 {
		t.Fatalf("%d messages", len(got))
	}
	a := got[1]
	if a.QueueStates != 1 || a.QueueResults != 1 {
		t.Errorf("queues: %d state changes, %d result bundles, want 1 and 1", a.QueueStates, a.QueueResults)
	}
	if a.Dropped != 6 || a.DroppedByReason[OutboxLossMaxBytes] != 4 || a.DroppedByReason[OutboxLossCompacted] != 120 ||
		a.DroppedByReason[OutboxLossQuarantined] != 2 {
		t.Errorf("losses: dropped %d, by reason %v", a.Dropped, a.DroppedByReason)
	}
	if a.QueueState != probewire.QueueOverflow {
		t.Errorf("queue state %q after new losses, want overflow", a.QueueState)
	}
	if a.Skipped != 5 || a.Overrun != 7 {
		t.Errorf("skipped %d, overrun %d, want 5 and 7", a.Skipped, a.Overrun)
	}
	if a.ResultsPerS != 10 {
		t.Errorf("results_per_s %v, want 10 (600 results in 60 s)", a.ResultsPerS)
	}
	if a.ICMPCapability != probewire.ICMPUnprivileged || a.ICMPCapabilityV6 != probewire.ICMPUnavailable {
		t.Errorf("icmp capability %q / %q, want unprivileged / unavailable", a.ICMPCapability, a.ICMPCapabilityV6)
	}
	if a.ConfigVersion != 3 || a.Probes != 1 {
		t.Errorf("version %d, probes %d", a.ConfigVersion, a.Probes)
	}

	// Without new losses the spool with a waiting state change is backlog,
	// and the rate covers only the last 60 s.
	clock.Set(aliveStart.Add(130 * time.Second))
	e.Alive.SendOnce(ctx)
	got = edge.messages()
	if a := got[2]; a.QueueState != probewire.QueueBacklog || a.ResultsPerS != 0 {
		t.Errorf("third message: queue state %q, results_per_s %v; want backlog and 0", a.QueueState, a.ResultsPerS)
	}
}

// TestAliveReplyLogged covers AK4: the answer with server_time is read
// and the round trip time measured; a refusal is no success.
func TestAliveReplyLogged(t *testing.T) {
	clock := newFakeClock(aliveStart)
	s := &AliveSender{Clock: clock, Send: func(context.Context, string, []byte) ([]byte, error) {
		clock.Set(clock.Now().Add(40 * time.Millisecond))
		return []byte(`{"status":403,"type":"` + ProblemFeatureNotEntitled + `"}`), nil
	}}
	if out := s.SendOnce(context.Background()); out != aliveFailed {
		t.Fatalf("refused message counted as %v", out)
	}

	// An answer with server_time: logged at debug level with the round
	// trip time of the fake clock.
	const serverTime = "2026-11-02T08:00:00.012Z"
	var buf bytes.Buffer
	ok := &AliveSender{Clock: clock, Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Send: func(context.Context, string, []byte) ([]byte, error) {
			clock.Set(clock.Now().Add(40 * time.Millisecond))
			return []byte(`{"status":200,"server_time":"` + serverTime + `"}`), nil
		}}
	if out := ok.SendOnce(context.Background()); out != aliveSent {
		t.Fatalf("answered message counted as %v", out)
	}
	var entry struct {
		Level      string  `json:"level"`
		Msg        string  `json:"msg"`
		ServerTime string  `json:"server_time"`
		RTTMS      float64 `json:"rtt_ms"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("log %q: %v", buf.String(), err)
	}
	want, _ := time.Parse(time.RFC3339Nano, serverTime)
	got, err := time.Parse(time.RFC3339Nano, entry.ServerTime)
	if entry.Msg != "probe alive answered" || entry.Level != "DEBUG" || err != nil || !got.Equal(want) || entry.RTTMS != 40 {
		t.Fatalf("log entry %+v, want probe alive answered at DEBUG with server_time %s and rtt_ms 40", entry, serverTime)
	}
}

// TestAliveAfterRestartNoOverflowFromOldLosses covers AK2: the loss counts
// persist in the spool, and the first alive message after a restart
// compares with them, so losses before the restart are no overflow.
func TestAliveAfterRestartNoOverflowFromOldLosses(t *testing.T) {
	dir := t.TempDir()
	first, err := NewEngine(&EngineConfig{SpoolDir: dir, Send: func(context.Context, string, []byte) ([]byte, error) {
		return nil, ErrNoEdge
	}})
	if err != nil {
		t.Fatal(err)
	}
	first.Outbox.count(OutboxLossCompacted, 12)
	first.Outbox.count(OutboxLossMaxBytes, 3)

	clock := newFakeClock(aliveStart)
	edge := &aliveEdge{clock: clock}
	var calls atomic.Int32
	mux := NewMux(fakeListen(&calls, func(family int) (*fakeICMP, error) { return newFakeICMP(family, true), nil }))
	t.Cleanup(mux.Close)
	e, err := NewEngine(&EngineConfig{SpoolDir: dir, Send: edge.send, ICMP: mux})
	if err != nil {
		t.Fatal(err)
	}
	e.Alive.Clock = clock
	e.Alive.SendOnce(context.Background())
	got := edge.messages()
	if len(got) != 1 {
		t.Fatalf("%d messages", len(got))
	}
	if got[0].QueueState == probewire.QueueOverflow || got[0].DroppedByReason[OutboxLossCompacted] != 12 {
		t.Fatalf("first message after restart: queue state %q, losses %v; want no overflow and the kept counts",
			got[0].QueueState, got[0].DroppedByReason)
	}
	// A new loss after the restart is an overflow again.
	e.Outbox.count(OutboxLossMaxBytes, 1)
	e.Alive.SendOnce(context.Background())
	if got = edge.messages(); got[1].QueueState != probewire.QueueOverflow {
		t.Fatalf("queue state %q after a new loss, want overflow", got[1].QueueState)
	}
}
