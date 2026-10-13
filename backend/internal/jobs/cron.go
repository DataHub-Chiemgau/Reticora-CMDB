package jobs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed schedule: a five-field cron expression (minute hour
// day-of-month month day-of-week, UTC) or "@every <duration>", whose slots
// are the multiples of the duration since the Unix epoch.
type Spec struct {
	every                         time.Duration
	minute, hour, dom, month, dow uint64
	domAny, dowAny                bool
}

// ParseSpec parses a schedule expression.
func ParseSpec(expr string) (Spec, error) {
	expr = strings.TrimSpace(expr)
	if rest, ok := strings.CutPrefix(expr, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || d <= 0 {
			return Spec{}, fmt.Errorf("invalid @every duration %q", rest)
		}
		return Spec{every: d}, nil
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return Spec{}, fmt.Errorf("cron expression %q needs five fields", expr)
	}
	var s Spec
	var err error
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	targets := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, f := range fields {
		if *targets[i], err = parseField(f, bounds[i][0], bounds[i][1]); err != nil {
			return Spec{}, fmt.Errorf("cron field %d (%q): %w", i+1, f, err)
		}
	}
	// Sunday is 0 and 7.
	if s.dow&(1<<7) != 0 {
		s.dow |= 1
	}
	s.domAny, s.dowAny = fields[2] == "*", fields[4] == "*"
	return s, nil
}

func parseField(f string, lo, hi int) (uint64, error) {
	var set uint64
	for _, part := range strings.Split(f, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
		}
		from, to := lo, hi
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err1, err2 error
			from, err1 = strconv.Atoi(a)
			to, err2 = strconv.Atoi(b)
			if err1 != nil || err2 != nil {
				return 0, fmt.Errorf("invalid range %q", rng)
			}
		default:
			n, err := strconv.Atoi(rng)
			if err != nil {
				return 0, fmt.Errorf("invalid value %q", rng)
			}
			from, to = n, n
			if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return 0, fmt.Errorf("%d-%d outside %d-%d", from, to, lo, hi)
		}
		for v := from; v <= to; v += step {
			set |= 1 << uint(v)
		}
	}
	if set == 0 {
		return 0, errors.New("empty field")
	}
	return set, nil
}

// Next returns the first slot after t.
func (s *Spec) Next(t time.Time) time.Time {
	t = t.UTC()
	if s.every > 0 {
		n := t.UnixNano() / int64(s.every)
		return time.Unix(0, (n+1)*int64(s.every)).UTC()
	}
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = t.Truncate(time.Hour).Add(time.Hour)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// dayMatches follows cron: with both day fields restricted, either may
// match.
func (s *Spec) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dow
	case s.dowAny:
		return dom
	}
	return dom || dow
}

// Slots returns the slots in (after, until], oldest first, at most limit
// of the newest ones, and how many older ones it left out.
func (s *Spec) Slots(after, until time.Time, limit int) (slots []time.Time, skipped int) {
	for t := s.Next(after); !t.IsZero() && !t.After(until); t = s.Next(t) {
		slots = append(slots, t)
		if len(slots) > limit {
			slots = slots[1:]
			skipped++
		}
	}
	return slots, skipped
}
