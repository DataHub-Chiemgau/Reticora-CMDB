package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSpecNext covers the schedule expressions of JOB-02 (WP-131): five
// cron fields with lists, ranges and steps, Sunday as 0 and 7, the cron
// rule for two restricted day fields, and @every.
func TestSpecNext(t *testing.T) {
	for _, tc := range []struct{ spec, after, want string }{
		{"* * * * *", "2026-10-13T12:00:30Z", "2026-10-13T12:01:00Z"},
		{"*/15 * * * *", "2026-10-13T12:01:00Z", "2026-10-13T12:15:00Z"},
		{"30 2 * * *", "2026-10-13T12:00:00Z", "2026-10-14T02:30:00Z"},
		{"0 9-17/4 * * *", "2026-10-13T09:00:00Z", "2026-10-13T13:00:00Z"},
		{"0 0 1 * *", "2026-12-15T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"0 3 * * 7", "2026-10-13T00:00:00Z", "2026-10-18T03:00:00Z"}, // Sunday
		{"0 3 * * 1,3", "2026-10-13T04:00:00Z", "2026-10-14T03:00:00Z"},
		{"0 0 13 * 5", "2026-10-13T01:00:00Z", "2026-10-16T00:00:00Z"}, // Friday or the 13th
		{"0 0 29 2 *", "2026-03-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"@every 5m", "2026-10-13T12:07:00Z", "2026-10-13T12:10:00Z"},
		{"@every 5m", "2026-10-13T12:10:00Z", "2026-10-13T12:15:00Z"},
	} {
		spec, err := ParseSpec(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if got := spec.Next(mustTime(t, tc.after)); !got.Equal(mustTime(t, tc.want)) {
			t.Errorf("%s after %s: %s, want %s", tc.spec, tc.after, got.Format(time.RFC3339), tc.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "5-1 * * * *", "@every", "@every -1m", "x * * * *"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}

	spec, _ := ParseSpec("0 * * * *")
	slots, skipped := spec.Slots(mustTime(t, "2026-10-13T00:00:00Z"), mustTime(t, "2026-10-13T05:30:00Z"), 2)
	if skipped != 3 || len(slots) != 2 || !slots[0].Equal(mustTime(t, "2026-10-13T04:00:00Z")) || !slots[1].Equal(mustTime(t, "2026-10-13T05:00:00Z")) {
		t.Errorf("slots: %v skipped %d", slots, skipped)
	}
}

// TestSchedulerSingleRunnerAndCatchUp covers JOB-02 and OPS-03 (WP-131) on
// the memory locker: of two replicas exactly one leads and runs each slot
// once; a new kind starts with its next slot; missed slots are caught up,
// bounded per kind; a failed run is recorded; the other replica takes over
// when the leader stops.
func TestSchedulerSingleRunnerAndCatchUp(t *testing.T) {
	ctx := context.Background()
	c := &clock{t: mustTime(t, "2026-10-13T12:00:00Z")}
	locker, state := NewMemoryLocker(), NewMemoryStateStore()
	var runs []string
	newReplica := func(name string) *Scheduler {
		s := NewScheduler(locker, state).WithClock(c.now)
		s.Runner = name
		if err := s.Register(&Schedule{Kind: "cleanup", Spec: "*/5 * * * *", MaxCatchUp: 2,
			Run: func(context.Context) error { runs = append(runs, name+"@"+c.now().Format("15:04")); return nil }}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := newReplica("a"), newReplica("b")
	if err := a.Register(&Schedule{Kind: "cleanup", Spec: "* * * * *", Run: func(context.Context) error { return nil }}); err == nil {
		t.Error("duplicate kind accepted")
	}
	if err := a.Register(&Schedule{Kind: "bad", Spec: "nope", Run: func(context.Context) error { return nil }}); err == nil {
		t.Error("invalid spec accepted")
	}

	// First tick: a leads; the new kind waits for its next slot.
	if n := a.Tick(ctx) + b.Tick(ctx); n != 0 || !a.IsLeader() || b.IsLeader() {
		t.Fatalf("first tick: %d runs, leaders %v/%v", n, a.IsLeader(), b.IsLeader())
	}
	c.advance(5 * time.Minute)
	if n := a.Tick(ctx) + b.Tick(ctx); n != 1 {
		t.Errorf("12:05: %d runs, want 1", n)
	}
	if n := a.Tick(ctx) + b.Tick(ctx); n != 0 {
		t.Errorf("12:05 again: %d runs, want 0", n)
	}

	// Paused for 22 minutes: four slots missed, two are caught up.
	c.advance(22 * time.Minute)
	if n := a.Tick(ctx); n != 2 {
		t.Errorf("catch-up: %d runs, want 2", n)
	}
	if last, _, _ := state.LastDue(ctx, "cleanup"); !last.Equal(mustTime(t, "2026-10-13T12:25:00Z")) {
		t.Errorf("last slot: %s", last)
	}

	// The leader stops; b takes over.
	a.resign()
	c.advance(5 * time.Minute)
	if n := b.Tick(ctx); n != 1 || !b.IsLeader() {
		t.Errorf("takeover: %d runs, leader %v", n, b.IsLeader())
	}
	if len(runs) != 4 || runs[3] != "b@12:32" {
		t.Errorf("runs: %v", runs)
	}

	// A failed run is recorded and counts as run for its slot.
	failing := NewScheduler(NewMemoryLocker(), state).WithClock(c.now)
	_ = failing.Register(&Schedule{Kind: "audit", Spec: "@every 1m", Run: func(context.Context) error { return errors.New("boom") }})
	failing.Tick(ctx)
	c.advance(time.Minute)
	if n := failing.Tick(ctx); n != 1 {
		t.Errorf("failing run: %d", n)
	}
	if st := state.state["audit"]; st.Error != "boom" {
		t.Errorf("state of the failed run: %+v", st)
	}
}
