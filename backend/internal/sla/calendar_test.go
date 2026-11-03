package sla

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return parsed.UTC()
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

// due runs addTarget with the built-in standard calendar in UTC (business)
// or without calendar (elapsed).
func due(t *testing.T, start time.Time, minutes int, business bool) time.Time {
	t.Helper()
	var cal *Calendar
	if business {
		c := DefaultCalendar()
		cal = &c
	}
	got, err := addTarget(start, minutes, cal, time.UTC)
	if err != nil {
		t.Fatalf("addTarget: %v", err)
	}
	return got
}

func dueIn(t *testing.T, cal Calendar, loc *time.Location, start time.Time, minutes int) time.Time {
	t.Helper()
	got, err := addTarget(start, minutes, &cal, loc)
	if err != nil {
		t.Fatalf("addTarget: %v", err)
	}
	return got
}

func TestAddTargetElapsed(t *testing.T) {
	// Without a business calendar the target is plain elapsed time, unchanged
	// from the previous behaviour.
	start := at(t, "2026-08-07T17:00:00Z") // Friday 17:00
	got := due(t, start, 240, false)
	want := at(t, "2026-08-07T21:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("elapsed: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessSameDay(t *testing.T) {
	start := at(t, "2026-08-10T10:00:00Z") // Monday 10:00
	got := due(t, start, 120, true)
	want := at(t, "2026-08-10T12:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("same day: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessOvernight(t *testing.T) {
	// 4 business hours from Friday 17:00: 1h on Friday + 3h on Monday → Mon 11:00.
	start := at(t, "2026-08-07T17:00:00Z")
	got := due(t, start, 240, true)
	want := at(t, "2026-08-10T11:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("overnight: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessWeekend(t *testing.T) {
	// Created on Saturday: the clock starts Monday 08:00; 60 minutes → Mon 09:00.
	start := at(t, "2026-08-08T12:00:00Z") // Saturday
	got := due(t, start, 60, true)
	want := at(t, "2026-08-10T09:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("weekend: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessBeforeHours(t *testing.T) {
	// Created before business hours: the clock starts at 08:00 the same day.
	start := at(t, "2026-08-11T06:30:00Z") // Tuesday 06:30
	got := due(t, start, 90, true)
	want := at(t, "2026-08-11T09:30:00Z")
	if !got.Equal(want) {
		t.Fatalf("before hours: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessAfterHours(t *testing.T) {
	// Created after business hours: the clock starts the next weekday at 08:00.
	start := at(t, "2026-08-11T19:00:00Z") // Tuesday 19:00
	got := due(t, start, 30, true)
	want := at(t, "2026-08-12T08:30:00Z")
	if !got.Equal(want) {
		t.Fatalf("after hours: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessMultiDay(t *testing.T) {
	// A full business day has 10 hours (08:00–18:00); 12 business hours from
	// Monday 08:00 therefore end on Tuesday at 10:00.
	start := at(t, "2026-08-10T08:00:00Z")
	got := due(t, start, 720, true)
	want := at(t, "2026-08-11T10:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("multi day: got %s, want %s", got, want)
	}
}

func TestAddTargetZeroMinutes(t *testing.T) {
	start := at(t, "2026-08-10T10:00:00Z")
	if got := due(t, start, 0, true); !got.Equal(start) {
		t.Fatalf("zero minutes should return start unchanged, got %s", got)
	}
}

// TestAddTargetDaylightSavingSpring: Friday, 27.03.2026, 17:00 CET plus two
// business hours in Europe/Berlin is Monday, 30.03.2026, 09:00 CEST
// (07:00 UTC); the clocks moved forward on Sunday, 29.03.
func TestAddTargetDaylightSavingSpring(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	start := time.Date(2026, 3, 27, 17, 0, 0, 0, berlin)
	got := dueIn(t, DefaultCalendar(), berlin, start, 120)
	if want := at(t, "2026-03-30T07:00:00Z"); !got.Equal(want) {
		t.Fatalf("spring: got %s, want %s", got, want)
	}
	if local := got.In(berlin); local.Hour() != 9 || local.Weekday() != time.Monday {
		t.Fatalf("spring: local due %s, want Monday 09:00", local)
	}
}

// TestAddTargetDaylightSavingAutumn: Friday, 23.10.2026, 17:00 CEST plus two
// business hours is Monday, 26.10.2026, 09:00 CET (08:00 UTC); the clocks
// moved back on Sunday, 25.10.
func TestAddTargetDaylightSavingAutumn(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	start := time.Date(2026, 10, 23, 17, 0, 0, 0, berlin)
	got := dueIn(t, DefaultCalendar(), berlin, start, 120)
	if want := at(t, "2026-10-26T08:00:00Z"); !got.Equal(want) {
		t.Fatalf("autumn: got %s, want %s", got, want)
	}
}

func TestAddTargetHolidayOnWeekday(t *testing.T) {
	// Monday 10.08.2026 is a holiday: two hours from Friday 17:00 end on
	// Tuesday at 09:00.
	cal := DefaultCalendar()
	cal.Holidays = []Holiday{{Date: "2026-08-10", Name: "Betriebsruhe"}}
	got := dueIn(t, cal, time.UTC, at(t, "2026-08-07T17:00:00Z"), 120)
	if want := at(t, "2026-08-11T09:00:00Z"); !got.Equal(want) {
		t.Fatalf("holiday: got %s, want %s", got, want)
	}
}

func TestAddTargetTwoWindowsPerDay(t *testing.T) {
	// Lunch break 12:00–13:00: two hours from Monday 11:00 end at 14:00.
	cal := Calendar{WeeklyHours: []Window{{Weekday: 1, Start: "08:00", End: "12:00"}, {Weekday: 1, Start: "13:00", End: "17:00"}}}
	got := dueIn(t, cal, time.UTC, at(t, "2026-08-10T11:00:00Z"), 120)
	if want := at(t, "2026-08-10T14:00:00Z"); !got.Equal(want) {
		t.Fatalf("lunch break: got %s, want %s", got, want)
	}
	// From inside the break the clock waits until 13:00.
	got = dueIn(t, cal, time.UTC, at(t, "2026-08-10T12:30:00Z"), 30)
	if want := at(t, "2026-08-10T13:30:00Z"); !got.Equal(want) {
		t.Fatalf("inside break: got %s, want %s", got, want)
	}
}

func TestAddTargetAucklandAgainstUTC(t *testing.T) {
	// Sunday 09.08.2026 21:00 UTC is Monday 09:00 in Auckland (NZST,
	// UTC+12): there the clock runs, in UTC it waits for Monday 08:00.
	auckland := zone(t, "Pacific/Auckland")
	start := at(t, "2026-08-09T21:00:00Z")
	got := dueIn(t, DefaultCalendar(), auckland, start, 60)
	if want := at(t, "2026-08-09T22:00:00Z"); !got.Equal(want) {
		t.Fatalf("auckland: got %s, want %s", got, want)
	}
	got = dueIn(t, DefaultCalendar(), time.UTC, start, 60)
	if want := at(t, "2026-08-10T09:00:00Z"); !got.Equal(want) {
		t.Fatalf("utc: got %s, want %s", got, want)
	}
}

func TestBusinessMinutesBetween(t *testing.T) {
	cal := DefaultCalendar()
	s := cal.In(time.UTC)
	from, to := at(t, "2026-08-07T17:00:00Z"), at(t, "2026-08-10T09:00:00Z")
	if got := s.BusinessMinutesBetween(from, to); got != 120 {
		t.Fatalf("forward: got %d, want 120", got)
	}
	if got := s.BusinessMinutesBetween(to, from); got != -120 {
		t.Fatalf("backward: got %d, want -120", got)
	}
	if got := s.BusinessMinutesBetween(from, from); got != 0 {
		t.Fatalf("same moment: got %d, want 0", got)
	}
	// Across the spring change in Berlin: Friday 17:00 CET to Monday 09:00
	// CEST are two business hours as well.
	berlin := zone(t, "Europe/Berlin")
	sb := cal.In(berlin)
	if got := sb.BusinessMinutesBetween(time.Date(2026, 3, 27, 17, 0, 0, 0, berlin), time.Date(2026, 3, 30, 9, 0, 0, 0, berlin)); got != 120 {
		t.Fatalf("berlin spring: got %d, want 120", got)
	}
}

func TestIsBusinessMoment(t *testing.T) {
	cal := DefaultCalendar()
	monday := at(t, "2026-08-10T10:00:00Z")
	if !cal.In(time.UTC).IsBusinessMoment(monday) {
		t.Fatal("Monday 10:00 should be a business moment")
	}
	if cal.In(time.UTC).IsBusinessMoment(at(t, "2026-08-08T10:00:00Z")) {
		t.Fatal("Saturday should not be a business moment")
	}
	if cal.In(time.UTC).IsBusinessMoment(at(t, "2026-08-10T18:00:00Z")) {
		t.Fatal("18:00 ends the window")
	}
	cal.Holidays = []Holiday{{Date: "2026-08-10"}}
	if cal.In(time.UTC).IsBusinessMoment(monday) {
		t.Fatal("a holiday should not be a business moment")
	}
	// The holiday is a local date: in Auckland Monday 10.08. starts at
	// Sunday 12:00 UTC.
	if cal.In(zone(t, "Pacific/Auckland")).IsBusinessMoment(at(t, "2026-08-09T22:00:00Z")) {
		t.Fatal("the local holiday in Auckland should pause the clock")
	}
}

func TestCalendarValidate(t *testing.T) {
	week := DefaultCalendar().WeeklyHours
	cases := []struct {
		name  string
		cal   Calendar
		field string
	}{
		{"zone unknown", Calendar{Timezone: "Mars/Olympus", WeeklyHours: week}, "timezone"},
		{"zone abbreviation", Calendar{Timezone: "CET", WeeklyHours: week}, "timezone"},
		{"zone local", Calendar{Timezone: "Local", WeeklyHours: week}, "timezone"},
		{"weekday zero", Calendar{WeeklyHours: []Window{{Weekday: 0, Start: "08:00", End: "18:00"}}}, "weekly_hours"},
		{"weekday eight", Calendar{WeeklyHours: []Window{{Weekday: 8, Start: "08:00", End: "18:00"}}}, "weekly_hours"},
		{"malformed time", Calendar{WeeklyHours: []Window{{Weekday: 1, Start: "8:00", End: "18:00"}}}, "weekly_hours"},
		{"hour 25", Calendar{WeeklyHours: []Window{{Weekday: 1, Start: "08:00", End: "25:00"}}}, "weekly_hours"},
		{"start equals end", Calendar{WeeklyHours: []Window{{Weekday: 1, Start: "08:00", End: "08:00"}}}, "weekly_hours"},
		{"start after end", Calendar{WeeklyHours: []Window{{Weekday: 1, Start: "18:00", End: "08:00"}}}, "weekly_hours"},
		{"overlap", Calendar{WeeklyHours: []Window{{Weekday: 2, Start: "08:00", End: "12:00"}, {Weekday: 2, Start: "11:00", End: "15:00"}}}, "weekly_hours"},
		{"empty", Calendar{WeeklyHours: []Window{}}, "weekly_hours"},
		{"holiday malformed", Calendar{WeeklyHours: week, Holidays: []Holiday{{Date: "10.08.2026"}}}, "holidays"},
		{"holiday twice", Calendar{WeeklyHours: week, Holidays: []Holiday{{Date: "2026-12-25", Name: "a"}, {Date: "2026-12-25", Name: "b"}}}, "holidays"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cal.Validate()
			var fe *api.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Fatalf("got %v, want a field error on %s", err, tc.field)
			}
		})
	}
	valid := []Calendar{
		DefaultCalendar(),
		{Timezone: "Europe/Berlin", WeeklyHours: []Window{{Weekday: 1, Start: "08:00", End: "12:00"}, {Weekday: 1, Start: "12:00", End: "16:00"}, {Weekday: 7, Start: "00:00", End: "24:00"}}, Holidays: []Holiday{{Date: "2026-12-24"}, {Date: "2026-12-25"}}},
		{Timezone: "UTC", WeeklyHours: week},
	}
	for _, cal := range valid {
		if err := cal.Validate(); err != nil {
			t.Fatalf("valid calendar %+v: %v", cal, err)
		}
	}
}

func TestAddTargetWithoutBusinessTimeFails(t *testing.T) {
	// Only Mondays, and every Monday of the next three years is a holiday:
	// the search stops after 400 days instead of looping.
	cal := Calendar{WeeklyHours: []Window{{Weekday: 1, Start: "08:00", End: "18:00"}}}
	for d := at(t, "2026-08-10T00:00:00Z"); d.Year() < 2030; d = d.AddDate(0, 0, 7) {
		cal.Holidays = append(cal.Holidays, Holiday{Date: d.Format(time.DateOnly)})
	}
	if err := cal.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := addTarget(at(t, "2026-08-08T00:00:00Z"), 60, &cal, time.UTC); !errors.Is(err, ErrNoBusinessTime) {
		t.Fatalf("got %v, want ErrNoBusinessTime", err)
	}
}

func TestFillRemaining(t *testing.T) {
	now := at(t, "2026-08-07T17:00:00Z") // Friday 17:00
	state := TicketSLA{ResponseDueAt: at(t, "2026-08-10T09:00:00Z"), ResolutionDueAt: at(t, "2026-08-07T16:00:00Z")}
	cal := DefaultCalendar()
	fillRemaining(&state, cal.In(time.UTC), now)
	if *state.ResponseRemainingMinutes != 120 || *state.ResolutionRemainingMinutes != -60 || state.Paused {
		t.Fatalf("business: %+v", state)
	}
	fillRemaining(&state, nil, now)
	if *state.ResponseRemainingMinutes != 3840 || *state.ResolutionRemainingMinutes != -60 || state.Paused {
		t.Fatalf("elapsed: response %d resolution %d", *state.ResponseRemainingMinutes, *state.ResolutionRemainingMinutes)
	}
	resolved := now
	state.ResolvedAt = &resolved
	fillRemaining(&state, cal.In(time.UTC), at(t, "2026-08-08T10:00:00Z"))
	if state.ResolutionRemainingMinutes != nil || state.ResponseRemainingMinutes == nil || !state.Paused {
		t.Fatalf("resolved on Saturday: %+v", state)
	}
}

func TestOptionalIDDecoding(t *testing.T) {
	var req UpdatePolicyRequest
	if err := json.Unmarshal([]byte(`{"name":"x"}`), &req); err != nil || req.CalendarID.Set {
		t.Fatalf("absent: %+v %v", req.CalendarID, err)
	}
	if err := json.Unmarshal([]byte(`{"calendar_id":null}`), &req); err != nil || !req.CalendarID.Set || req.CalendarID.Value != "" {
		t.Fatalf("null: %+v %v", req.CalendarID, err)
	}
	if err := json.Unmarshal([]byte(`{"calendar_id":"abc"}`), &req); err != nil || !req.CalendarID.Set || req.CalendarID.Value != "abc" {
		t.Fatalf("value: %+v %v", req.CalendarID, err)
	}
}

// TestMemoryRepositoryCalendarChain: the memory repository (--no-db)
// applies the chain without the client level: policy calendar, org
// default, built-in standard; without a zone at the calendar in UTC.
func TestMemoryRepositoryCalendarChain(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	const org = "org-1"
	repo.calendars["cal-org"] = &memoryCalendar{ID: "cal-org", OrganizationID: org, IsDefault: true,
		Calendar: Calendar{WeeklyHours: []Window{{Weekday: 6, Start: "10:00", End: "12:00"}}}}
	repo.calendars["cal-berlin"] = &memoryCalendar{ID: "cal-berlin", OrganizationID: org,
		Calendar: Calendar{Timezone: "Europe/Berlin", WeeklyHours: DefaultCalendar().WeeklyHours}}
	repo.calendars["cal-client"] = &memoryCalendar{ID: "cal-client", OrganizationID: org, ClientID: "client-1",
		Calendar: DefaultCalendar()}

	created := at(t, "2026-08-07T17:00:00Z") // Friday 17:00 UTC, 19:00 in Berlin
	tk := &ticket.Ticket{ID: "t-1", Priority: "high", CreatedAt: created}

	// Org default: Saturday 10:00–12:00 UTC.
	policy := &Policy{OrganizationID: org, Name: "p", Priority: "high", ResponseTargetMinutes: 60, ResolutionTargetMinutes: 120, BusinessCalendar: true}
	if err := repo.CreatePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	state, err := repo.ApplyForTicket(ctx, org, tk, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.CalendarID == nil || *state.CalendarID != "cal-org" || !state.ResponseDueAt.Equal(at(t, "2026-08-08T11:00:00Z")) {
		t.Fatalf("org default: calendar %v due %s", state.CalendarID, state.ResponseDueAt)
	}

	// Policy calendar in Berlin wins: Monday 08:00 CEST is 06:00 UTC.
	berlin := "cal-berlin"
	if _, err = repo.UpdatePolicy(ctx, org, policy.ID, UpdatePolicyRequest{CalendarID: OptionalID{Set: true, Value: berlin}}); err != nil {
		t.Fatal(err)
	}
	if state, err = repo.ApplyForTicket(ctx, org, tk, ""); err != nil {
		t.Fatal(err)
	}
	if *state.CalendarID != berlin || !state.ResponseDueAt.Equal(at(t, "2026-08-10T07:00:00Z")) {
		t.Fatalf("policy calendar: calendar %v due %s", *state.CalendarID, state.ResponseDueAt)
	}

	// A client calendar cannot serve an org policy.
	client := "cal-client"
	if _, err = repo.UpdatePolicy(ctx, org, policy.ID, UpdatePolicyRequest{CalendarID: OptionalID{Set: true, Value: client}}); !errors.As(err, new(*api.FieldError)) {
		t.Fatalf("client calendar on org policy: got %v, want a field error", err)
	}

	// Without stored calendars: the built-in standard in UTC.
	delete(repo.calendars, "cal-org")
	delete(repo.calendars, "cal-berlin")
	if _, err = repo.UpdatePolicy(ctx, org, policy.ID, UpdatePolicyRequest{CalendarID: OptionalID{Set: true}}); err != nil {
		t.Fatal(err)
	}
	if state, err = repo.ApplyForTicket(ctx, org, tk, ""); err != nil {
		t.Fatal(err)
	}
	if state.CalendarID != nil || !state.ResponseDueAt.Equal(at(t, "2026-08-07T18:00:00Z")) {
		t.Fatalf("built-in: calendar %v due %s", state.CalendarID, state.ResponseDueAt)
	}
}
