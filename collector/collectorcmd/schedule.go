package collectorcmd

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the organization's IANA zone without the host's zoneinfo
)

// Separate intervals (DIS-10): the sweep of a scope runs on the scope's
// schedule (interval or cron, default 24 h), the polls (SNMP, Redfish, SSH,
// WMI, IPMI, power, NAS) on their own interval per protocol (organization
// setting, default 15 min, never below 5 min). A cron schedule is evaluated
// in the organization's time zone (E-84, option A). The discovery loop ticks
// every minute and runs only what is due; polls target the hosts the last
// sweep of the scope found. The upload spool is flushed continuously,
// independent of discovery (NFR-03).

const (
	defaultSweepInterval = 24 * time.Hour
	defaultPollInterval  = 15 * time.Minute
	minPollInterval      = 5 * time.Minute
	defaultScheduleTick  = time.Minute
	defaultSpoolFlush    = 30 * time.Second
)

// discoveryIntervals are the organization's intervals as the server sends
// them with the scopes.
type discoveryIntervals struct {
	Sweep     string            `json:"sweep"`
	Poll      string            `json:"poll"`
	Protocols map[string]string `json:"protocols,omitempty"`
}

// parseInterval parses d and raises it to minimum; invalid values use
// fallback.
func parseInterval(d string, fallback, minimum time.Duration) time.Duration {
	v, err := time.ParseDuration(strings.TrimSpace(d))
	if err != nil || v <= 0 {
		v = fallback
	}
	if v < minimum {
		v = minimum
	}
	return v
}

// pollInterval returns the interval of a poll protocol.
func (iv *discoveryIntervals) pollInterval(protocol string) time.Duration {
	if iv == nil {
		return defaultPollInterval
	}
	if per, ok := iv.Protocols[protocol]; ok {
		return parseInterval(per, defaultPollInterval, minPollInterval)
	}
	return parseInterval(iv.Poll, defaultPollInterval, minPollInterval)
}

// schedule is the sweep schedule of a scope: an interval or a cron
// expression, evaluated in loc (nil: UTC).
type schedule struct {
	every time.Duration
	cron  *cronSpec
	loc   *time.Location
}

func parseSchedule(s string, fallback time.Duration) schedule {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d >= minPollInterval {
		return schedule{every: d}
	}
	if c, err := parseCron(s); err == nil {
		return schedule{cron: c}
	}
	return schedule{every: fallback}
}

// next returns the next run after the run at last.
func (s schedule) next(last time.Time) time.Time {
	if s.cron != nil {
		return s.cron.nextIn(last, s.loc)
	}
	return last.Add(s.every)
}

// scheduleLocations caches the loaded time zones by name: the passes are
// built every tick.
var scheduleLocations sync.Map

// scheduleLocation returns the time zone of the organization's IANA name;
// an empty or unknown name is UTC.
func scheduleLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, ok := scheduleLocations.Load(name); ok {
		return loc.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		slog.Warn("unknown organization time zone, cron schedules use UTC", "timezone", name, "error", err)
		loc = time.UTC
	}
	scheduleLocations.Store(name, loc)
	return loc
}

// scheduler remembers when each (pass, protocol) is due next.
type scheduler struct {
	mu   sync.Mutex
	next map[string]time.Time
}

func newScheduler() *scheduler { return &scheduler{next: map[string]time.Time{}} }

// due reports whether key is due at now; a due key is scheduled again with
// sched. A key seen for the first time is due at once unless seed set it.
func (s *scheduler) due(key string, now time.Time, sched schedule) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.next[key]; ok && now.Before(at) {
		return false
	}
	s.next[key] = sched.next(now)
	return true
}

// seed sets when a key seen for the first time is due: first is called only
// then, so a (re)started collector takes the run state the server holds
// (DIS-10).
func (s *scheduler) seed(key string, first func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.next[key]; !ok {
		s.next[key] = first()
	}
}

// nextRun returns when key is due next.
func (s *scheduler) nextRun(key string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next[key]
}

// hostCache keeps the hosts the last sweep of each pass found; polls
// target them.
type hostCache struct {
	mu    sync.Mutex
	hosts map[string][]string
}

func (c *hostCache) set(pass string, hosts []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hosts == nil {
		c.hosts = map[string][]string{}
	}
	c.hosts[pass] = hosts
}

func (c *hostCache) get(pass string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hosts[pass]
}

// --- cron -----------------------------------------------------------------

// cronSpec is a five-field cron expression (minute hour day month weekday).
type cronSpec struct {
	minute, hour, day, month, weekday map[int]bool
	anyDay, anyWeekday                bool
}

func parseCron(expr string) (*cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, errors.New("cron: five fields expected")
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	sets := make([]map[int]bool, 5)
	for i, f := range fields {
		set, err := cronField(f, bounds[i][0], bounds[i][1])
		if err != nil {
			return nil, err
		}
		sets[i] = set
	}
	if sets[4][7] {
		sets[4][0] = true
	}
	return &cronSpec{minute: sets[0], hour: sets[1], day: sets[2], month: sets[3], weekday: sets[4],
		anyDay: fields[2] == "*", anyWeekday: fields[4] == "*"}, nil
}

func cronField(field string, lo, hi int) (map[int]bool, error) {
	set := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return nil, errors.New("cron: invalid step")
			}
			step = n
		}
		from, to := lo, hi
		if base != "*" {
			a, b, isRange := strings.Cut(base, "-")
			var err error
			if from, err = strconv.Atoi(a); err != nil {
				return nil, errors.New("cron: invalid value")
			}
			to = from
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return nil, errors.New("cron: invalid range")
				}
			} else if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return nil, errors.New("cron: value out of range")
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return set, nil
}

// next returns the first matching minute after t (UTC).
func (c *cronSpec) next(t time.Time) time.Time { return c.nextIn(t, time.UTC) }

// nextIn returns the first minute after t whose wall clock in loc matches.
// It steps in real minutes and takes only a wall clock later than the one
// of t: a daylight-saving change neither repeats a run (the hour after the
// clocks go back) nor shifts one; a wall-clock time the change skips does
// not occur that day.
func (c *cronSpec) nextIn(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc).Truncate(time.Minute)
	after := wallClock(t)
	t = t.Add(time.Minute)
	for limit := 0; limit < 366*24*60; limit++ {
		if c.month[int(t.Month())] && c.dayMatches(t) && c.hour[t.Hour()] && c.minute[t.Minute()] && wallClock(t).After(after) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return t
}

// wallClock is the local date and time of t without its offset.
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
}

// dayMatches applies the cron rule: with both day and weekday restricted
// either may match.
func (c *cronSpec) dayMatches(t time.Time) bool {
	day, weekday := c.day[t.Day()], c.weekday[int(t.Weekday())]
	switch {
	case c.anyDay && c.anyWeekday:
		return true
	case c.anyDay:
		return weekday
	case c.anyWeekday:
		return day
	}
	return day || weekday
}

// --- spool ------------------------------------------------------------------

// runSpoolFlusher delivers the upload spool and the buffered metric
// batches (MON-02) every interval and whenever the backend became reachable
// again, independent of discovery (NFR-03).
func runSpoolFlusher(ctx context.Context, up *uploader, interval time.Duration) {
	if interval <= 0 {
		interval = defaultSpoolFlush
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		up.flushSpool(ctx)
		up.flushMetrics(ctx)
		select {
		case <-ctx.Done():
			slog.Info("spool flusher stopped")
			return
		case <-ticker.C:
		case <-up.flushNow:
		}
	}
}

// requestFlush asks the flusher to deliver the spool now (after a
// successful heartbeat).
func (u *uploader) requestFlush() {
	if u.flushNow == nil {
		return
	}
	select {
	case u.flushNow <- struct{}{}:
	default:
	}
}
