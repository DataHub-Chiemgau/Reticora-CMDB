package probe_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-962, PRB-09):
//
//	cd backend && TEST_NATS_URL=nats://127.0.0.1:4222 go test -count=1 -p 1 -run 'TestAlive|TestClockSkew' ./internal/probe/

// aliveAt is an alive message whose collector clock is offset ahead of
// received.
func aliveAt(received time.Time, offset time.Duration) *probewire.Alive {
	return &probewire.Alive{CollectorTime: received.Add(offset), QueueState: probewire.QueueOK,
		ICMPCapability: probewire.ICMPUnprivileged, ICMPCapabilityV6: probewire.ICMPUnprivileged}
}

// skewFeed sends n alive messages of the collector, 10 s apart from start,
// with the collector clock offset ahead; it returns the time after the last.
func skewFeed(t *testing.T, c *probe.ClockSkew, org, collector string, start time.Time, n int, offset time.Duration) time.Time {
	t.Helper()
	at := start
	for range n {
		if err := c.Observe(context.Background(), org, collector, aliveAt(at, offset), at); err != nil {
			t.Fatal(err)
		}
		at = at.Add(10 * time.Second)
	}
	return at
}

// skewEvents returns the data of the collector.clock_skew events of the
// collector, oldest first.
func (e *stateEnv) skewEvents(collector string) []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = 'collector.clock_skew' AND entity_id = $2 ORDER BY sequence`, e.f.OrgA, collector)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// TestAliveWritesLastAliveAndOffset covers AK1 and AK2: every alive message
// writes last_alive_at = received_at; clock_offset_ms is the median of the
// last six offsets (an outlier does not move it), its sign collector −
// server.
func TestAliveWritesLastAliveAndOffset(t *testing.T) {
	e := newStateEnv(t)
	c := probe.NewClockSkew(e.f.App)
	collector := e.collectors[0]
	start := time.Now().UTC().Truncate(time.Millisecond)
	next := skewFeed(t, c, e.f.OrgA, collector, start, 5, 1500*time.Millisecond)
	next = skewFeed(t, c, e.f.OrgA, collector, next, 1, 10*time.Minute) // one outlier
	last := next.Add(-10 * time.Second)
	var alive time.Time
	var offset int32
	var status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT last_alive_at, clock_offset_ms, member_status
		FROM collector_group_member WHERE collector_id = $1`, collector).Scan(&alive, &offset, &status); err != nil {
		t.Fatal(err)
	}
	if !alive.Equal(last) {
		t.Errorf("last_alive_at = %v, want the receipt of the last alive message %v", alive, last)
	}
	if offset != 1500 || status != probe.MemberHealthy {
		t.Errorf("clock_offset_ms = %d, status %s; want the median 1500 and healthy", offset, status)
	}
	// A collector behind the server has a negative offset; an older receipt
	// (another instance) does not move last_alive_at back.
	other := e.collectors[1]
	skewFeed(t, c, e.f.OrgA, other, start, 3, -800*time.Millisecond)
	if err := c.Observe(context.Background(), e.f.OrgA, other, aliveAt(start.Add(-time.Hour), -800*time.Millisecond),
		start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT last_alive_at, clock_offset_ms FROM collector_group_member
		WHERE collector_id = $1`, other).Scan(&alive, &offset); err != nil {
		t.Fatal(err)
	}
	if offset != -800 || !alive.Equal(start.Add(20*time.Second)) {
		t.Errorf("behind: clock_offset_ms = %d, last_alive_at %v; want -800 and %v", offset, alive, start.Add(20*time.Second))
	}
	if got := e.skewEvents(collector); len(got) != 0 {
		t.Errorf("events below 2 s: %v", got)
	}
}

// TestClockSkewDegradesAndEmitsOnce covers AK3: exactly one event on
// crossing 2 s (degraded false), exactly one with degraded true on crossing
// 60 s (the member becomes degraded and stays assigned), healthy again after
// six medians below 30 s and one event resolved true after six below 2 s.
func TestClockSkewDegradesAndEmitsOnce(t *testing.T) {
	e := newStateEnv(t)
	c := probe.NewClockSkew(e.f.App)
	collector := e.collectors[0]
	status := func() string {
		return e.scalar(`SELECT member_status FROM collector_group_member WHERE collector_id = $1`, collector)
	}
	at := time.Now().UTC()
	at = skewFeed(t, c, e.f.OrgA, collector, at, 8, 5*time.Second)
	if got := e.skewEvents(collector); len(got) != 1 || got[0]["degraded"] != false || got[0]["skew_ms"] != float64(5000) ||
		got[0]["collector_group_id"] != e.group || got[0]["id"] != collector {
		t.Fatalf("after crossing 2 s: events %v, want one with degraded false and skew_ms 5000", got)
	}
	if s := status(); s != probe.MemberHealthy {
		t.Fatalf("status %s at 5 s, want healthy", s)
	}
	at = skewFeed(t, c, e.f.OrgA, collector, at, 8, 90*time.Second)
	got := e.skewEvents(collector)
	if len(got) != 2 || got[1]["degraded"] != true || got[1]["skew_ms"] != float64(90000) {
		t.Fatalf("after crossing 60 s: events %v, want a second one with degraded true", got)
	}
	if s := status(); s != probe.MemberDegraded {
		t.Fatalf("status %s at 90 s, want degraded", s)
	}
	at = skewFeed(t, c, e.f.OrgA, collector, at, 10, 10*time.Second)
	if s := status(); s != probe.MemberHealthy {
		t.Fatalf("status %s after six medians below 30 s, want healthy", s)
	}
	if got = e.skewEvents(collector); len(got) != 2 {
		t.Fatalf("below 30 s, above 2 s: events %v, want still two", got)
	}
	skewFeed(t, c, e.f.OrgA, collector, at, 10, 100*time.Millisecond)
	got = e.skewEvents(collector)
	if len(got) != 3 || got[2]["resolved"] != true || got[2]["degraded"] != false {
		t.Fatalf("below 2 s: events %v, want a third one with resolved true", got)
	}
	// A new instance (restart) seeds from the stored values: no second
	// warning for the open state, the smoothing begins anew.
	fresh := probe.NewClockSkew(e.f.App)
	other := e.collectors[1]
	at = skewFeed(t, c, e.f.OrgA, other, time.Now().UTC(), 6, 3*time.Second)
	skewFeed(t, fresh, e.f.OrgA, other, at, 6, 3*time.Second)
	if got := e.skewEvents(other); len(got) != 1 {
		t.Fatalf("restart with an open warning: events %v, want one", got)
	}
}

// TestClockSkewCorrectsObservedAt covers AK4: result and state change of a
// collector degraded with +90 s appear with observed_at − 90 s and
// clock_offset_ms = 90000, the order stays (epoch, seq); a healthy
// collector's rows are unchanged with NULL.
func TestClockSkewCorrectsObservedAt(t *testing.T) {
	e := newStateEnv(t)
	bg := context.Background()
	id, epoch, collector := e.probe("10.96.2.1")
	c := probe.NewClockSkew(e.f.App)
	received := time.Now().UTC().Truncate(time.Millisecond)
	skewFeed(t, c, e.f.OrgA, collector, received.Add(-time.Minute), 6, 90*time.Second)
	if s := e.scalar(`SELECT member_status FROM collector_group_member WHERE collector_id = $1`, collector); s != probe.MemberDegraded {
		t.Fatalf("member status %s, want degraded", s)
	}
	// The collector clock is 90 s ahead: it reports its measurements 90 s
	// in the future.
	measured := received.Add(-5 * time.Second)
	seen := measured.Add(90 * time.Second)
	for i, to := range []probewire.State{probewire.StateDown, probewire.StateUp} {
		from := probewire.StateUnknown
		if i == 1 {
			from = probewire.StateDown
		}
		if err := e.applier.Apply(bg, e.f.OrgA, collector, &probewire.StateChange{ProbeID: id, Epoch: epoch, Seq: uint64(i + 1), //nolint:gosec // 1, 2
			From: from, To: to, Reason: "timeout", ObservedAt: seen.Add(time.Duration(i) * time.Second)}, received); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := e.f.Admin.Query(bg, `SELECT seq, observed_at, clock_offset_ms, late FROM probe_state_change
		WHERE probe_id = $1 ORDER BY epoch, seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var seq int64
		var at time.Time
		var offset *int32
		var late bool
		if err = rows.Scan(&seq, &at, &offset, &late); err != nil {
			t.Fatal(err)
		}
		want := measured.Add(time.Duration(seq-1) * time.Second)
		if !at.Equal(want) || offset == nil || *offset != 90000 || late {
			t.Errorf("change %d: observed_at %v, clock_offset_ms %v, late %v; want %v, 90000, not late", seq, at, offset, late, want)
		}
		n++
	}
	rows.Close()
	if n != 2 {
		t.Fatalf("%d state changes, want 2", n)
	}
	var state string
	var since time.Time
	if err = e.f.Admin.QueryRow(bg, `SELECT state, since FROM probe_state WHERE probe_id = $1`, id).Scan(&state, &since); err != nil {
		t.Fatal(err)
	}
	if state != "up" || !since.Equal(measured.Add(time.Second)) {
		t.Errorf("probe_state %s since %v, want up since %v (corrected, last by seq)", state, since, measured.Add(time.Second))
	}

	out, err := probe.NewPGIngestStore(e.f.App).WriteResults(bg, e.f.OrgA, []probe.ResultsMessage{{
		CollectorID: collector, Results: []probewire.Result{
			{ProbeID: id, Epoch: epoch, Seq: 3, ObservedAt: seen, Status: probewire.StatusOK},
			{ProbeID: id, Epoch: epoch, Seq: 4, ObservedAt: seen.Add(30 * time.Second), Status: probewire.StatusOK},
		}}})
	if err != nil || out.Written != 2 {
		t.Fatalf("write results: %+v, %v", out, err)
	}
	var corrected int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM probe_result WHERE probe_id = $1 AND clock_offset_ms = 90000
		AND ((seq = 3 AND time = $2) OR (seq = 4 AND time = $3))`, id, measured, measured.Add(30*time.Second)).Scan(&corrected); err != nil {
		t.Fatal(err)
	}
	if corrected != 2 {
		t.Errorf("%d results with corrected time and clock_offset_ms 90000, want 2", corrected)
	}

	// The other collector is healthy: no correction, NULL.
	other := e.collectors[0]
	if other == collector {
		other = e.collectors[1]
	}
	if _, err := probe.NewPGIngestStore(e.f.App).WriteResults(bg, e.f.OrgA, []probe.ResultsMessage{{
		CollectorID: other, Results: []probewire.Result{{ProbeID: id, Epoch: epoch, Seq: 5, ObservedAt: measured, Status: probewire.StatusOK}},
	}}); err != nil {
		t.Fatal(err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_result WHERE probe_id = $1 AND seq = 5 AND time = $2 AND clock_offset_ms IS NULL`,
		id, measured); n != "1" {
		t.Errorf("result of the healthy collector: %s rows uncorrected, want 1", n)
	}
}

// TestClockSkewEmitsOnceAcrossInstances covers AK3 with several server
// instances: the alive messages of a collector reach two evaluations in
// turn (queue group); the open warning and the degraded status lie in the
// member row, so each crossing yields exactly one event, and a warning
// resolved through one instance can open again through the other.
func TestClockSkewEmitsOnceAcrossInstances(t *testing.T) {
	e := newStateEnv(t)
	a, b := probe.NewClockSkew(e.f.App), probe.NewClockSkew(e.f.App)
	collector := e.collectors[0]
	at := time.Now().UTC()
	// alternate sends n alive messages, every other one to b.
	alternate := func(n int, offset time.Duration) {
		for i := range n {
			c := a
			if i%2 == 1 {
				c = b
			}
			if err := c.Observe(context.Background(), e.f.OrgA, collector, aliveAt(at, offset), at); err != nil {
				t.Fatal(err)
			}
			at = at.Add(10 * time.Second)
		}
	}
	alternate(4, 500*time.Millisecond) // both instances know the collector
	alternate(12, 5*time.Second)
	alternate(12, 90*time.Second)
	got := e.skewEvents(collector)
	if len(got) != 2 || got[0]["degraded"] != false || got[1]["degraded"] != true {
		t.Fatalf("crossing 2 s and 60 s on two instances: events %v, want one with degraded false and one with degraded true", got)
	}
	if off, _ := got[0]["offset_ms"].(float64); got[0]["collector_id"] != collector || off <= probe.ClockSkewWarnMS ||
		got[0]["skew_ms"] != got[0]["offset_ms"] || got[1]["offset_ms"] != float64(90000) {
		t.Fatalf("event data %v, want collector_id and offset_ms (AK3)", got)
	}
	alternate(24, 100*time.Millisecond)
	if s := e.scalar(`SELECT member_status FROM collector_group_member WHERE collector_id = $1`, collector); s != probe.MemberHealthy {
		t.Fatalf("status %s after the clock came back, want healthy", s)
	}
	if got = e.skewEvents(collector); len(got) != 3 || got[2]["resolved"] != true {
		t.Fatalf("below 2 s on two instances: events %v, want a third one with resolved true", got)
	}
	// Degraded again: the instance that did not resolve sees the row.
	alternate(12, 90*time.Second)
	got = e.skewEvents(collector)
	if len(got) != 5 || got[3]["degraded"] != false || got[4]["degraded"] != true {
		t.Fatalf("second episode: events %v, want a new warning and a new degrade", got)
	}
	if s := e.scalar(`SELECT member_status FROM collector_group_member WHERE collector_id = $1`, collector); s != probe.MemberDegraded {
		t.Fatalf("status %s in the second episode, want degraded", s)
	}
}

// TestClockSkewRepeatedDeliveryWritesOnce covers AK4 with a repeated
// delivery (JetStream redelivery, a repeated upload): the offset of the
// degraded member changed between the deliveries, so the corrected time
// differs; the result is still written once (no second probe_result row,
// no second bridge to metric_sample), and so after the member recovered.
func TestClockSkewRepeatedDeliveryWritesOnce(t *testing.T) {
	e := newStateEnv(t)
	bg := context.Background()
	id, epoch, collector := e.probe("10.96.2.2")
	c := probe.NewClockSkew(e.f.App)
	at := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
	at = skewFeed(t, c, e.f.OrgA, collector, at, 6, 90*time.Second)
	seen := at.Add(90 * time.Second)
	batch := []probe.ResultsMessage{{CollectorID: collector, Results: []probewire.Result{
		{ProbeID: id, Epoch: epoch, Seq: 7, ObservedAt: seen, Status: probewire.StatusOK},
	}}}
	store := probe.NewPGIngestStore(e.f.App)
	if out, err := store.WriteResults(bg, e.f.OrgA, batch); err != nil || out.Written != 1 {
		t.Fatalf("first delivery: %+v, %v", out, err)
	}
	// The median moves to 95 s, the member stays degraded.
	at = skewFeed(t, c, e.f.OrgA, collector, at, 6, 95*time.Second)
	if out, err := store.WriteResults(bg, e.f.OrgA, batch); err != nil || out.Written != 0 || out.Samples != 0 {
		t.Fatalf("repeated delivery under another correction: %+v, %v; want nothing written", out, err)
	}
	// Healthy again: no correction, the repetition is still found.
	skewFeed(t, c, e.f.OrgA, collector, at, 12, 100*time.Millisecond)
	if s := e.scalar(`SELECT member_status FROM collector_group_member WHERE collector_id = $1`, collector); s != probe.MemberHealthy {
		t.Fatalf("status %s, want healthy", s)
	}
	if out, err := store.WriteResults(bg, e.f.OrgA, batch); err != nil || out.Written != 0 {
		t.Fatalf("repeated delivery after the recovery: %+v, %v; want nothing written", out, err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_result WHERE probe_id = $1 AND seq = 7`, id); n != "1" {
		t.Fatalf("%s rows for one result delivered three times, want 1", n)
	}
	// Another result of the same collector is written.
	next := []probe.ResultsMessage{{CollectorID: collector, Results: []probewire.Result{
		{ProbeID: id, Epoch: epoch, Seq: 8, ObservedAt: seen.Add(30 * time.Second), Status: probewire.StatusOK},
	}}}
	if out, err := store.WriteResults(bg, e.f.OrgA, next); err != nil || out.Written != 1 {
		t.Fatalf("next result: %+v, %v", out, err)
	}
}
