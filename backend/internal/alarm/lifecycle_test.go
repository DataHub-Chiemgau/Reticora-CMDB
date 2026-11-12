package alarm_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The lifecycle of an alarm (ALR-01, WP-982) on the MemoryStore: the
// transition table, severity changes of a raise and the events per state
// change. The PostgreSQL cases are in lifecycle_integration_test.go.

const memOrg = "00000000-0000-4000-8000-0000000000a1"

func memService() (*alarm.Service, *alarm.MemorySink) {
	sink := &alarm.MemorySink{}
	return alarm.NewService(alarm.NewMemoryStore(), nil, sink), sink
}

func raiseIn(series, severity string) *alarm.RaiseInput {
	return &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: ruleID, SeriesKey: series, Severity: severity,
		Summary: "cpu high"}
}

func eventTypes(sink *alarm.MemorySink) []string {
	var out []string
	for _, e := range sink.Events() {
		out = append(out, e.Type)
	}
	return out
}

// TestTransitionMatrixComplete checks every pair of source state (new
// included) and target state against the table of ALR-01, and drives every
// pair through the service: an allowed change succeeds, every other one is
// ErrInvalidTransition and writes nothing.
func TestTransitionMatrixComplete(t *testing.T) {
	allowed := map[[2]string]bool{
		{alarm.StateNew, alarm.StatePending}:           true,
		{alarm.StateNew, alarm.StateOpen}:              true,
		{alarm.StatePending, alarm.StateOpen}:          true,
		{alarm.StatePending, alarm.StateResolved}:      true,
		{alarm.StateOpen, alarm.StateAcknowledged}:     true,
		{alarm.StateAcknowledged, alarm.StateOpen}:     true,
		{alarm.StateOpen, alarm.StateResolved}:         true,
		{alarm.StateAcknowledged, alarm.StateResolved}: true,
	}
	from := append([]string{alarm.StateNew}, alarm.States...)
	for _, f := range from {
		for _, to := range alarm.States {
			if got, want := alarm.CanTransition(f, to), allowed[[2]string{f, to}]; got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", f, to, got, want)
			}
		}
	}

	// The operations of the service on an alarm in each state.
	ctx := context.Background()
	reach := func(t *testing.T, s *alarm.Service, state string) *alarm.Alarm {
		t.Helper()
		var a *alarm.Alarm
		var err error
		switch state {
		case alarm.StatePending:
			a, err = s.Pend(ctx, memOrg, raiseIn("m", alarm.SeverityWarning))
		case alarm.StateOpen:
			a, err = s.Raise(ctx, memOrg, raiseIn("m", alarm.SeverityWarning))
		case alarm.StateAcknowledged:
			if a, err = s.Raise(ctx, memOrg, raiseIn("m", alarm.SeverityWarning)); err == nil {
				a, err = s.Acknowledge(ctx, memOrg, a.ID, "00000000-0000-4000-8000-0000000000f1", "")
			}
		case alarm.StateResolved:
			if a, err = s.Raise(ctx, memOrg, raiseIn("m", alarm.SeverityWarning)); err == nil {
				a, err = s.Clear(ctx, memOrg, a.DedupKey, time.Time{}, "")
			}
		}
		if err != nil || a.State != state {
			t.Fatalf("reach %s: %+v, %v", state, a, err)
		}
		return a
	}
	ops := map[string]func(s *alarm.Service, a *alarm.Alarm) (*alarm.Alarm, error){
		alarm.StateAcknowledged: func(s *alarm.Service, a *alarm.Alarm) (*alarm.Alarm, error) {
			return s.Acknowledge(ctx, memOrg, a.ID, "00000000-0000-4000-8000-0000000000f1", "")
		},
		alarm.StateOpen: func(s *alarm.Service, a *alarm.Alarm) (*alarm.Alarm, error) {
			if a.State == alarm.StatePending {
				return s.Raise(ctx, memOrg, raiseIn("m", alarm.SeverityWarning))
			}
			return s.Unacknowledge(ctx, memOrg, a.ID, "00000000-0000-4000-8000-0000000000f1", "")
		},
		alarm.StateResolved: func(s *alarm.Service, a *alarm.Alarm) (*alarm.Alarm, error) {
			// Clear finds only an alarm that is not resolved; on a resolved
			// one the ack is the operation that must fail.
			if a.State == alarm.StateResolved {
				return s.Acknowledge(ctx, memOrg, a.ID, "00000000-0000-4000-8000-0000000000f1", "")
			}
			return s.Clear(ctx, memOrg, a.DedupKey, time.Time{}, "")
		},
	}
	for _, f := range alarm.States {
		for _, to := range []string{alarm.StateOpen, alarm.StateAcknowledged, alarm.StateResolved} {
			if f == alarm.StateResolved && to != alarm.StateResolved {
				continue // a resolved alarm is never addressed by key again
			}
			s, sink := memService()
			a := reach(t, s, f)
			before, _ := s.Store().Transitions(ctx, memOrg, a.ID)
			eventsBefore := len(sink.Events())
			got, err := ops[to](s, a)
			after, _ := s.Store().Transitions(ctx, memOrg, a.ID)
			want := allowed[[2]string{f, to}]
			switch {
			case want && (err != nil || got.State != to || len(after) != len(before)+1):
				t.Errorf("%s → %s: %+v, %v, %d transitions; want the change with one transition", f, to, got, err, len(after)-len(before))
			case !want && !errors.Is(err, alarm.ErrInvalidTransition):
				t.Errorf("%s → %s: error %v, want ErrInvalidTransition", f, to, err)
			case !want && (len(after) != len(before) || len(sink.Events()) != eventsBefore):
				t.Errorf("%s → %s refused but wrote %d transitions and %d events", f, to,
					len(after)-len(before), len(sink.Events())-eventsBefore)
			}
			if !want {
				if cur, _ := s.Store().Get(ctx, memOrg, a.ID); cur.State != f {
					t.Errorf("%s → %s refused but the alarm is %s", f, to, cur.State)
				}
			}
		}
	}
}

func TestRaiseRaisesSeverityAndReopensAcknowledged(t *testing.T) {
	ctx := context.Background()
	s, sink := memService()
	a, err := s.Raise(ctx, memOrg, raiseIn("eth0", alarm.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	if a, err = s.Acknowledge(ctx, memOrg, a.ID, "00000000-0000-4000-8000-0000000000f1", "on it"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Raise(ctx, memOrg, raiseIn("eth0", alarm.SeverityCritical))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || got.State != alarm.StateOpen || got.Severity != alarm.SeverityCritical ||
		got.AcknowledgedAt != nil || got.AcknowledgedBy != nil || got.OccurrenceCount != 2 {
		t.Errorf("raise critical on acknowledged: %+v; want the alarm open, critical, ack lapsed, count 2", got)
	}
	trs, _ := s.Store().Transitions(ctx, memOrg, a.ID)
	last := trs[len(trs)-1]
	if last.Reason != alarm.ReasonSeverityChanged || *last.FromState != alarm.StateAcknowledged || last.ToState != alarm.StateOpen ||
		*last.FromSeverity != alarm.SeverityWarning || *last.ToSeverity != alarm.SeverityCritical {
		t.Errorf("last transition %+v", last)
	}
	want := []string{events.AlarmOpened, events.AlarmAcknowledged, events.AlarmSeverityChanged}
	if got := eventTypes(sink); !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
	data := sink.Events()[2].Data.(events.AlarmData)
	if data.ID != a.ID || data.AlarmID != a.ID || data.Severity != alarm.SeverityCritical || data.DedupKey != a.DedupKey {
		t.Errorf("event data %+v", data)
	}
}

func TestRaiseLowerSeverityKeepsState(t *testing.T) {
	ctx := context.Background()
	s, sink := memService()
	a, err := s.Raise(ctx, memOrg, raiseIn("eth1", alarm.SeverityCritical))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Acknowledge(ctx, memOrg, a.ID, "00000000-0000-4000-8000-0000000000f1", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.Raise(ctx, memOrg, raiseIn("eth1", alarm.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != alarm.StateAcknowledged || got.Severity != alarm.SeverityCritical || got.OccurrenceCount != 2 {
		t.Errorf("raise warning on acknowledged critical: %+v; want acknowledged, critical, count 2", got)
	}
	trs, _ := s.Store().Transitions(ctx, memOrg, a.ID)
	if len(trs) != 2 {
		t.Errorf("%d transitions, want 2 (opened, acknowledged)", len(trs))
	}
	if want := []string{events.AlarmOpened, events.AlarmAcknowledged}; !slices.Equal(eventTypes(sink), want) {
		t.Errorf("events %v, want %v", eventTypes(sink), want)
	}
}

func TestClearPendingEmitsNoEvent(t *testing.T) {
	ctx := context.Background()
	s, sink := memService()
	a, err := s.Pend(ctx, memOrg, raiseIn("eth2", alarm.SeverityWarning))
	if err != nil || a.State != alarm.StatePending {
		t.Fatalf("pend: %+v, %v", a, err)
	}
	got, err := s.Clear(ctx, memOrg, a.DedupKey, time.Time{}, "recovered")
	if err != nil || got == nil || got.State != alarm.StateResolved {
		t.Fatalf("clear: %+v, %v", got, err)
	}
	if n := len(sink.Events()); n != 0 {
		t.Errorf("%d events for pending → resolved, want none", n)
	}
	trs, _ := s.Store().Transitions(ctx, memOrg, a.ID)
	if len(trs) != 2 || trs[1].Reason != "recovered" || *trs[1].FromState != alarm.StatePending {
		t.Errorf("transitions %+v, want pending and resolved (recovered)", trs)
	}
	// Nothing left to clear.
	if again, err := s.Clear(ctx, memOrg, a.DedupKey, time.Time{}, ""); again != nil || err != nil {
		t.Errorf("second clear: %+v, %v; want nothing", again, err)
	}
	// An opened alarm yields alarm.resolved.
	b, _ := s.Raise(ctx, memOrg, raiseIn("eth3", alarm.SeverityWarning))
	if _, err := s.Clear(ctx, memOrg, b.DedupKey, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	if want := []string{events.AlarmOpened, events.AlarmResolved}; !slices.Equal(eventTypes(sink), want) {
		t.Errorf("events %v, want %v", eventTypes(sink), want)
	}
}

// TestTransitionTextLimits checks the lengths of reason (200) and comment
// (1000) on the MemoryStore as the database checks them: a longer comment
// or reason is ErrInvalidTransitionInput and changes nothing.
func TestTransitionTextLimits(t *testing.T) {
	ctx := context.Background()
	s, sink := memService()
	a, err := s.Raise(ctx, memOrg, raiseIn("eth4", alarm.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Acknowledge(ctx, memOrg, a.ID, "", strings.Repeat("x", alarm.CommentMaxLen+1)); !errors.Is(err, alarm.ErrInvalidTransitionInput) {
		t.Errorf("ack with a comment of 1001 characters: %v, want ErrInvalidTransitionInput", err)
	}
	if _, err = s.Clear(ctx, memOrg, a.DedupKey, time.Time{}, strings.Repeat("r", alarm.ReasonMaxLen+1)); !errors.Is(err, alarm.ErrInvalidTransitionInput) {
		t.Errorf("clear with a reason of 201 characters: %v, want ErrInvalidTransitionInput", err)
	}
	got, err := s.Store().Get(ctx, memOrg, a.ID)
	if err != nil || got.State != alarm.StateOpen {
		t.Errorf("alarm after refused changes: %+v, %v; want open", got, err)
	}
	if trs, _ := s.Store().Transitions(ctx, memOrg, a.ID); len(trs) != 1 {
		t.Errorf("%d transitions, want only the raise", len(trs))
	}
	if n := len(sink.Events()); n != 1 {
		t.Errorf("%d events, want only alarm.opened", n)
	}
	if _, err = s.Acknowledge(ctx, memOrg, a.ID, "", strings.Repeat("ü", alarm.CommentMaxLen)); err != nil {
		t.Errorf("ack with a comment of 1000 characters: %v", err)
	}
	if _, err = s.Store().AppendTransition(ctx, memOrg, &alarm.Transition{AlarmID: a.ID, ToState: alarm.StateOpen,
		Comment: new(strings.Repeat("x", alarm.CommentMaxLen+1))}); !errors.Is(err, alarm.ErrInvalidTransitionInput) {
		t.Errorf("store append with a long comment: %v, want ErrInvalidTransitionInput", err)
	}
}
