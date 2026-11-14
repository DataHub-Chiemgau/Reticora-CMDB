package alarm_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// The check consumer on the MemoryStore (ALR-02, MON-04, WP-986): the
// mapping of the probe states, the collector's time and the late, ended
// outage. The PostgreSQL and NATS cases are in
// check_consumer_integration_test.go.

const checkProbe = "11111111-1111-4111-8111-0000000009a6"

// fakeChecks is a CheckReader of fixed views.
type fakeChecks map[string]*probe.CheckView

func (f fakeChecks) ReadCheck(_ context.Context, _, probeID string) (*probe.CheckView, error) {
	v, ok := f[probeID]
	if !ok {
		return nil, nil
	}
	c := *v
	return &c, nil
}

func checkView(state, reason string, since time.Time) *probe.CheckView {
	return &probe.CheckView{ProbeID: checkProbe, Type: "icmp", Target: "10.0.0.1", HasState: true, State: state, Reason: reason,
		Since: since, ReceivedAt: since.Add(time.Second), IntervalS: 30, TimeoutS: 10, DownCount: 5, UpCount: 1,
		Thresholds: map[string]any{"loss_pct": 20}}
}

// stateEvent is a probe.state_changed envelope of the probe.
func stateEvent(t *testing.T, to string, late, notify bool) *events.Envelope {
	t.Helper()
	data, err := json.Marshal(map[string]any{"id": checkProbe, "probe_id": checkProbe, "to": to, "late": late, "notify": notify})
	if err != nil {
		t.Fatal(err)
	}
	return &events.Envelope{ID: "e", Type: events.ProbeStateChanged, OrgID: memOrg, EntityID: checkProbe, Data: data}
}

// checkAlarm returns the alarm of the probe (nil without one).
func checkAlarm(t *testing.T, svc *alarm.Service) *alarm.Alarm {
	t.Helper()
	page, err := svc.Store().List(context.Background(), memOrg, &alarm.ListFilter{SourceKinds: []string{alarm.SourceCheck}, SourceID: checkProbe})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		return nil
	}
	return &page.Items[0]
}

// TestCheckConsumerMapsStates: down → critical, degraded → warning (a
// later down raises it), up → resolved, unknown → unchanged; the details
// carry type, target, reason and the effective configuration.
func TestCheckConsumerMapsStates(t *testing.T) {
	svc, sink := memService()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	checks := fakeChecks{checkProbe: checkView("degraded", "rtt", at)}
	c := alarm.NewCheckConsumer(svc, checks, nil)
	ctx := context.Background()
	if err := c.Handle(ctx, stateEvent(t, "degraded", false, true)); err != nil {
		t.Fatal(err)
	}
	a := checkAlarm(t, svc)
	if a == nil || a.Severity != alarm.SeverityWarning || a.State != alarm.StateOpen || a.DedupKey != "check:"+checkProbe+":state" {
		t.Fatalf("degraded: %+v, want an open warning alarm check:<probe>:state", a)
	}
	if a.Details["probe_type"] != "icmp" || a.Details["target"] != "10.0.0.1" || a.Details["reason"] != "rtt" ||
		a.Details["interval_s"] != int64(30) || a.Details["down_count"] != int64(5) || a.Details["timeout_s"] != int64(10) ||
		a.Details["thresholds"] == nil {
		t.Errorf("details %v", a.Details)
	}
	checks[checkProbe] = checkView("down", "timeout", at.Add(time.Minute))
	if err := c.Handle(ctx, stateEvent(t, "down", false, true)); err != nil {
		t.Fatal(err)
	}
	if a = checkAlarm(t, svc); a.Severity != alarm.SeverityCritical {
		t.Errorf("down after degraded: severity %s, want critical", a.Severity)
	}
	// unknown: the open alarm stays as it is, nothing new.
	checks[checkProbe] = checkView("unknown", "auth_failed", at.Add(2*time.Minute))
	before := len(sink.Events())
	if err := c.Handle(ctx, stateEvent(t, "unknown", false, true)); err != nil {
		t.Fatal(err)
	}
	if a = checkAlarm(t, svc); a.State != alarm.StateOpen || len(sink.Events()) != before {
		t.Errorf("unknown: state %s, %d new events; want the alarm unchanged", a.State, len(sink.Events())-before)
	}
	checks[checkProbe] = checkView("up", "", at.Add(3*time.Minute))
	if err := c.Handle(ctx, stateEvent(t, "up", false, true)); err != nil {
		t.Fatal(err)
	}
	if a = checkAlarm(t, svc); a.State != alarm.StateResolved {
		t.Errorf("up: state %s, want resolved", a.State)
	}
	// unknown without an alarm opens none.
	svc2, _ := memService()
	c2 := alarm.NewCheckConsumer(svc2, fakeChecks{checkProbe: checkView("unknown", "collector_unreachable", at)}, nil)
	if err := c2.Handle(ctx, stateEvent(t, "unknown", false, true)); err != nil {
		t.Fatal(err)
	}
	if a := checkAlarm(t, svc2); a != nil {
		t.Errorf("unknown opened %+v", a)
	}
}

// TestCheckConsumerUsesCollectorTimestamp: opened_at and resolved_at are
// probe_state.since (the collector's time), not the time of the handling.
func TestCheckConsumerUsesCollectorTimestamp(t *testing.T) {
	svc, _ := memService()
	down := time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC)
	checks := fakeChecks{checkProbe: checkView("down", "timeout", down)}
	c := alarm.NewCheckConsumer(svc, checks, nil)
	if err := c.Handle(context.Background(), stateEvent(t, "down", false, true)); err != nil {
		t.Fatal(err)
	}
	a := checkAlarm(t, svc)
	if a == nil || a.OpenedAt == nil || !a.OpenedAt.Equal(down) {
		t.Fatalf("opened_at %v, want the collector's %v", a, down)
	}
	up := down.Add(7 * time.Minute)
	checks[checkProbe] = checkView("up", "", up)
	if err := c.Handle(context.Background(), stateEvent(t, "up", false, true)); err != nil {
		t.Fatal(err)
	}
	if a = checkAlarm(t, svc); a.ResolvedAt == nil || !a.ResolvedAt.Equal(up) {
		t.Errorf("resolved_at %v, want the collector's %v", a.ResolvedAt, up)
	}
}

// TestCheckConsumerLateEndedOutageOpensNothing: a late down whose outage
// has ended (the current state is up, notify false) opens no alarm; a late
// change that is not the current state changes nothing; a down without
// notification opens nothing but counts on an open alarm.
func TestCheckConsumerLateEndedOutageOpensNothing(t *testing.T) {
	svc, sink := memService()
	at := time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC)
	checks := fakeChecks{checkProbe: checkView("up", "", at.Add(time.Minute))}
	c := alarm.NewCheckConsumer(svc, checks, nil)
	ctx := context.Background()
	if err := c.Handle(ctx, stateEvent(t, "down", true, false)); err != nil {
		t.Fatal(err)
	}
	if a := checkAlarm(t, svc); a != nil || len(sink.Events()) != 0 {
		t.Fatalf("late ended outage opened %+v", a)
	}
	// The state is down, but the event notifies not (a replay): no alarm.
	checks[checkProbe] = checkView("down", "timeout", at.Add(2*time.Minute))
	if err := c.Handle(ctx, stateEvent(t, "down", false, false)); err != nil {
		t.Fatal(err)
	}
	if a := checkAlarm(t, svc); a != nil {
		t.Fatalf("a down without notification opened %+v", a)
	}
	// A notifying down opens; a late up that is not the current state
	// leaves it open; a repeated down counts.
	if err := c.Handle(ctx, stateEvent(t, "down", false, true)); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(ctx, stateEvent(t, "up", true, true)); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(ctx, stateEvent(t, "down", false, false)); err != nil {
		t.Fatal(err)
	}
	a := checkAlarm(t, svc)
	if a == nil || a.State != alarm.StateOpen || a.OccurrenceCount != 2 {
		t.Errorf("alarm %+v, want open with two occurrences", a)
	}
}

// TestCheckConsumerSummaryCutsByCharacter: a target of multibyte
// characters longer than the summary is cut at 500 characters, never inside
// a UTF-8 sequence (PostgreSQL refuses an invalid one).
func TestCheckConsumerSummaryCutsByCharacter(t *testing.T) {
	svc, _ := memService()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	v := checkView("down", "timeout", at)
	v.Type, v.Target = "http", "https://example.org/"+strings.Repeat("ä", 700)
	c := alarm.NewCheckConsumer(svc, fakeChecks{checkProbe: v}, nil)
	if err := c.Handle(context.Background(), stateEvent(t, "down", false, true)); err != nil {
		t.Fatal(err)
	}
	a := checkAlarm(t, svc)
	if a == nil {
		t.Fatal("no alarm")
	}
	if !utf8.ValidString(a.Summary) || utf8.RuneCountInString(a.Summary) != alarm.SummaryMaxLen {
		t.Errorf("summary of %d characters (valid UTF-8 %v), want %d", utf8.RuneCountInString(a.Summary), utf8.ValidString(a.Summary),
			alarm.SummaryMaxLen)
	}
	if a.DedupKey != alarm.CheckDedupKey(checkProbe) {
		t.Errorf("dedup_key %s, want %s", a.DedupKey, alarm.CheckDedupKey(checkProbe))
	}
}
