package alarm_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
)

// Suppression in maintenance windows on the MemoryStore with a fake clock
// (ALR-11, WP-1033): the alarm of a CI the window certainly affects opens
// suppressed and reaches nobody, a heuristically affected one is only
// marked, and the end of the window releases the open alarms.

const (
	maintWindow = "33333333-3333-4333-8333-0000000010a3"
	routerCI    = "22222222-2222-4222-8222-000000001001"
	cpeCI       = "22222222-2222-4222-8222-000000001002"
	monitorCI   = "22222222-2222-4222-8222-000000001003"
)

// fakeWindows is a MaintenanceChecker: the router is in the window, the
// CPE behind it certainly affected, the monitor heuristically, all only
// while the window is active.
type fakeWindows struct{ from, to time.Time }

func (f fakeWindows) MaintenanceOf(_ context.Context, _, ciID string, at time.Time) (alarm.MaintenanceHit, error) {
	if at.Before(f.from) || !at.Before(f.to) {
		return alarm.MaintenanceHit{}, nil
	}
	switch ciID {
	case routerCI, cpeCI:
		return alarm.MaintenanceHit{WindowID: maintWindow}, nil
	case monitorCI:
		return alarm.MaintenanceHit{WindowID: maintWindow, Heuristic: true}, nil
	}
	return alarm.MaintenanceHit{}, nil
}

func maintService(t *testing.T, start time.Time) (*alarm.Service, *alarm.MemorySink, *time.Time) {
	t.Helper()
	now := start
	sink := &alarm.MemorySink{}
	svc := alarm.NewService(alarm.NewMemoryStore(), nil, sink).WithClock(func() time.Time { return now }).
		WithMaintenance(fakeWindows{from: start.Add(-time.Hour), to: start.Add(time.Hour)})
	return svc, sink, &now
}

func ciRaise(ciID, series string) *alarm.RaiseInput {
	id := ciID
	return &alarm.RaiseInput{SourceKind: alarm.SourceCheck, SourceID: "11111111-1111-4111-8111-0000000010a3", SeriesKey: series,
		CIID: &id, Severity: alarm.SeverityCritical, Summary: "icmp down"}
}

// delivered reports whether an event reaches anybody (the audience of the
// notification chain, WP-987).
func delivered(t *testing.T, e *events.Event) bool {
	t.Helper()
	raw, err := json.Marshal(e.Data)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	sub, ok := notify.Subscriptions[e.Type]
	return ok && sub.Audience != nil && sub.Audience(d) != nil
}

// TestMaintenanceSuppressesCertainChild: the alarm of the CPE behind the
// router of the window opens with suppressed_reason maintenance and
// details.maintenance_window_id; its alarm.opened reaches nobody. After the
// window an alarm of the CPE is not suppressed.
func TestMaintenanceSuppressesCertainChild(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	svc, sink, now := maintService(t, start)
	a, err := svc.Raise(context.Background(), memOrg, ciRaise(cpeCI, "cpe"))
	if err != nil {
		t.Fatal(err)
	}
	if a.SuppressedReason == nil || *a.SuppressedReason != alarm.SuppressedMaintenance || a.Details["maintenance_window_id"] != maintWindow {
		t.Fatalf("alarm %+v, want suppressed maintenance with the window", a)
	}
	evs := sink.Events()
	if len(evs) != 1 || evs[0].Type != events.AlarmOpened || delivered(t, &evs[0]) {
		t.Fatalf("events %v: want one alarm.opened that reaches nobody", evs)
	}
	*now = start.Add(2 * time.Hour)
	b, err := svc.Raise(context.Background(), memOrg, ciRaise(cpeCI, "cpe-later"))
	if err != nil {
		t.Fatal(err)
	}
	if b.SuppressedReason != nil {
		t.Errorf("alarm after the window suppressed: %v", *b.SuppressedReason)
	}
	if last := sink.Events(); !delivered(t, &last[len(last)-1]) {
		t.Error("the alarm after the window reaches nobody")
	}
}

// TestMaintenanceHeuristicNotSuppressed: the monitor reached only over
// connected_to is marked maintenance_heuristic and notified as usual.
func TestMaintenanceHeuristicNotSuppressed(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	svc, sink, _ := maintService(t, start)
	a, err := svc.Raise(context.Background(), memOrg, ciRaise(monitorCI, "monitor"))
	if err != nil {
		t.Fatal(err)
	}
	if a.SuppressedReason != nil || a.Details["maintenance_heuristic"] != true || a.Details["maintenance_window_id"] != nil {
		t.Fatalf("alarm %+v, want unsuppressed with maintenance_heuristic", a)
	}
	if evs := sink.Events(); len(evs) != 1 || !delivered(t, &evs[0]) {
		t.Errorf("the heuristic alarm reaches nobody: %v", evs)
	}
}

// TestMaintenanceNotifyAfterEnd: an alarm still open at the end of the
// window loses its suppression and is notified with alarm.opened
// after_maintenance = true; with notify_after_maintenance false it only
// loses the suppression. A resolved alarm stays as it is.
func TestMaintenanceNotifyAfterEnd(t *testing.T) {
	for _, notifyAfter := range []bool{true, false} {
		start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
		svc, sink, now := maintService(t, start)
		ctx := context.Background()
		if !notifyAfter {
			s := alarm.Defaults()
			s.NotifyAfterMaintenance = false
			if _, err := svc.Store().PutSettings(ctx, memOrg, &s, nil); err != nil {
				t.Fatal(err)
			}
		}
		open, err := svc.Raise(ctx, memOrg, ciRaise(routerCI, "router"))
		if err != nil {
			t.Fatal(err)
		}
		gone, err := svc.Raise(ctx, memOrg, ciRaise(cpeCI, "cpe"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Clear(ctx, memOrg, gone.DedupKey, start.Add(10*time.Minute), ""); err != nil {
			t.Fatal(err)
		}
		sink.Reset()
		*now = start.Add(time.Hour)
		n, err := svc.ReleaseMaintenance(ctx, memOrg, maintWindow)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("notify_after_maintenance %v: %d alarms released, want the open one", notifyAfter, n)
		}
		a, err := svc.Store().Get(ctx, memOrg, open.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a.SuppressedReason != nil || a.State != alarm.StateOpen {
			t.Errorf("released alarm %+v, want open without suppression", a)
		}
		evs := sink.Events()
		switch {
		case notifyAfter && (len(evs) != 1 || evs[0].Type != events.AlarmOpened || !delivered(t, &evs[0])):
			t.Errorf("after the window: events %v, want one delivered alarm.opened", evs)
		case notifyAfter:
			if d, _ := evs[0].Data.(events.AlarmData); !d.AfterMaintenance {
				t.Errorf("alarm.opened without after_maintenance: %+v", d)
			}
		case !notifyAfter && len(evs) != 0:
			t.Errorf("notify_after_maintenance false: events %v, want none", evs)
		}
		// A second release finds nothing more.
		if n, err := svc.ReleaseMaintenance(ctx, memOrg, maintWindow); err != nil || n != 0 {
			t.Errorf("second release: %d, %v; want nothing", n, err)
		}
	}
}

// TestMaintenanceLateObservationNotSuppressed: an observation inside the
// window that is raised only after the window ended (a buffered collector,
// its window already released) opens unsuppressed and is notified.
func TestMaintenanceLateObservationNotSuppressed(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	svc, sink, now := maintService(t, start)
	*now = start.Add(2 * time.Hour)
	in := ciRaise(cpeCI, "cpe-late")
	in.ObservedAt = start.Add(30 * time.Minute)
	a, err := svc.Raise(context.Background(), memOrg, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.SuppressedReason != nil || a.Details["maintenance_window_id"] != nil {
		t.Errorf("late observation %+v, want unsuppressed", a)
	}
	if evs := sink.Events(); len(evs) != 1 || !delivered(t, &evs[0]) {
		t.Errorf("late observation: events %v, want one delivered alarm.opened", evs)
	}
}

// TestMaintenanceEscalationKeepsWindow: a suppressed alarm escalated by an
// observation without window (raised after the end, before the release)
// keeps its window in the details, so the release of the window finds it.
func TestMaintenanceEscalationKeepsWindow(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	svc, _, now := maintService(t, start)
	ctx := context.Background()
	in := ciRaise(cpeCI, "cpe")
	in.Severity = alarm.SeverityWarning
	if _, err := svc.Raise(ctx, memOrg, in); err != nil {
		t.Fatal(err)
	}
	*now = start.Add(time.Hour + time.Second)
	a, err := svc.Raise(ctx, memOrg, ciRaise(cpeCI, "cpe"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Severity != alarm.SeverityCritical || deref(a.SuppressedReason) != alarm.SuppressedMaintenance || a.Details["maintenance_window_id"] != maintWindow {
		t.Fatalf("escalated alarm %+v, want critical, still suppressed with its window", a)
	}
	if n, err := svc.ReleaseMaintenance(ctx, memOrg, maintWindow); err != nil || n != 1 {
		t.Errorf("release of the window: %d, %v; want the escalated alarm", n, err)
	}
}

// overlapWindows holds the CPE in window A until start+1h and in window B
// until start+2h.
type overlapWindows struct{ start time.Time }

const otherWindow = "33333333-3333-4333-8333-0000000010b3"

func (o overlapWindows) MaintenanceOf(_ context.Context, _, ciID string, at time.Time) (alarm.MaintenanceHit, error) {
	if ciID != cpeCI || at.Before(o.start.Add(-time.Hour)) {
		return alarm.MaintenanceHit{}, nil
	}
	switch {
	case at.Before(o.start.Add(time.Hour)):
		return alarm.MaintenanceHit{WindowID: maintWindow}, nil
	case at.Before(o.start.Add(2 * time.Hour)):
		return alarm.MaintenanceHit{WindowID: otherWindow}, nil
	}
	return alarm.MaintenanceHit{}, nil
}

// TestMaintenanceOverlappingWindowsKeepSuppression: at the end of window A
// an alarm whose CI window B still holds stays suppressed and moves to B,
// without an event; the end of B releases it.
func TestMaintenanceOverlappingWindowsKeepSuppression(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	now := start
	sink := &alarm.MemorySink{}
	svc := alarm.NewService(alarm.NewMemoryStore(), nil, sink).WithClock(func() time.Time { return now }).
		WithMaintenance(overlapWindows{start: start})
	ctx := context.Background()
	a, err := svc.Raise(ctx, memOrg, ciRaise(cpeCI, "cpe"))
	if err != nil {
		t.Fatal(err)
	}
	sink.Reset()
	now = start.Add(90 * time.Minute)
	if n, rerr := svc.ReleaseMaintenance(ctx, memOrg, maintWindow); rerr != nil || n != 0 {
		t.Fatalf("end of A: %d released (%v), want none", n, rerr)
	}
	if a, err = svc.Store().Get(ctx, memOrg, a.ID); err != nil {
		t.Fatal(err)
	}
	if deref(a.SuppressedReason) != alarm.SuppressedMaintenance || a.Details["maintenance_window_id"] != otherWindow {
		t.Errorf("after the end of A: %+v, want suppressed for window B", a)
	}
	if evs := sink.Events(); len(evs) != 0 {
		t.Errorf("end of A: events %v, want none", evs)
	}
	now = start.Add(3 * time.Hour)
	if n, err := svc.ReleaseMaintenance(ctx, memOrg, otherWindow); err != nil || n != 1 {
		t.Errorf("end of B: %d released (%v), want the alarm", n, err)
	}
}

// TestMaintenanceStaleSuppressionReleased: an alarm suppressed for a
// window that is no longer active without its end being seen (cancelled or
// deleted while active) is released by ReleaseStaleMaintenance; one whose
// window is still active stays.
func TestMaintenanceStaleSuppressionReleased(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	now := start
	sink := &alarm.MemorySink{}
	checker := &switchableWindows{fakeWindows: fakeWindows{from: start.Add(-time.Hour), to: start.Add(time.Hour)}}
	svc := alarm.NewService(alarm.NewMemoryStore(), nil, sink).WithClock(func() time.Time { return now }).WithMaintenance(checker)
	ctx := context.Background()
	a, err := svc.Raise(ctx, memOrg, ciRaise(cpeCI, "cpe"))
	if err != nil {
		t.Fatal(err)
	}
	sink.Reset()
	if n, rerr := svc.ReleaseStaleMaintenance(ctx, memOrg); rerr != nil || n != 0 {
		t.Fatalf("window active: %d released (%v), want none", n, rerr)
	}
	checker.cancelled = true
	if n, rerr := svc.ReleaseStaleMaintenance(ctx, memOrg); rerr != nil || n != 1 {
		t.Fatalf("window cancelled: %d released (%v), want the alarm", n, rerr)
	}
	if a, err = svc.Store().Get(ctx, memOrg, a.ID); err != nil || a.SuppressedReason != nil {
		t.Errorf("after the release: %+v (%v), want unsuppressed", a, err)
	}
	if evs := sink.Events(); len(evs) != 1 || !delivered(t, &evs[0]) {
		t.Errorf("release of the cancelled window: events %v, want one delivered alarm.opened", evs)
	}
}

// switchableWindows is fakeWindows that can be cancelled.
type switchableWindows struct {
	fakeWindows
	cancelled bool
}

func (s *switchableWindows) MaintenanceOf(ctx context.Context, orgID, ciID string, at time.Time) (alarm.MaintenanceHit, error) {
	if s.cancelled {
		return alarm.MaintenanceHit{}, nil
	}
	return s.fakeWindows.MaintenanceOf(ctx, orgID, ciID, at)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
