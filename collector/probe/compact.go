package probe

import (
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// CompactBucket is the time grid of the compaction of the result spool
// (PRB-07, WP-954 AK2): per probe one result per 5 minutes is kept.
const CompactBucket = 5 * time.Minute

// Compact thins results out for the result spool when it nears its budget
// (PRB-07: measurements are compacted first, state changes are never
// dropped): per probe and bucket (observed_at truncated to bucket) the
// first result is kept, plus every result that is no ok (fail, error) and
// every result whose sequence number is in stateRelated (the last result
// of a state change). The kept results stay in their order (by sequence
// number); dropped is the number of results left out. A bucket of 0 or
// less takes CompactBucket.
func Compact(results []probewire.Result, stateRelated map[uint64]bool, bucket time.Duration) (kept []probewire.Result, dropped int) {
	if bucket <= 0 {
		bucket = CompactBucket
	}
	ordered := append([]probewire.Result(nil), results...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	type slot struct {
		probe string
		start int64
	}
	// The first result of a slot is the earliest observed one.
	first := map[slot]int{}
	for i := range ordered {
		r := &ordered[i]
		if r.Status != probewire.StatusOK || stateRelated[r.Seq] {
			continue
		}
		s := slot{probe: r.ProbeID, start: r.ObservedAt.Truncate(bucket).UnixNano()}
		if j, ok := first[s]; !ok || r.ObservedAt.Before(ordered[j].ObservedAt) {
			first[s] = i
		}
	}
	keep := make(map[int]bool, len(first))
	for _, i := range first {
		keep[i] = true
	}
	kept = make([]probewire.Result, 0, len(first))
	for i := range ordered {
		r := &ordered[i]
		if keep[i] || r.Status != probewire.StatusOK || stateRelated[r.Seq] {
			kept = append(kept, *r)
		}
	}
	return kept, len(ordered) - len(kept)
}
