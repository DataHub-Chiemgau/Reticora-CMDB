// Package scopeinterval derives the interval of a discovery scope from its
// schedule and run state (DIS-10, TOP-02). The offline detector (REC-09,
// LCY-05) and the discovery edge cleanup (TOP-02) share it, so a weekly
// cron scope counts as a week for both and not as the 24 h default (F-111).
package scopeinterval

import (
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// Default is the interval of a schedule that cannot be read (DIS-10).
const Default = 24 * time.Hour

// Scope is the schedule of one discovery scope with the run state the
// server holds; a zero LastRun or NextRun means none.
type Scope struct {
	Schedule         string
	LastRun, NextRun time.Time
}

// Of returns the interval of s:
//
//   - a duration (24h, 168h) is its value;
//   - a cron expression is the gap next_run_at − last_run_at the collector
//     recorded with its last run; without such a run (never ran, or the
//     state is incomplete) the gap between the next two cron slots after
//     now;
//   - anything else, or a gap that is not positive, is Default.
//
// The cron slots are evaluated in UTC; the collector evaluates the
// schedule in the organization's time zone (WP-495), which moves a slot
// but changes the gap between two slots by at most a clock change.
func Of(s Scope, now time.Time) time.Duration {
	expr := strings.TrimSpace(s.Schedule)
	if d, err := time.ParseDuration(expr); err == nil {
		if d > 0 {
			return d
		}
		return Default
	}
	spec, err := jobs.ParseSpec(expr)
	if err != nil {
		return Default
	}
	if !s.LastRun.IsZero() && !s.NextRun.IsZero() {
		if gap := s.NextRun.Sub(s.LastRun); gap > 0 {
			return gap
		}
	}
	first := spec.Next(now)
	if first.IsZero() {
		return Default
	}
	second := spec.Next(first)
	if second.IsZero() {
		return Default
	}
	return second.Sub(first)
}

// Shortest returns the shortest interval of scopes, the interval of the CI
// they cover; without a scope it returns fallback (the organization's
// sweep interval).
func Shortest(scopes []Scope, fallback time.Duration, now time.Time) time.Duration {
	best := time.Duration(0)
	for _, s := range scopes {
		if d := Of(s, now); best == 0 || d < best {
			best = d
		}
	}
	if best == 0 {
		return fallback
	}
	return best
}

// Scopes zips the per-scope columns of a query (schedule, last_run_at,
// next_run_at aggregated in the same order); a nil time is no run.
func Scopes(schedules []string, lastRuns, nextRuns []*time.Time) []Scope {
	out := make([]Scope, len(schedules))
	for i, s := range schedules {
		out[i].Schedule = s
		if i < len(lastRuns) && lastRuns[i] != nil {
			out[i].LastRun = *lastRuns[i]
		}
		if i < len(nextRuns) && nextRuns[i] != nil {
			out[i].NextRun = *nextRuns[i]
		}
	}
	return out
}
