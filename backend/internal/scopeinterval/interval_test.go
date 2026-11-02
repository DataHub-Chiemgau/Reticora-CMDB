package scopeinterval_test

import (
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/scopeinterval"
)

// TestScopeInterval covers WP-497 (TOP-02, F-111): a duration is its value;
// a cron schedule is the gap next_run_at − last_run_at of its last run, or
// without a run the gap between its next two slots; a value that cannot be
// read falls back to the 24 h default.
func TestScopeInterval(t *testing.T) {
	day := 24 * time.Hour
	// Monday 2026-10-26 12:00 UTC.
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		scope scopeinterval.Scope
		want  time.Duration
	}{
		{"duration 168h", scopeinterval.Scope{Schedule: "168h"}, 7 * day},
		{"duration 24h with spaces", scopeinterval.Scope{Schedule: " 24h "}, day},
		{"duration 15m ignores the run state", scopeinterval.Scope{Schedule: "15m",
			LastRun: now.Add(-3 * day), NextRun: now.Add(4 * day)}, 15 * time.Minute},
		{"weekly cron without run: next two slots", scopeinterval.Scope{Schedule: "0 3 * * 0"}, 7 * day},
		{"weekly cron with run state", scopeinterval.Scope{Schedule: "0 3 * * 0",
			LastRun: now.Add(-3 * day), NextRun: now.Add(4 * day)}, 7 * day},
		// Next slots after now: 2026-11-01 and 2026-12-01.
		{"monthly cron without run: next two slots", scopeinterval.Scope{Schedule: "0 3 1 * *"}, 30 * day},
		{"monthly cron with run state", scopeinterval.Scope{Schedule: "0 3 1 * *",
			LastRun: now.Add(-10 * day), NextRun: now.Add(21 * day)}, 31 * day},
		{"daily cron without run", scopeinterval.Scope{Schedule: "0 2 * * *"}, day},
		{"cron with only last_run_at: next two slots", scopeinterval.Scope{Schedule: "0 3 * * 0",
			LastRun: now.Add(-3 * day)}, 7 * day},
		{"cron with a gap that is not positive: next two slots", scopeinterval.Scope{Schedule: "0 3 * * 0",
			LastRun: now.Add(day), NextRun: now.Add(-day)}, 7 * day},
		{"invalid: empty", scopeinterval.Scope{}, scopeinterval.Default},
		{"invalid: text", scopeinterval.Scope{Schedule: "weekly"}, scopeinterval.Default},
		{"invalid: four cron fields", scopeinterval.Scope{Schedule: "0 3 * *"}, scopeinterval.Default},
		{"invalid: cron field out of range", scopeinterval.Scope{Schedule: "61 3 * * 0",
			LastRun: now.Add(-3 * day), NextRun: now.Add(4 * day)}, scopeinterval.Default},
		{"invalid: zero duration", scopeinterval.Scope{Schedule: "0s"}, scopeinterval.Default},
		{"invalid: negative duration", scopeinterval.Scope{Schedule: "-168h"}, scopeinterval.Default},
		{"invalid: cron without a slot (31 February)", scopeinterval.Scope{Schedule: "0 3 31 2 *"}, scopeinterval.Default},
	}
	for _, tc := range cases {
		if got := scopeinterval.Of(tc.scope, now); got != tc.want {
			t.Errorf("%s: Of(%q) = %v, want %v", tc.name, tc.scope.Schedule, got, tc.want)
		}
	}
}

// TestScopeIntervalShortest covers the interval of a CI several scopes
// cover: the shortest; without a scope the organization's fallback.
func TestScopeIntervalShortest(t *testing.T) {
	day := 24 * time.Hour
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if got := scopeinterval.Shortest(nil, 6*time.Hour, now); got != 6*time.Hour {
		t.Errorf("no scope: %v, want the fallback 6h", got)
	}
	scopes := []scopeinterval.Scope{{Schedule: "0 3 1 * *"}, {Schedule: "0 3 * * 0"}, {Schedule: "336h"}}
	if got := scopeinterval.Shortest(scopes, time.Hour, now); got != 7*day {
		t.Errorf("monthly, weekly cron and 336h: %v, want 168h", got)
	}
	if got := scopeinterval.Shortest([]scopeinterval.Scope{{Schedule: "weekly"}}, time.Hour, now); got != scopeinterval.Default {
		t.Errorf("invalid schedule: %v, want the default %v, not the fallback", got, scopeinterval.Default)
	}
}

// TestScopesZip covers the mapping of the aggregated query columns: a nil
// time is no run.
func TestScopesZip(t *testing.T) {
	last := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	next := last.Add(7 * 24 * time.Hour)
	got := scopeinterval.Scopes([]string{"0 3 * * 0", "24h"}, []*time.Time{&last, nil}, []*time.Time{&next, nil})
	want := []scopeinterval.Scope{{Schedule: "0 3 * * 0", LastRun: last, NextRun: next}, {Schedule: "24h"}}
	if len(got) != len(want) {
		t.Fatalf("Scopes: %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Scopes[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
