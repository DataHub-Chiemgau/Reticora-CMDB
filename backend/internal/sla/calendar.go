package sla

import "time"

// Business hours used when a policy sets business_calendar. The first
// iteration intentionally keeps the calendar fixed and simple: Monday to
// Friday, 08:00–18:00 UTC, no holidays. Per-tenant calendars (time zone,
// custom windows, holiday tables) are a follow-up and must not change this
// function's signature — pass a Calendar instead of extending the constants.
const (
	businessDayStartHour = 8  // 08:00
	businessDayEndHour   = 18 // 18:00
)

// addTarget computes the due time for a target given in minutes. With
// businessCalendar=false the minutes are simple elapsed time. With
// businessCalendar=true only business hours (Mon–Fri, 08:00–18:00 UTC) count:
// a ticket created Friday 17:00 with a 4-hour response target is due Monday
// at 09:00, not Friday 21:00.
func addTarget(start time.Time, minutes int, businessCalendar bool) time.Time {
	if !businessCalendar {
		return start.Add(time.Duration(minutes) * time.Minute)
	}
	return addBusinessMinutes(start, minutes)
}

// addBusinessMinutes advances start by the given number of business minutes.
func addBusinessMinutes(start time.Time, minutes int) time.Time {
	if minutes <= 0 {
		return start
	}
	current := nextBusinessMoment(start.UTC())
	remaining := time.Duration(minutes) * time.Minute
	for remaining > 0 {
		endOfDay := time.Date(current.Year(), current.Month(), current.Day(),
			businessDayEndHour, 0, 0, 0, time.UTC)
		available := endOfDay.Sub(current)
		if remaining <= available {
			return current.Add(remaining)
		}
		remaining -= available
		current = nextBusinessMoment(endOfDay.Add(time.Second))
	}
	return current
}

// nextBusinessMoment moves t forward to the next instant that lies inside
// business hours (Mon–Fri, 08:00–18:00 UTC). A t already inside business
// hours is returned unchanged.
func nextBusinessMoment(t time.Time) time.Time {
	t = t.UTC()
	for {
		if isWeekend(t) {
			// Skip to Monday 08:00.
			daysUntilMonday := (int(time.Monday) - int(t.Weekday()) + 7) % 7
			if daysUntilMonday == 0 {
				daysUntilMonday = 7
			}
			t = time.Date(t.Year(), t.Month(), t.Day()+daysUntilMonday,
				businessDayStartHour, 0, 0, 0, time.UTC)
			continue
		}
		dayStart := time.Date(t.Year(), t.Month(), t.Day(),
			businessDayStartHour, 0, 0, 0, time.UTC)
		dayEnd := time.Date(t.Year(), t.Month(), t.Day(),
			businessDayEndHour, 0, 0, 0, time.UTC)
		if t.Before(dayStart) {
			t = dayStart
			continue
		}
		if !t.Before(dayEnd) {
			// After hours: next weekday at 08:00.
			t = dayStart.AddDate(0, 0, 1)
			continue
		}
		return t
	}
}

func isWeekend(t time.Time) bool {
	return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
}
