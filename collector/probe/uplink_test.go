package probe

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// edgeCall is one request the fake edge received.
type edgeCall struct {
	kind   string
	at     time.Time
	size   int
	seqs   []uint64 // the seq of the state change or of every result
	states []probewire.StateChange
}

// fakeEdge answers the probe subjects like the server (WP-959): 202 with
// the highest seq of the message; answer replaces the reply.
type fakeEdge struct {
	mu     sync.Mutex
	calls  []edgeCall
	answer func(c *edgeCall) ([]byte, error)
	notify chan struct{}
}

func newFakeEdge() *fakeEdge { return &fakeEdge{notify: make(chan struct{}, 1024)} }

func reply(status int, seq *uint64, problem string) []byte {
	data, _ := json.Marshal(UplinkReply{Status: status, Seq: seq, Type: problem})
	return data
}

func (f *fakeEdge) send(_ context.Context, kind string, data []byte) ([]byte, error) {
	c := edgeCall{kind: kind, at: time.Now(), size: len(data)}
	if kind == KindAlive {
		// The alive message of the engine (WP-957) is answered and not
		// recorded: the tests of the uplink look at state and results.
		now := time.Now().UTC()
		raw, _ := json.Marshal(UplinkReply{Status: http.StatusOK, ServerTime: &now})
		return raw, nil
	}
	switch kind {
	case KindState:
		var sc probewire.StateChange
		if err := probewire.Decode(data, &sc); err != nil {
			return reply(http.StatusBadRequest, nil, ""), nil
		}
		c.seqs = []uint64{sc.Seq}
		c.states = []probewire.StateChange{sc}
	case KindResults:
		var batch probewire.ResultBatch
		if err := probewire.Decode(data, &batch); err != nil {
			return reply(http.StatusBadRequest, nil, ""), nil
		}
		for i := range batch.Results {
			c.seqs = append(c.seqs, batch.Results[i].Seq)
		}
	}
	f.mu.Lock()
	answer := f.answer
	f.mu.Unlock()
	var raw []byte
	var err error
	if answer != nil {
		raw, err = answer(&c)
	} else {
		top := c.seqs[len(c.seqs)-1]
		raw = reply(http.StatusAccepted, &top, "")
	}
	if raw != nil && err == nil {
		var r UplinkReply
		_ = json.Unmarshal(raw, &r)
		if r.Status < 300 {
			f.mu.Lock()
			f.calls = append(f.calls, c)
			f.mu.Unlock()
		}
	}
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return raw, err
}

func (f *fakeEdge) setAnswer(a func(c *edgeCall) ([]byte, error)) {
	f.mu.Lock()
	f.answer = a
	f.mu.Unlock()
}

// accepted returns the accepted calls.
func (f *fakeEdge) accepted() []edgeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]edgeCall(nil), f.calls...)
}

// waitFor waits until cond holds for the accepted calls.
func (f *fakeEdge) waitFor(t *testing.T, timeout time.Duration, cond func([]edgeCall) bool) []edgeCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		calls := f.accepted()
		if cond(calls) {
			return calls
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached within %s; %d accepted calls", timeout, len(calls))
		}
		select {
		case <-f.notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func countResults(calls []edgeCall) int {
	n := 0
	for _, c := range calls {
		if c.kind == KindResults {
			n += len(c.seqs)
		}
	}
	return n
}

// startUplink runs an uplink of o over the fake edge until the test ends.
func startUplink(t *testing.T, o *Outbox, f *fakeEdge, tune func(u *Uplink)) *Uplink {
	t.Helper()
	u := NewUplink(o, f.send)
	u.Poll = 20 * time.Millisecond
	u.ResultsInterval = 200 * time.Millisecond
	u.SyncRate = 1000
	u.BackoffMin, u.BackoffMax = 10*time.Millisecond, 40*time.Millisecond
	if tune != nil {
		tune(u)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); u.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return u
}

// addResults spools n results from seq on as bundles of per results.
func addResults(t *testing.T, o *Outbox, from uint64, n, per int) {
	t.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(from, uint64(n)))
	for i := range n {
		seq := from + uint64(i)
		r := icmpResultAt(i, seq, time.Now().UTC(), rng)
		if err := o.AddResult(ctx, &r, false); err != nil {
			t.Fatal(err)
		}
		if per > 0 && (i+1)%per == 0 {
			if err := o.Flush(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := o.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func queueLen(t *testing.T, q *buffer.DiskBuffer) int {
	t.Helper()
	n, err := q.Len(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStateChangeSentImmediately covers AK1 and PRB-05: a spooled state
// change goes to .probe.state at once (no batch wait, the poll interval is
// far longer), p95 < 1 s from spooling to sending; the reply's seq
// acknowledges it in the spool.
func TestStateChangeSentImmediately(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	f := newFakeEdge()
	u := startUplink(t, o, f, func(u *Uplink) { u.Poll = 30 * time.Second })
	// The first round of the state queue runs at the start.
	time.Sleep(50 * time.Millisecond)
	ctx := context.Background()
	var lat []time.Duration
	for seq := uint64(1); seq <= 40; seq++ {
		start := time.Now()
		if err := o.EnqueueState(ctx, stateChange(seq, start.UTC())); err != nil {
			t.Fatal(err)
		}
		u.NotifyState()
		calls := f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return len(c) == int(seq) })
		last := calls[len(calls)-1]
		if last.kind != KindState || last.seqs[0] != seq {
			t.Fatalf("call %d = %s %v, want state %d", seq, last.kind, last.seqs, seq)
		}
		lat = append(lat, last.at.Sub(start))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if p95 := lat[len(lat)*95/100-1]; p95 >= time.Second {
		t.Fatalf("p95 from spooling to sending = %s, want < 1 s", p95)
	}
	deadline := time.Now().Add(2 * time.Second)
	for queueLen(t, o.States()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("acknowledged state changes still in the spool")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestResultsBatchedByTimeAndCount covers AK2: a few results wait for the
// results interval; 1,000 results go at once; no message has more than
// 1,000 results or 256 KiB; the results arrive in sequence order with one
// message in flight.
func TestResultsBatchedByTimeAndCount(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	f := newFakeEdge()
	var inFlight, maxInFlight int
	var mu sync.Mutex
	f.setAnswer(func(c *edgeCall) ([]byte, error) {
		mu.Lock()
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		top := c.seqs[len(c.seqs)-1]
		return reply(http.StatusAccepted, &top, ""), nil
	})
	const interval = 600 * time.Millisecond
	startUplink(t, o, f, func(u *Uplink) { u.ResultsInterval = interval })

	start := time.Now()
	addResults(t, o, 1, 5, 0)
	calls := f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return countResults(c) == 5 })
	if waited := calls[0].at.Sub(start); waited < interval-100*time.Millisecond {
		t.Fatalf("5 results sent after %s, want them to wait for the interval %s", waited, interval)
	}

	start = time.Now()
	addResults(t, o, 6, 1000, 0)
	calls = f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return countResults(c) == 1005 })
	if took := calls[len(calls)-1].at.Sub(start); took >= interval-100*time.Millisecond {
		t.Fatalf("1,000 results sent after %s, want at once", took)
	}

	addResults(t, o, 1006, 2500, 100)
	calls = f.waitFor(t, 10*time.Second, func(c []edgeCall) bool { return countResults(c) == 3505 })
	var last uint64
	for _, c := range calls {
		if len(c.seqs) > probewire.MaxBatchResults || c.size > MaxUplinkMessageBytes {
			t.Fatalf("message with %d results and %d bytes", len(c.seqs), c.size)
		}
		for _, s := range c.seqs {
			if s <= last {
				t.Fatalf("seq %d after %d: not in sequence order", s, last)
			}
			last = s
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != 1 {
		t.Fatalf("at most %d messages in flight, want 1", maxInFlight)
	}
}

// TestResyncStateBeforeResultsInSequenceOrder covers AK3, AK6 and PRB-07:
// without the edge everything stays in the spool (no other path); after
// the reconnect every state change goes before the first result, both in
// sequence order.
func TestResyncStateBeforeResultsInSequenceOrder(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	f := newFakeEdge()
	var mu sync.Mutex
	connected := false
	attempts := 0
	f.setAnswer(func(c *edgeCall) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if !connected {
			attempts++
			return nil, ErrNoEdge
		}
		top := c.seqs[len(c.seqs)-1]
		return reply(http.StatusAccepted, &top, ""), nil
	})
	ctx := context.Background()
	for seq := uint64(1); seq <= 20; seq++ {
		if err := o.EnqueueState(ctx, stateChange(seq, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	addResults(t, o, 1, 3000, 150)
	u := startUplink(t, o, f, nil)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	if attempts == 0 {
		t.Error("the uplink did not try the edge")
	}
	connected = true
	mu.Unlock()
	if n := queueLen(t, o.States()); n != 20 {
		t.Fatalf("%d state changes in the spool while disconnected, want 20", n)
	}
	u.NotifyState()
	calls := f.waitFor(t, 10*time.Second, func(c []edgeCall) bool { return countResults(c) == 3000 })
	var states []uint64
	seenResults := false
	var last uint64
	for _, c := range calls {
		switch c.kind {
		case KindState:
			if seenResults {
				t.Fatalf("state change %d after a result message", c.seqs[0])
			}
			states = append(states, c.seqs[0])
		case KindResults:
			seenResults = true
			for _, s := range c.seqs {
				if s <= last {
					t.Fatalf("result seq %d after %d", s, last)
				}
				last = s
			}
		}
	}
	if len(states) != 20 || !sort.SliceIsSorted(states, func(i, j int) bool { return states[i] < states[j] }) {
		t.Fatalf("state changes %v, want 1..20 in order", states)
	}
}

// TestResyncRateLimited covers AK3: a backlog goes at most SyncRate result
// messages per second.
func TestResyncRateLimited(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	addResults(t, o, 1, 6000, 1000)
	f := newFakeEdge()
	const rate = 10
	startUplink(t, o, f, func(u *Uplink) { u.SyncRate = rate })
	calls := f.waitFor(t, 10*time.Second, func(c []edgeCall) bool { return countResults(c) == 6000 })
	if len(calls) < 6 {
		t.Fatalf("%d messages, want at least 6 (1,000 results or 256 KiB each)", len(calls))
	}
	gap := time.Second / rate
	for i := 1; i < len(calls); i++ {
		if d := calls[i].at.Sub(calls[i-1].at); d < gap-10*time.Millisecond {
			t.Fatalf("messages %d and %d %s apart, want at least %s at %d/s", i-1, i, d, gap, rate)
		}
	}
	if DefaultSyncRate != 5 {
		t.Fatalf("DefaultSyncRate = %d, want 5", DefaultSyncRate)
	}
}

// TestRefusedBatchIsQuarantinedAndCounted covers AK4: a 4xx refusal moves
// the entries into the quarantine and counts them (quarantined); 5xx and
// network errors are repeated without loss.
func TestRefusedBatchIsQuarantinedAndCounted(t *testing.T) {
	dir := t.TempDir()
	o := openOutbox(t, dir, 0)
	f := newFakeEdge()
	var mu sync.Mutex
	failures := map[string]int{}
	f.setAnswer(func(c *edgeCall) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case c.kind == KindState && c.seqs[0] == 2:
			return reply(http.StatusUnprocessableEntity, nil, ""), nil
		case c.kind == KindState && c.seqs[0] == 3 && failures["5xx"] < 3:
			failures["5xx"]++
			return reply(http.StatusServiceUnavailable, nil, ""), nil
		case c.kind == KindResults && c.seqs[0] == 1:
			return reply(http.StatusBadRequest, nil, ""), nil
		case c.kind == KindResults && failures["net"] < 2:
			failures["net"]++
			return nil, context.DeadlineExceeded
		}
		top := c.seqs[len(c.seqs)-1]
		return reply(http.StatusAccepted, &top, ""), nil
	})
	ctx := context.Background()
	for seq := uint64(1); seq <= 3; seq++ {
		if err := o.EnqueueState(ctx, stateChange(seq, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	addResults(t, o, 1, 10, 0) // refused bundle
	u := startUplink(t, o, f, nil)
	f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return len(c) == 2 }) // states 1 and 3
	deadline := time.Now().Add(5 * time.Second)
	for queueLen(t, o.Results()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("refused bundle still in the queue")
		}
		time.Sleep(10 * time.Millisecond)
	}
	addResults(t, o, 11, 10, 0) // accepted after two network errors
	f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return countResults(c) == 10 })

	stats, err := o.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Losses[OutboxLossQuarantined]; got != 2 {
		t.Fatalf("quarantined = %d, want 2 (one state change, one bundle)", got)
	}
	if stats.Losses[OutboxLossMaxBytes] != 0 {
		t.Fatalf("max_bytes = %d, want 0", stats.Losses[OutboxLossMaxBytes])
	}
	for _, sub := range []string{OutboxStateDir, OutboxResultsDir} {
		entries, err := os.ReadDir(filepath.Join(dir, sub, buffer.QuarantineDir))
		if err != nil || len(entries) != 1 {
			t.Fatalf("quarantine of %s: %d entries (%v), want 1", sub, len(entries), err)
		}
	}
	if st := u.Stats(); st.Quarantined != 2 || st.Retries < 5 {
		t.Fatalf("uplink stats %+v, want 2 quarantined and at least 5 retries", st)
	}
}

// TestNotLicensedPausesWithoutLoss covers AK4: 403 feature-not-entitled
// pauses the uplink; nothing is sent during the pause, nothing is lost,
// and after the pause one message probes and the data go.
func TestNotLicensedPausesWithoutLoss(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	f := newFakeEdge()
	var mu sync.Mutex
	licensed := false
	var refusals int
	f.setAnswer(func(c *edgeCall) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if !licensed {
			refusals++
			return reply(http.StatusForbidden, nil, ProblemFeatureNotEntitled), nil
		}
		top := c.seqs[len(c.seqs)-1]
		return reply(http.StatusAccepted, &top, ""), nil
	})
	ctx := context.Background()
	if err := o.EnqueueState(ctx, stateChange(1, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	addResults(t, o, 1, 50, 0)
	var paused, resumed int
	var hookMu sync.Mutex
	const pause = 500 * time.Millisecond
	u := startUplink(t, o, f, func(u *Uplink) {
		u.PauseInterval = pause
		u.OnNotLicensed = func() { hookMu.Lock(); paused++; hookMu.Unlock() }
		u.OnLicensed = func() { hookMu.Lock(); resumed++; hookMu.Unlock() }
	})
	time.Sleep(pause / 2)
	mu.Lock()
	if refusals != 1 {
		t.Errorf("%d requests during the pause, want the one refused", refusals)
	}
	licensed = true
	mu.Unlock()
	if !u.Stats().Paused {
		t.Error("uplink not paused")
	}
	if queueLen(t, o.States()) != 1 || queueLen(t, o.Results()) != 1 {
		t.Fatal("data left the spool during the pause")
	}
	f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return len(c) == 2 && countResults(c) == 50 })
	stats, err := o.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if lost := stats.Lost(); lost != 0 {
		t.Fatalf("lost = %d, want 0: a refusal of the feature is no loss", lost)
	}
	hookMu.Lock()
	defer hookMu.Unlock()
	if paused != 1 || resumed == 0 {
		t.Fatalf("pause hooks: not licensed %d, licensed %d", paused, resumed)
	}
}

// TestAckRemovesUpToPersistedSeq covers AK1: the reply's seq acknowledges
// every entry up to it, nothing beyond; the rest is sent again.
func TestAckRemovesUpToPersistedSeq(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	ctx := context.Background()
	for seq := uint64(1); seq <= 3; seq++ {
		if err := o.EnqueueState(ctx, stateChange(seq, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	addResults(t, o, 1, 10, 0)
	addResults(t, o, 11, 10, 0)
	f := newFakeEdge()
	var mu sync.Mutex
	first := true
	f.setAnswer(func(c *edgeCall) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		top := c.seqs[len(c.seqs)-1]
		switch {
		case c.kind == KindState:
			// The server holds the state changes up to 3 already.
			three := uint64(3)
			return reply(http.StatusAccepted, &three, ""), nil
		case first:
			// Only the first bundle (seq 1..10) is persisted.
			first = false
			ten := uint64(10)
			return reply(http.StatusAccepted, &ten, ""), nil
		}
		return reply(http.StatusAccepted, &top, ""), nil
	})
	startUplink(t, o, f, nil)
	calls := f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return len(c) == 3 })
	if calls[0].kind != KindState || calls[0].seqs[0] != 1 {
		t.Fatalf("first call %s %v, want state 1", calls[0].kind, calls[0].seqs)
	}
	if len(calls[1].seqs) != 20 || calls[2].seqs[0] != 11 || len(calls[2].seqs) != 10 {
		t.Fatalf("result messages %v and %v, want 1..20 and then 11..20 again", calls[1].seqs, calls[2].seqs)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.accepted()); n != 3 {
		t.Fatalf("%d calls, want 3: state 2 and 3 acknowledged by the seq of the first reply", n)
	}
	if queueLen(t, o.States()) != 0 || queueLen(t, o.Results()) != 0 {
		t.Fatal("entries left in the spool")
	}
}

// TestRetryableStatusesAreNotQuarantined covers the exceptions of AK4: 401
// (collector disabled), 403 without the problem type feature-not-entitled,
// 408 and 429 (server busy) are repeated with backoff like 5xx; nothing is
// quarantined or counted as loss and every state change arrives.
func TestRetryableStatusesAreNotQuarantined(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	f := newFakeEdge()
	statuses := map[uint64]int{1: http.StatusTooManyRequests, 2: http.StatusUnauthorized,
		3: http.StatusRequestTimeout, 4: http.StatusForbidden}
	var mu sync.Mutex
	refused := map[uint64]int{}
	f.setAnswer(func(c *edgeCall) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		seq := c.seqs[0]
		if s, ok := statuses[seq]; ok && c.kind == KindState && refused[seq] < 2 {
			refused[seq]++
			return reply(s, nil, ""), nil
		}
		top := c.seqs[len(c.seqs)-1]
		return reply(http.StatusAccepted, &top, ""), nil
	})
	ctx := context.Background()
	for seq := uint64(1); seq <= 4; seq++ {
		if err := o.EnqueueState(ctx, stateChange(seq, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	u := startUplink(t, o, f, nil)
	calls := f.waitFor(t, 10*time.Second, func(c []edgeCall) bool { return len(c) == 4 })
	for i, c := range calls {
		if c.kind != KindState || c.seqs[0] != uint64(i+1) {
			t.Fatalf("call %d: %s seq %v, want state seq %d", i, c.kind, c.seqs, i+1)
		}
	}
	stats, err := o.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Losses[OutboxLossQuarantined]; got != 0 {
		t.Fatalf("quarantined = %d, want 0", got)
	}
	if st := u.Stats(); st.Quarantined != 0 || st.Retries < 8 {
		t.Fatalf("uplink stats %+v, want none quarantined and at least 8 retries", st)
	}
}

// TestStateEntryWithoutSeqIsQuarantined: an entry of the state queue
// without a readable seq (another format) cannot be acknowledged; it goes
// to the quarantine instead of blocking the queue, the next entry is sent.
func TestStateEntryWithoutSeqIsQuarantined(t *testing.T) {
	o := openOutbox(t, t.TempDir(), 0)
	f := newFakeEdge()
	ctx := context.Background()
	payload, err := json.Marshal(stateChange(1, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.States().Enqueue(ctx, &buffer.Message{ID: seqID(1), Topic: topicProbeState, Payload: payload,
		CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := o.EnqueueState(ctx, stateChange(2, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	u := startUplink(t, o, f, nil)
	calls := f.waitFor(t, 5*time.Second, func(c []edgeCall) bool { return len(c) >= 1 })
	if calls[0].seqs[0] != 2 {
		t.Fatalf("first accepted state seq %d, want 2", calls[0].seqs[0])
	}
	deadline := time.Now().Add(5 * time.Second)
	for queueLen(t, o.States()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("state queue not empty")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.accepted()); n != 1 {
		t.Fatalf("%d accepted calls, want 1 (no endless repetition)", n)
	}
	if st := u.Stats(); st.Quarantined != 1 {
		t.Fatalf("uplink stats %+v, want 1 quarantined", st)
	}
}
