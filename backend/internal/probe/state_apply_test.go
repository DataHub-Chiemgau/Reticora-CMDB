package probe

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The rule of the state changes (E-175) and late/notify (E-180), WP-961.

func TestDecideRule(t *testing.T) {
	const a, b = "collector-a", "collector-b"
	cases := []struct {
		name string
		in   ApplyInput
		want Decision
	}{
		{"first change", ApplyInput{Assigned: true, AssignedEpoch: 3, AssignedCollector: a, Epoch: 3, Seq: 1, Collector: a}, DecisionApply},
		{"same epoch, larger seq", ApplyInput{Assigned: true, AssignedEpoch: 3, AssignedCollector: a, HasState: true, StateEpoch: 3, StateSeq: 5,
			Epoch: 3, Seq: 6, Collector: a}, DecisionApply},
		{"same epoch, smaller seq", ApplyInput{Assigned: true, AssignedEpoch: 3, AssignedCollector: a, HasState: true, StateEpoch: 3, StateSeq: 5,
			Epoch: 3, Seq: 4, Collector: a}, DecisionHistory},
		{"new epoch after a collector switch, smaller seq", ApplyInput{Assigned: true, AssignedEpoch: 4, AssignedCollector: b, HasState: true,
			StateEpoch: 3, StateSeq: 900, Epoch: 4, Seq: 1, Collector: b}, DecisionApply},
		{"stale epoch", ApplyInput{Assigned: true, AssignedEpoch: 4, AssignedCollector: b, HasState: true, StateEpoch: 4, StateSeq: 1,
			Epoch: 3, Seq: 901, Collector: a}, DecisionStale},
		{"epoch above the assignment", ApplyInput{Assigned: true, AssignedEpoch: 4, AssignedCollector: a, Epoch: 5, Seq: 1, Collector: a},
			DecisionRefuse},
		{"another collector in the epoch", ApplyInput{Assigned: true, AssignedEpoch: 4, AssignedCollector: a, Epoch: 4, Seq: 1, Collector: b},
			DecisionStale},
		{"no assignment", ApplyInput{Epoch: 1, Seq: 1, Collector: a}, DecisionStale},
	}
	for _, c := range cases {
		if got := Decide(&c.in); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLateAndNotify(t *testing.T) {
	now := time.Now()
	if Late(now.Add(-50*time.Second), now, 30*time.Second) {
		t.Error("50 s with 30 s interval is not late (limit 60 s)")
	}
	if !Late(now.Add(-61*time.Second), now, 30*time.Second) {
		t.Error("61 s with 30 s interval is late")
	}
	if !Notify(false, true) || !Notify(true, false) || Notify(true, true) {
		t.Error("notify: false only for a late change with a successor")
	}
	msgs := []*stateMessage{
		{collector: "c1", change: probewire.StateChange{ProbeID: "p", Epoch: 1, Seq: 1}},
		{collector: "c1", change: probewire.StateChange{ProbeID: "p", Epoch: 1, Seq: 2}},
		{collector: "c1", change: probewire.StateChange{ProbeID: "q", Epoch: 2, Seq: 1}},
		{collector: "c1", change: probewire.StateChange{ProbeID: "q", Epoch: 1, Seq: 9}},
	}
	markFollowed(msgs)
	in := func(epoch int64) *ApplyInput {
		return &ApplyInput{Assigned: true, AssignedEpoch: epoch, AssignedCollector: "c1"}
	}
	if !followedBy(msgs[0].followers, in(1)) || followedBy(msgs[1].followers, in(1)) || followedBy(msgs[2].followers, in(2)) ||
		!followedBy(msgs[3].followers, in(2)) {
		t.Errorf("followers %v %v %v %v", msgs[0].followers, msgs[1].followers, msgs[2].followers, msgs[3].followers)
	}
}

// TestLateWithoutRealSuccessorNotifies: a younger change the rule refuses
// (newer epoch than the assignment) or keeps as stale (another collector)
// is no successor; the late change is the current state and notifies
// (E-180, AK4).
func TestLateWithoutRealSuccessorNotifies(t *testing.T) {
	msgs := []*stateMessage{
		{collector: "c1", change: probewire.StateChange{ProbeID: "p", Epoch: 3, Seq: 5}},
		{collector: "c1", change: probewire.StateChange{ProbeID: "p", Epoch: 4, Seq: 1}},
		{collector: "c2", change: probewire.StateChange{ProbeID: "p", Epoch: 3, Seq: 9}},
	}
	markFollowed(msgs)
	assigned := &ApplyInput{Assigned: true, AssignedEpoch: 3, AssignedCollector: "c1"}
	if len(msgs[0].followers) != 2 || followedBy(msgs[0].followers, assigned) {
		t.Errorf("followers %v: a refused and a stale change count as successor", msgs[0].followers)
	}
	if !Notify(true, followedBy(msgs[0].followers, assigned)) {
		t.Error("the late change without a real successor does not notify")
	}
	if followedBy(msgs[0].followers, &ApplyInput{}) {
		t.Error("without assignment nothing follows")
	}
}

// TestStateLaneStopsProbeAfterFailure: when a change of a probe fails, the
// later changes of that probe in the fetch go back unapplied, the changes
// of other probes go on (AK1).
func TestStateLaneStopsProbeAfterFailure(t *testing.T) {
	const org, collector = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	const p1, p2 = "33333333-3333-4333-8333-333333333331", "33333333-3333-4333-8333-333333333332"
	a := NewStateApplier(nil, StreamConfig{SubjectPrefix: "probe"}, nil)
	var (
		mu      sync.Mutex
		applied []string
	)
	a.write = func(_ context.Context, m *stateMessage) error {
		if m.change.ProbeID == p1 && m.change.Seq == 1 {
			return errors.New("database down")
		}
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, fmt.Sprintf("%s:%d", m.change.ProbeID[len(m.change.ProbeID)-1:], m.change.Seq))
		return nil
	}
	var deliveries []*fakeDelivery
	var msgs []stateDelivery
	for _, c := range []struct {
		probe string
		seq   uint64
	}{{p1, 1}, {p2, 1}, {p1, 2}, {p2, 2}, {p1, 3}} {
		raw, err := probewire.Encode(&probewire.StateChange{ProbeID: c.probe, Epoch: 1, Seq: c.seq, From: probewire.StateUp,
			To: probewire.StateDown, ObservedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		d := &fakeDelivery{subject: "probe.state." + org + "." + collector, data: raw, delivered: 1}
		deliveries = append(deliveries, d)
		msgs = append(msgs, d)
	}
	a.process(context.Background(), msgs)
	slices.Sort(applied)
	if strings.Join(applied, ",") != "2:1,2:2" {
		t.Errorf("applied %v, want only the changes of the other probe", applied)
	}
	for i, acked := range []bool{false, true, false, true, false} {
		if d := deliveries[i]; d.acked != acked || d.naked == acked || d.termed {
			t.Errorf("delivery %d: acked %v naked %v termed %v, want acked %v", i, d.acked, d.naked, d.termed, acked)
		}
	}
}
