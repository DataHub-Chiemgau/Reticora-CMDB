package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// openOutbox opens a probe spool in a temporary directory that checks
// its budget on every flush.
func openOutbox(t *testing.T, dir string, maxBytes int64) *Outbox {
	t.Helper()
	o, err := OpenOutbox(dir, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	o.checkEvery = 0
	return o
}

func stateChange(seq uint64, observed time.Time) *probewire.StateChange {
	return &probewire.StateChange{Header: probewire.Header{SchemaVersion: probewire.SchemaVersion}, ProbeID: "probe-1", Epoch: 1,
		Seq: seq, From: probewire.StateUp, To: probewire.StateDown, Reason: probewire.ReasonTimeout, ObservedAt: observed}
}

// icmpResultAt is a realistic icmp result of a probe with a UUID.
func icmpResultAt(probe int, seq uint64, at time.Time, rng *rand.Rand) probewire.Result {
	rtt, lo, hi, jitter, loss := 1+rng.Float64()*40, 0.8+rng.Float64(), 2+rng.Float64()*60, rng.Float64()*5, 0.0
	return probewire.Result{ProbeID: fmt.Sprintf("8f1d%04x-3c2b-4e5f-9a7b-%012x", probe%65536, rng.Uint64()%(1<<40)), Epoch: 3, Seq: seq,
		ObservedAt: at, Status: probewire.StatusOK, RTTMS: &rtt, Loss: &loss,
		Values: map[string]float64{ValueRTTMinMS: lo, ValueRTTMaxMS: hi, ValueJitterMS: jitter}}
}

// readResults peeks every bundle and returns the results in order.
func readResults(t *testing.T, o *Outbox) []probewire.Result {
	t.Helper()
	msgs, err := o.Results().PeekBatch(context.Background(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var out []probewire.Result
	for i := range msgs {
		batch, _, err := DecodeBundle(&msgs[i])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, batch.Results...)
	}
	return out
}

// TestOutboxSurvivesRestart covers AK1 and PRB-07: state changes are on
// disk when EnqueueState returns, bundles after Flush; a new outbox on the
// same directory delivers both, and the loss counts survive.
func TestOutboxSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	o := openOutbox(t, dir, 0)
	for seq := uint64(1); seq <= 3; seq++ {
		if err := o.EnqueueState(ctx, stateChange(seq, compactBase.Add(time.Duration(seq)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for seq := uint64(1); seq <= 10; seq++ {
		r := icmpResultAt(int(seq), seq, compactBase.Add(time.Duration(seq)*time.Second), rng)
		if err := o.AddResult(ctx, &r, seq == 4); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	o.count(OutboxLossQuarantined, 2)

	again := openOutbox(t, dir, 0)
	states, err := again.States().PeekBatch(ctx, 10)
	if err != nil || len(states) != 3 {
		t.Fatalf("states after the restart: %d, %v", len(states), err)
	}
	results := readResults(t, again)
	if len(results) != 10 {
		t.Fatalf("results after the restart: %d, want 10", len(results))
	}
	msgs, _ := again.Results().PeekBatch(ctx, 10)
	b := msgs[0]
	if b.Metadata[MetaSeqFrom] != "1" || b.Metadata[MetaSeqTo] != "10" || !b.CreatedAt.Equal(results[0].ObservedAt) {
		t.Errorf("bundle metadata %v, created %v", b.Metadata, b.CreatedAt)
	}
	if _, related, _ := DecodeBundle(&b); !related[4] || len(related) != 1 {
		t.Errorf("state related results %v, want {4}", related)
	}
	if st, _ := again.Stats(ctx); st.Losses[OutboxLossQuarantined] != 2 || st.StateMessages != 3 || st.ResultBundles != 1 {
		t.Errorf("stats after the restart: %+v", st)
	}
}

// TestOutboxKeepsSequenceOrder: entries are delivered in sequence order,
// whatever the observed_at; bundling happens at 1,000 results.
func TestOutboxKeepsSequenceOrder(t *testing.T) {
	ctx := context.Background()
	o := openOutbox(t, t.TempDir(), 0)
	rng := rand.New(rand.NewPCG(3, 4))
	for seq := uint64(1); seq <= 2500; seq++ {
		// observed_at runs backwards: a long check observed earlier.
		r := icmpResultAt(int(seq%50), seq, compactBase.Add(-time.Duration(seq)*time.Second), rng)
		if err := o.AddResult(ctx, &r, false); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := o.Stats(ctx); st.ResultBundles != 2 || st.Pending != 500 {
		t.Errorf("after 2,500 results: %d bundles, %d pending; want 2 bundles of 1,000 and 500 pending", st.ResultBundles, st.Pending)
	}
	if err := o.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for i, r := range readResults(t, o) {
		if r.Seq != uint64(i+1) {
			t.Fatalf("result %d has seq %d: out of sequence order", i, r.Seq)
		}
	}
	for _, seq := range []uint64{9, 3, 12} {
		if err := o.EnqueueState(ctx, stateChange(seq, compactBase.Add(-time.Duration(seq)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	states, _ := o.States().PeekBatch(ctx, 10)
	var order []string
	for _, m := range states {
		order = append(order, m.Metadata[MetaSeqFrom])
	}
	if fmt.Sprint(order) != "[3 9 12]" {
		t.Errorf("state order %v, want [3 9 12]", order)
	}
}

// TestOutboxStateNeverEvictedWithin24h covers AK2: at the state budget a
// new state change is refused (ErrSpoolFull), none younger than 24 h is
// displaced; older ones make room and count as max_bytes.
func TestOutboxStateNeverEvictedWithin24h(t *testing.T) {
	ctx := context.Background()
	o := openOutbox(t, t.TempDir(), 40_000) // state budget 4,000 bytes
	stored := 0
	var err error
	for seq := uint64(1); seq < 100; seq++ {
		if err = o.EnqueueState(ctx, stateChange(seq, time.Now())); err != nil {
			break
		}
		stored++
	}
	if !errors.Is(err, buffer.ErrSpoolFull) || stored == 0 {
		t.Fatalf("after %d state changes: %v, want ErrSpoolFull", stored, err)
	}
	if st, _ := o.Stats(ctx); st.StateMessages != stored {
		t.Fatalf("%d state changes kept, want all %d", st.StateMessages, stored)
	}
	// Older than 24 h: displaced to make room, counted.
	entries, _ := os.ReadDir(o.States().Dir)
	old := time.Now().Add(-25 * time.Hour)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			_ = os.Chtimes(filepath.Join(o.States().Dir, e.Name()), old, old)
			break
		}
	}
	before, _ := o.Stats(ctx)
	if err := o.EnqueueState(ctx, stateChange(500, time.Now())); err != nil {
		t.Fatalf("with an entry older than 24 h: %v", err)
	}
	after, _ := o.Stats(ctx)
	if after.Losses[OutboxLossMaxBytes] != before.Losses[OutboxLossMaxBytes]+1 {
		t.Errorf("max_bytes %d -> %d, want + 1 for the displaced state change", before.Losses[OutboxLossMaxBytes], after.Losses[OutboxLossMaxBytes])
	}
}

// TestOutboxBudgetFor24Hours covers AK4: 24 h × 170 results/s ≈ 14.7
// million results fit with gzip per bundle into the result share of the
// default budget (bundles of one second, as written by Run).
func TestOutboxBudgetFor24Hours(t *testing.T) {
	const perSecond, seconds = 170, 24 * 3600
	if perSecond*seconds < 14_600_000 {
		t.Fatal("the calculation must cover 14.7 million results")
	}
	ctx := context.Background()
	o := openOutbox(t, t.TempDir(), 0)
	rng := rand.New(rand.NewPCG(5, 6))
	const sample = 30 // seconds
	seq := uint64(0)
	for s := range sample {
		for p := range perSecond {
			seq++
			r := icmpResultAt(p+s*perSecond%5000, seq, compactBase.Add(time.Duration(s)*time.Second), rng)
			if err := o.AddResult(ctx, &r, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	st, err := o.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	perBundle := float64(st.ResultBytes) / sample
	day := perBundle * seconds
	budget := float64(DefaultOutboxMaxBytes - DefaultOutboxMaxBytes/outboxStateShare)
	t.Logf("%.0f bytes per bundle of %d results (%.1f bytes per result); 24 h: %.2f GiB of %.2f GiB",
		perBundle, perSecond, perBundle/perSecond, day/(1<<30), budget/(1<<30))
	if day > budget {
		t.Errorf("24 h of results need %.0f bytes, the result budget is %.0f", day, budget)
	}
}

// TestOutboxCrashDuringWrite covers AK3: a write torn by a crash (the
// temporary file of DiskBuffer) leaves no entry; a damaged entry is
// quarantined and counted, the others are delivered.
func TestOutboxCrashDuringWrite(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	o := openOutbox(t, dir, 0)
	if err := o.EnqueueState(ctx, stateChange(1, compactBase)); err != nil {
		t.Fatal(err)
	}
	// The crash: a half written temporary file, and a torn entry.
	if err := os.WriteFile(filepath.Join(o.States().Dir, ".tmp-crash"), []byte(`{"id":"0000`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.States().Dir, seqID(2)+".json"), []byte(`{"id":"000`), 0o600); err != nil {
		t.Fatal(err)
	}
	again := openOutbox(t, dir, 0)
	states, err := again.States().PeekBatch(ctx, 10)
	if err != nil || len(states) != 1 || states[0].ID != seqID(1) {
		t.Fatalf("after the crash: %+v, %v; want the complete entry only", states, err)
	}
	if st, _ := again.Stats(ctx); st.Losses[OutboxLossQuarantined] != 1 {
		t.Errorf("losses %v, want the torn entry quarantined", st.Losses)
	}
}

// TestOutboxCountsLossByReason covers AK2 and AK5: compacted results
// count as compacted (no loss), a bundle refused at the limit with its
// results as max_bytes, a damaged entry as quarantined.
func TestOutboxCountsLossByReason(t *testing.T) {
	ctx := context.Background()
	o := openOutbox(t, t.TempDir(), 150_000) // result budget 135,000 bytes
	rng := rand.New(rand.NewPCG(7, 8))
	seq := uint64(0)
	// 10 probes every 10 s over 30 min, one bundle per 10 s.
	for s := 0; s < 30*60; s += 10 {
		for p := range 10 {
			seq++
			r := icmpResultAt(p, seq, compactBase.Add(time.Duration(s)*time.Second), rng)
			r.ProbeID = fmt.Sprintf("probe-%d", p)
			if err := o.AddResult(ctx, &r, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Flush(ctx); err != nil && !errors.Is(err, buffer.ErrSpoolFull) {
			t.Fatal(err)
		}
	}
	st, _ := o.Stats(ctx)
	if st.Losses[OutboxLossCompacted] == 0 {
		t.Fatalf("losses %v, want compacted results at 80 %% of the budget", st.Losses)
	}
	if st.ResultBytes > 135_000 {
		t.Errorf("result queue %d bytes above its budget", st.ResultBytes)
	}
	if lost := st.Lost(); lost != st.Losses[OutboxLossMaxBytes]+st.Losses[OutboxLossQuarantined] {
		t.Errorf("Lost() = %d counts compacted results", lost)
	}
	// The limit: a budget too small for one more bundle.
	tiny := openOutbox(t, t.TempDir(), 2_000)
	var refused int
	for i := range 20 {
		r := icmpResultAt(i, uint64(i+1), compactBase, rng)
		_ = tiny.AddResult(ctx, &r, false)
		if err := tiny.Flush(ctx); errors.Is(err, buffer.ErrSpoolFull) {
			refused++
		}
	}
	if ts, _ := tiny.Stats(ctx); refused == 0 || ts.Losses[OutboxLossMaxBytes] != int64(refused) {
		t.Errorf("refused %d bundles, max_bytes %v", refused, ts.Losses)
	}
	// A damaged bundle is quarantined.
	if err := os.WriteFile(filepath.Join(o.Results().Dir, "00000000000000000000.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Results().PeekBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if after, _ := o.Stats(ctx); after.Losses[OutboxLossQuarantined] != 1 {
		t.Errorf("losses %v, want one quarantined entry", after.Losses)
	}
}

// TestOutboxCompactionPassesOverIncompressibleBundles covers AK2 (PRB-07:
// compact first, then drop): when the oldest window holds nothing to leave
// out (a site outage: only failures), the compaction goes past it and
// thins out the bundles behind it instead of refusing new bundles at the
// limit.
func TestOutboxCompactionPassesOverIncompressibleBundles(t *testing.T) {
	ctx := context.Background()
	o := openOutbox(t, t.TempDir(), 120_000)
	o.compactWindow = 60
	rng := rand.New(rand.NewPCG(9, 10))
	seq := uint64(0)
	at := compactBase
	// More failure bundles than one compaction window, one result each.
	for range o.compactWindow + 20 {
		seq++
		at = at.Add(time.Second)
		r := icmpResultAt(1, seq, at, rng)
		r.ProbeID, r.Status = "probe-down", probewire.StatusFail
		if err := o.AddResult(ctx, &r, false); err != nil {
			t.Fatal(err)
		}
		if err := o.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Then compressible bundles: 10 probes every second, more than the
	// rest of the budget holds uncompacted.
	for range 300 {
		at = at.Add(time.Second)
		for p := range 10 {
			seq++
			r := icmpResultAt(p, seq, at, rng)
			r.ProbeID = fmt.Sprintf("probe-%d", p)
			if err := o.AddResult(ctx, &r, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Flush(ctx); err != nil && !errors.Is(err, buffer.ErrSpoolFull) {
			t.Fatal(err)
		}
	}
	st, err := o.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Losses[OutboxLossCompacted] == 0 || st.Losses[OutboxLossMaxBytes] != 0 {
		t.Errorf("losses %v, want compacted results and nothing refused at the limit", st.Losses)
	}
	// Every failure is still there.
	fails := 0
	for _, r := range readResults(t, o) {
		if r.Status == probewire.StatusFail {
			fails++
		}
	}
	if fails != o.compactWindow+20 {
		t.Errorf("%d failures in the spool, want %d", fails, o.compactWindow+20)
	}
}

// TestOutboxCompactionReadsBehindItsCursor: bundles the compaction went
// past are not read again; a compacted head longer than any window does
// not stop the compaction of the bundles behind it.
func TestOutboxCompactionReadsBehindItsCursor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	o := openOutbox(t, dir, 0)
	o.compactWindow = 60
	rng := rand.New(rand.NewPCG(11, 12))
	at := compactBase
	seq := uint64(0)
	// A compacted head of more bundles than 4 windows (written as
	// compacted parts directly), then uncompacted bundles of 10 probes.
	for range 4*o.compactWindow + 10 {
		seq++
		at = at.Add(time.Second)
		r := icmpResultAt(1, seq, at, rng)
		msg, err := o.bundle([]probewire.Result{r}, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		// Written directly: Enqueue would scan the growing queue each time.
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o.results.Dir, msg.ID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 60 {
		at = at.Add(time.Second)
		for p := range 10 {
			seq++
			r := icmpResultAt(p, seq, at, rng)
			r.ProbeID = fmt.Sprintf("probe-%d", p)
			if err := o.AddResult(ctx, &r, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := o.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	o.mu.Lock()
	for {
		progressed, err := o.compactOnce(ctx)
		if err != nil {
			o.mu.Unlock()
			t.Fatal(err)
		}
		if !progressed {
			break
		}
	}
	cursor := o.compacted
	o.mu.Unlock()
	st, _ := o.Stats(ctx)
	if st.Losses[OutboxLossCompacted] == 0 {
		t.Errorf("losses %v, want the bundles behind the compacted head compacted", st.Losses)
	}
	// Behind the cursor only the last part waits for the bundles to come.
	if rest, err := o.results.PeekAfter(ctx, cursor, 10); err != nil || len(rest) > 1 {
		t.Errorf("%d bundles after the cursor %s (%v), want at most the last part", len(rest), cursor, err)
	}
}
