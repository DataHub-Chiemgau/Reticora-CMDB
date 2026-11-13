package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Schedule triggers (JOB-02 [P3], WFL-02, WP-1091): the due slot of a cron
// expression in the time zone of the definition and the limited catch-up.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestDueSlotInDefinitionTimeZone|TestCatchUpLimitedToLatestSlot|TestValidateSchedule|TestMemorySchedulerOneRunPerSlot' ./internal/workflow/

func mustSpec(t *testing.T, expr string) (string, func(after, now time.Time) (time.Time, int, bool)) {
	t.Helper()
	spec, err := parseSchedule(expr)
	if err != nil {
		t.Fatal(err)
	}
	return expr, func(after, now time.Time) (time.Time, int, bool) {
		return dueSlot(expr, spec, berlin(t), after, now)
	}
}

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("time zone database unavailable")
	}
	return loc
}

// TestDueSlotInDefinitionTimeZone: "0 6 * * *" in Europe/Berlin is 06:00
// local time on both sides of the switch to and from summer time: 05:00
// UTC in winter, 04:00 UTC in summer.
func TestDueSlotInDefinitionTimeZone(t *testing.T) {
	_, due := mustSpec(t, "0 6 * * *")
	for _, c := range []struct {
		name       string
		after, now time.Time
		want       time.Time
	}{
		{"day before the switch to summer time", time.Date(2026, 3, 27, 5, 30, 0, 0, time.UTC), time.Date(2026, 3, 28, 5, 30, 0, 0, time.UTC),
			time.Date(2026, 3, 28, 5, 0, 0, 0, time.UTC)},
		{"day of the switch to summer time", time.Date(2026, 3, 28, 5, 30, 0, 0, time.UTC), time.Date(2026, 3, 29, 5, 30, 0, 0, time.UTC),
			time.Date(2026, 3, 29, 4, 0, 0, 0, time.UTC)},
		{"day of the switch to winter time", time.Date(2026, 10, 24, 5, 30, 0, 0, time.UTC), time.Date(2026, 10, 25, 6, 30, 0, 0, time.UTC),
			time.Date(2026, 10, 25, 5, 0, 0, 0, time.UTC)},
	} {
		slot, skipped, ok := due(c.after, c.now)
		if !ok || !slot.Equal(c.want) || skipped != 0 {
			t.Errorf("%s: slot %v skipped %d ok %v, want %v", c.name, slot, skipped, ok, c.want)
		}
	}
	// Before 06:00 local time on the day of the switch nothing is due yet.
	if slot, _, ok := due(time.Date(2026, 3, 28, 5, 30, 0, 0, time.UTC), time.Date(2026, 3, 29, 3, 59, 0, 0, time.UTC)); ok {
		t.Errorf("before 06:00 CEST: slot %v due, want none", slot)
	}
	// A wall time the clock skips (02:30 on 29 March) runs once, not twice.
	_, half := mustSpec(t, "30 2 * * *")
	slot, skipped, ok := half(time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC), time.Date(2026, 3, 29, 3, 0, 0, 0, time.UTC))
	if !ok || skipped != 0 || !slot.Equal(time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)) {
		t.Errorf("skipped wall time: slot %v skipped %d ok %v, want 03:30 CEST once", slot, skipped, ok)
	}
}

// TestCatchUpLimitedToLatestSlot: after an outage only the newest missed
// slot is due; the older ones are left out and counted.
func TestCatchUpLimitedToLatestSlot(t *testing.T) {
	_, due := mustSpec(t, "0 * * * *")
	after := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC) // 10:00 CEST, the last run
	now := time.Date(2026, 6, 1, 13, 30, 0, 0, time.UTC) // 15:30 CEST
	slot, skipped, ok := due(after, now)
	if !ok || !slot.Equal(time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)) || skipped != 4 {
		t.Fatalf("catch-up: slot %v skipped %d ok %v, want 15:00 CEST with 4 skipped", slot, skipped, ok)
	}
	if _, _, ok = due(slot, now); ok {
		t.Errorf("the slot that ran is due again")
	}
	every, err := parseSchedule("@every 15m")
	if err != nil {
		t.Fatal(err)
	}
	slot, skipped, ok = dueSlot("@every 15m", every, time.UTC, after, after.Add(time.Hour+5*time.Minute))
	if !ok || !slot.Equal(after.Add(time.Hour)) || skipped != 3 {
		t.Errorf("@every 15m: slot %v skipped %d ok %v, want after+1h with 3 skipped", slot, skipped, ok)
	}
}

// TestValidateSchedule: cron and timezone of a schedule trigger are
// checked; an @every below one minute is refused.
func TestValidateSchedule(t *testing.T) {
	for _, c := range []struct {
		trigger JSONMap
		field   string
	}{
		{JSONMap{"type": "schedule", "cron": "0 6 * * *", "timezone": "Europe/Berlin"}, ""},
		{JSONMap{"type": "schedule", "cron": "@every 5m"}, ""},
		{JSONMap{"type": "manual"}, ""},
		{JSONMap{"type": "schedule"}, "trigger.cron"},
		{JSONMap{"type": "schedule", "cron": "61 * * * *"}, "trigger.cron"},
		{JSONMap{"type": "schedule", "cron": "@every 30s"}, "trigger.cron"},
		{JSONMap{"type": "schedule", "cron": 5}, "trigger.cron"},
		{JSONMap{"type": "schedule", "cron": "0 6 * * *", "timezone": "Mars/Olympus"}, "trigger.timezone"},
		{JSONMap{"type": "schedule", "cron": "0 6 * * *", "timezone": "Local"}, "trigger.timezone"},
		{JSONMap{"type": "schedule", "cron": "0 6 * * *", "timezone": 1}, "trigger.timezone"},
	} {
		err := ValidateSchedule(c.trigger)
		var fe *api.FieldError
		switch {
		case c.field == "" && err != nil:
			t.Errorf("%v: %v, want valid", c.trigger, err)
		case c.field != "" && (!errors.As(err, &fe) || fe.Field != c.field || fe.Status != 422):
			t.Errorf("%v: %v, want 422 on %s", c.trigger, err, c.field)
		}
	}
}

// TestMemorySchedulerOneRunPerSlot: without a database the scheduler
// starts one run per slot as well (parity), with the context of the slot.
func TestMemorySchedulerOneRunPerSlot(t *testing.T) {
	repo := NewMemoryRepository()
	exec := NewExecutor(repo, nil, nil, nil, nil)
	ctx := context.Background()
	def := &Definition{OrganizationID: "org-1", Name: "nightly", Trigger: JSONMap{"type": "schedule", "cron": "0 6 * * *", "timezone": "UTC"},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := repo.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(exec, repo, func(context.Context) ([]string, error) { return []string{"org-1"}, nil },
		func(context.Context, string) bool { return true })
	s.now = func() time.Time { return time.Now().UTC().Add(25 * time.Hour) }
	for range 2 {
		if _, err := s.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runs, _, err := repo.ListRuns(ctx, "org-1", &RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v, want one", runs, err)
	}
	if runs[0].Trigger != TriggerSchedule || runs[0].Status != StatusSucceeded {
		t.Errorf("run: trigger %q status %q, want schedule and succeeded", runs[0].Trigger, runs[0].Status)
	}
	sched, _ := runs[0].Context["schedule"].(map[string]any)
	if sched["cron"] != "0 6 * * *" || sched["slot"] == "" {
		t.Errorf("context: %v, want schedule.slot and schedule.cron", runs[0].Context)
	}
}
