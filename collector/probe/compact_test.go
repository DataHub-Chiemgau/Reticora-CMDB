package probe

import (
	"fmt"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-954, PRB-07): cd collector && go test -count=1 -run 'TestCompaction|TestOutbox' ./probe/

var compactBase = time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)

// series returns results of probes every 30 s over minutes, seq ascending.
func series(probes, minutes int) []probewire.Result {
	var out []probewire.Result
	seq := uint64(1)
	for s := 0; s < minutes*60; s += 30 {
		for p := range probes {
			rtt := 1.5
			out = append(out, probewire.Result{ProbeID: fmt.Sprintf("probe-%d", p), Seq: seq, Status: probewire.StatusOK,
				ObservedAt: compactBase.Add(time.Duration(s)*time.Second + time.Duration(p)*time.Millisecond), RTTMS: &rtt})
			seq++
		}
	}
	return out
}

// TestCompactionOneResultPerFiveMinutes covers AK2: per probe one result
// per 5-minute slot, the earliest of the slot, in sequence order.
func TestCompactionOneResultPerFiveMinutes(t *testing.T) {
	in := series(3, 30) // 3 probes, 30 min, every 30 s: 180 results
	kept, dropped := Compact(in, nil, CompactBucket)
	if len(kept) != 3*6 || dropped != len(in)-18 {
		t.Fatalf("kept %d, dropped %d; want 18 (3 probes × 6 slots)", len(kept), dropped)
	}
	seen := map[string]bool{}
	for i, r := range kept {
		if i > 0 && r.Seq <= kept[i-1].Seq {
			t.Fatalf("kept results out of sequence order at %d", i)
		}
		slot := fmt.Sprint(r.ProbeID, r.ObservedAt.Truncate(CompactBucket))
		if seen[slot] {
			t.Fatalf("two results of %s", slot)
		}
		seen[slot] = true
		if r.ObservedAt.Sub(r.ObservedAt.Truncate(CompactBucket)) > time.Second {
			t.Errorf("kept %v, not the earliest result of its slot", r.ObservedAt)
		}
	}
}

// TestCompactionKeepsFailuresAndStateResults covers AK2: every result that
// is no ok and every result of a state change stays.
func TestCompactionKeepsFailuresAndStateResults(t *testing.T) {
	in := series(2, 10) // 40 results
	in[7].Status, in[7].Code = probewire.StatusFail, probewire.ReasonTimeout
	in[9].Status, in[9].Code = probewire.StatusError, probewire.ReasonICMPUnavailable
	related := map[uint64]bool{in[13].Seq: true, in[31].Seq: true}
	kept, dropped := Compact(in, related, CompactBucket)
	has := map[uint64]bool{}
	for _, r := range kept {
		has[r.Seq] = true
	}
	for _, seq := range []uint64{in[7].Seq, in[9].Seq, in[13].Seq, in[31].Seq} {
		if !has[seq] {
			t.Errorf("result %d (failure or state change) was compacted away", seq)
		}
	}
	if len(kept)+dropped != len(in) || dropped == 0 {
		t.Errorf("kept %d + dropped %d != %d", len(kept), dropped, len(in))
	}
}
