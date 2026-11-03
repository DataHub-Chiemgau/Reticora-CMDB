package sla

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // IANA zone names resolve without the host's zoneinfo

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Window is one span of working time on a weekday in local wall time
// (TKT-02): weekday 1 is Monday, 7 is Sunday; start and end are "HH:MM"
// with start before end ("24:00" ends at midnight).
type Window struct {
	Weekday int    `json:"weekday"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// Holiday is a whole local day on which the SLA clock pauses.
type Holiday struct {
	Date string `json:"date"` // YYYY-MM-DD
	Name string `json:"name,omitempty"`
}

// Calendar is a business calendar (TKT-02): working windows per weekday
// in local wall time and holidays. Timezone is optional; without it the
// caller derives the zone (client, then organization).
type Calendar struct {
	Timezone    string    `json:"timezone,omitempty"`
	WeeklyHours []Window  `json:"weekly_hours"`
	Holidays    []Holiday `json:"holidays"`
}

// DefaultCalendar is the built-in calendar used when a policy counts
// business time but no stored calendar applies: Monday to Friday,
// 08:00–18:00, no holidays.
func DefaultCalendar() Calendar {
	hours := make([]Window, 0, 5)
	for d := 1; d <= 5; d++ {
		hours = append(hours, Window{Weekday: d, Start: "08:00", End: "18:00"})
	}
	return Calendar{WeeklyHours: hours, Holidays: []Holiday{}}
}

// maxIdleDays bounds the search for the next business moment: a calendar
// whose windows are all covered by holidays for this long has none.
const maxIdleDays = 400

// ErrNoBusinessTime: the calendar has no business moment within
// maxIdleDays of the start.
var ErrNoBusinessTime = errors.New("business calendar has no working time within 400 days")

// ValidTimezone checks an IANA time zone name like tenantapi.validTimezone
// (LOC-01–LOC-03): "UTC" is a name of the tz database, "Local" and
// abbreviations are not.
//
// validation: fixed text with the rejected name.
func ValidTimezone(field, tz string) error {
	if tz == "" || tz == "Local" || !strings.Contains(tz, "/") && tz != "UTC" {
		return &api.FieldError{Field: field, Message: fmt.Sprintf("%q is not an IANA time zone (e.g. Europe/Berlin)", tz)}
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return &api.FieldError{Field: field, Message: fmt.Sprintf("%q is not an IANA time zone (e.g. Europe/Berlin)", tz)}
	}
	return nil
}

// parseClock parses "HH:MM" (00:00–24:00) into minutes after midnight.
func parseClock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	var h, m int
	for _, c := range s[:2] {
		if c < '0' || c > '9' {
			return 0, false
		}
		h = h*10 + int(c-'0')
	}
	for _, c := range s[3:] {
		if c < '0' || c > '9' {
			return 0, false
		}
		m = m*10 + int(c-'0')
	}
	if m > 59 || h > 24 || h == 24 && m != 0 {
		return 0, false
	}
	return h*60 + m, true
}

// Validate checks the calendar (TKT-02) and returns a *api.FieldError for
// an invalid IANA time zone, an unknown weekday, a malformed time, a
// window with start not before end, overlapping windows on one day, an
// empty window list and malformed or duplicate holiday dates. WP-768 uses
// the same check.
//
// validation: fixed texts with the rejected values.
func (c *Calendar) Validate() error {
	if c.Timezone != "" {
		if err := ValidTimezone("timezone", c.Timezone); err != nil {
			return err
		}
	}
	if len(c.WeeklyHours) == 0 {
		return &api.FieldError{Field: "weekly_hours", Message: "must contain at least one window"}
	}
	type span struct{ start, end int }
	perDay := map[int][]span{}
	for _, w := range c.WeeklyHours {
		if w.Weekday < 1 || w.Weekday > 7 {
			return &api.FieldError{Field: "weekly_hours", Message: fmt.Sprintf("weekday %d is not between 1 (Monday) and 7 (Sunday)", w.Weekday)}
		}
		start, okStart := parseClock(w.Start)
		end, okEnd := parseClock(w.End)
		if !okStart || !okEnd {
			return &api.FieldError{Field: "weekly_hours", Message: fmt.Sprintf("window %q–%q: times must be HH:MM", w.Start, w.End)}
		}
		if start >= end {
			return &api.FieldError{Field: "weekly_hours", Message: fmt.Sprintf("window %s–%s on weekday %d: start must be before end", w.Start, w.End, w.Weekday)}
		}
		perDay[w.Weekday] = append(perDay[w.Weekday], span{start, end})
	}
	for day, spans := range perDay {
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		for i := 1; i < len(spans); i++ {
			if spans[i].start < spans[i-1].end {
				return &api.FieldError{Field: "weekly_hours", Message: fmt.Sprintf("windows on weekday %d overlap", day)}
			}
		}
	}
	seen := map[string]bool{}
	for _, h := range c.Holidays {
		if _, err := time.Parse(time.DateOnly, h.Date); err != nil {
			return &api.FieldError{Field: "holidays", Message: fmt.Sprintf("date %q must be YYYY-MM-DD", h.Date)}
		}
		if seen[h.Date] {
			return &api.FieldError{Field: "holidays", Message: fmt.Sprintf("date %s is listed twice", h.Date)}
		}
		seen[h.Date] = true
	}
	return nil
}

// Schedule is a calendar bound to a time zone, ready for computation.
type Schedule struct {
	loc      *time.Location
	windows  [7][][2]int // by time.Weekday: sorted [start, end) in minutes after local midnight
	holidays map[string]bool
}

// In binds the calendar to loc (UTC when nil). Windows that do not parse
// are skipped; stored calendars passed Validate.
func (c *Calendar) In(loc *time.Location) *Schedule {
	if loc == nil {
		loc = time.UTC
	}
	s := &Schedule{loc: loc, holidays: map[string]bool{}}
	for _, w := range c.WeeklyHours {
		start, okStart := parseClock(w.Start)
		end, okEnd := parseClock(w.End)
		if !okStart || !okEnd || start >= end || w.Weekday < 1 || w.Weekday > 7 {
			continue
		}
		day := w.Weekday % 7 // 7 (Sunday) → time.Sunday
		s.windows[day] = append(s.windows[day], [2]int{start, end})
	}
	for d := range s.windows {
		sort.Slice(s.windows[d], func(i, j int) bool { return s.windows[d][i][0] < s.windows[d][j][0] })
	}
	for _, h := range c.Holidays {
		s.holidays[h.Date] = true
	}
	return s
}

// dayWindows returns the absolute business spans of the local day that
// starts at the date of day (year, month, day in s.loc); nil on a holiday
// or a day without windows. Each bound is built from the local wall time,
// so a window keeps its local hours across a daylight saving change.
func (s *Schedule) dayWindows(y int, m time.Month, d int) [][2]time.Time {
	date := time.Date(y, m, d, 12, 0, 0, 0, s.loc)
	if s.holidays[date.Format(time.DateOnly)] {
		return nil
	}
	spans := s.windows[date.Weekday()]
	if len(spans) == 0 {
		return nil
	}
	out := make([][2]time.Time, 0, len(spans))
	for _, w := range spans {
		ws := time.Date(y, m, d, w[0]/60, w[0]%60, 0, 0, s.loc)
		we := time.Date(y, m, d, w[1]/60, w[1]%60, 0, 0, s.loc)
		if we.After(ws) {
			out = append(out, [2]time.Time{ws, we})
		}
	}
	return out
}

// Add advances start by the given number of business minutes. It returns
// ErrNoBusinessTime when no business moment lies within maxIdleDays.
func (s *Schedule) Add(start time.Time, minutes int) (time.Time, error) {
	if minutes <= 0 {
		return start, nil
	}
	remaining := time.Duration(minutes) * time.Minute
	local := start.In(s.loc)
	y, m, d := local.Date()
	idle := 0
	for i := 0; ; i++ {
		spans := s.dayWindows(y, m, d+i)
		found := false
		for _, span := range spans {
			ws, we := span[0], span[1]
			if !we.After(start) {
				continue
			}
			if ws.Before(start) {
				ws = start
			}
			found = true
			available := we.Sub(ws)
			if remaining <= available {
				return ws.Add(remaining).UTC(), nil
			}
			remaining -= available
		}
		if found {
			idle = 0
			continue
		}
		idle++
		if idle > maxIdleDays {
			return time.Time{}, ErrNoBusinessTime
		}
	}
}

// BusinessMinutesBetween counts the business minutes from from to to;
// negative when to lies before from.
func (s *Schedule) BusinessMinutesBetween(from, to time.Time) int {
	if to.Before(from) {
		return -s.BusinessMinutesBetween(to, from)
	}
	var total time.Duration
	lf := from.In(s.loc)
	y, m, d := lf.Date()
	for i := 0; ; i++ {
		dayStart := time.Date(y, m, d+i, 0, 0, 0, 0, s.loc)
		if !dayStart.Before(to) {
			break
		}
		for _, span := range s.dayWindows(y, m, d+i) {
			ws, we := span[0], span[1]
			if ws.Before(from) {
				ws = from
			}
			if we.After(to) {
				we = to
			}
			if we.After(ws) {
				total += we.Sub(ws)
			}
		}
	}
	return int(total / time.Minute)
}

// IsBusinessMoment reports whether t lies inside a window of a day that
// is no holiday.
func (s *Schedule) IsBusinessMoment(t time.Time) bool {
	y, m, d := t.In(s.loc).Date()
	for _, span := range s.dayWindows(y, m, d) {
		if !t.Before(span[0]) && t.Before(span[1]) {
			return true
		}
	}
	return false
}

// addTarget computes the due time for a target given in minutes. Without
// a calendar (sla.business_calendar = false) the minutes are elapsed time.
// With a calendar only minutes inside its windows in local wall time of
// loc count: weekends and holidays pause the clock.
func addTarget(start time.Time, minutes int, cal *Calendar, loc *time.Location) (time.Time, error) {
	if cal == nil {
		return start.Add(time.Duration(minutes) * time.Minute), nil
	}
	return cal.In(loc).Add(start, minutes)
}
