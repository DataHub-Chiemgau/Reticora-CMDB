// Scope expansion of the collector's discovery passes (WP-494, DIS-09):
// every scope the API accepts (up to 65,536 addresses) is discovered
// completely; a larger one is cut and the run reports it (F-106).
//
// Scope schedules after a (re)start (WP-495, DIS-10): the sweep of a scope
// starts from the run state the server holds, not at once (F-109).
//
// The scope run reports travel over NATS like in production; the expansion
// test needs TEST_NATS_URL or TEST_NATS_SERVER_BIN.
//
// Retest:
//
//	cd collector && TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v \
//	  -run 'TestAuditScopeExpansionCompleteOrReported|TestAuditScopeScheduleSurvivesRestart' ./collectorcmd/
package collectorcmd

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestAuditScopeExpansionCompleteOrReported: a scope the API accepts
// (up to 65,536 addresses) is discovered completely without a run error;
// a larger scope is not cut in silence but reported as a run error with
// cause scope_truncated and its target (DIS-08/DIS-09).
func TestAuditScopeExpansionCompleteOrReported(t *testing.T) {
	runs := auditCaptureScopeRuns(t)
	spy := &auditSpy{name: "sweep", countOnly: true}
	auditUsePlugins(t, spy)
	scopes := []struct {
		scope     collectorScope
		want      int
		first     string
		truncated bool
	}{
		{scope: collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a4", Kind: "cidr", Target: "172.17.0.0/16", Schedule: "24h", Plugins: []string{"sweep"}}, want: 65534, first: "172.17.0.1"},
		{scope: collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a5", Kind: "range", Target: "10.0.0.0-10.0.255.255", Schedule: "24h", Plugins: []string{"sweep"}}, want: 65536, first: "10.0.0.0"},
		// Larger than the API allows (e.g. a scope stored by an older server).
		{scope: collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a9", Kind: "cidr", Target: "10.64.0.0/15", Schedule: "24h", Plugins: []string{"sweep"}}, want: maxScopeAddresses, first: "10.64.0.1", truncated: true},
		{scope: collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000aa", Kind: "range", Target: "10.128.0.0-10.129.0.0", Schedule: "24h", Plugins: []string{"sweep"}}, want: maxScopeAddresses, first: "10.128.0.0", truncated: true},
	}
	var list []collectorScope
	for _, s := range scopes {
		list = append(list, s.scope)
	}
	collectorScopes.set(list, nil)
	runDueDiscovery(context.Background(), &collectorConfig{}, nil, newScheduler(), &hostCache{}, time.Now())

	spy.mu.Lock()
	counts := map[string]int{}
	for i, f := range spy.firsts {
		counts[f] = spy.counts[i]
	}
	spy.mu.Unlock()
	reported := map[string][]runError{}
	for _, r := range runs() {
		reported[r.ScopeID] = append(reported[r.ScopeID], r.Result.Errors...)
	}
	for _, s := range scopes {
		if got := counts[s.first]; got != s.want {
			t.Errorf("scope %s (%s): %d addresses discovered, want %d", s.scope.ID, s.scope.Target, got, s.want)
		}
		errs := reported[s.scope.ID]
		if !s.truncated {
			if len(errs) != 0 {
				t.Errorf("scope %s (%s): run errors %+v, want none", s.scope.ID, s.scope.Target, errs)
			}
			continue
		}
		if len(errs) != 1 || errs[0].Cause != "scope_truncated" || errs[0].Target != s.scope.Target {
			t.Errorf("scope %s (%s): run errors %+v, want one with cause scope_truncated and the target (no silent truncation)",
				s.scope.ID, s.scope.Target, errs)
		}
	}
}

// TestAuditScopeScheduleSurvivesRestart: after a (re)start the collector
// honours the run state the server holds: a cron or interval scope that ran
// recently is not due; only a never-run scope sweeps at once (DIS-10,
// F-109). Without next_run_at a cron scope waits for its next match in the
// organization's time zone and an interval scope for one interval after its
// last run.
func TestAuditScopeScheduleSurvivesRestart(t *testing.T) {
	spy := &auditSpy{name: "sweep"}
	auditUsePlugins(t, spy)
	now := time.Date(2026, 10, 15, 10, 7, 0, 0, time.UTC)
	serverScopes := `[
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000a6","kind":"host","target":"192.0.2.21","schedule":"0 2 * * *","plugins":["sweep"],"credential_ids":[],
	   "last_run_at":"2026-10-15T02:00:00Z","next_run_at":"2026-10-16T02:00:00Z"},
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000a7","kind":"host","target":"192.0.2.22","schedule":"24h","plugins":["sweep"],"credential_ids":[],
	   "last_run_at":"2026-10-15T09:07:00Z","next_run_at":"2026-10-16T09:07:00Z"},
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000a8","kind":"host","target":"192.0.2.23","schedule":"24h","plugins":["sweep"],"credential_ids":[]},
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000ab","kind":"host","target":"192.0.2.24","schedule":"0 2 * * *","plugins":["sweep"],"credential_ids":[],
	   "timezone":"Europe/Berlin"},
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000ac","kind":"host","target":"192.0.2.25","schedule":"6h","plugins":["sweep"],"credential_ids":[],
	   "last_run_at":"2026-10-15T08:07:00Z"}
	]`
	var scopes []collectorScope
	if err := json.Unmarshal([]byte(serverScopes), &scopes); err != nil {
		t.Fatal(err)
	}
	collectorScopes.set(scopes, nil)
	// A fresh scheduler is what a collector start (or auto-update restart) has.
	sched := newScheduler()
	runDueDiscovery(context.Background(), &collectorConfig{}, nil, sched, &hostCache{}, now)

	cases := []struct {
		name, id, target string
		wantSweep        bool
		wantNext         string
	}{
		{"cron 0 2 * * * scope that ran at 02:00 is not due at 10:07", "6f0b6f0e-0000-4000-8000-0000000000a6", "192.0.2.21", false, "2026-10-16T02:00:00Z"},
		{"24h scope that ran an hour ago is not due after a restart", "6f0b6f0e-0000-4000-8000-0000000000a7", "192.0.2.22", false, "2026-10-16T09:07:00Z"},
		{"never-run scope sweeps at once (control)", "6f0b6f0e-0000-4000-8000-0000000000a8", "192.0.2.23", true, "2026-10-16T10:07:00Z"},
		{"cron scope without next_run_at waits for its next match (02:00 in Berlin)", "6f0b6f0e-0000-4000-8000-0000000000ab", "192.0.2.24", false, "2026-10-16T00:00:00Z"},
		{"6h scope without next_run_at is due one interval after its last run", "6f0b6f0e-0000-4000-8000-0000000000ac", "192.0.2.25", false, "2026-10-15T14:07:00Z"},
	}
	for _, tc := range cases {
		if swept := len(spy.seen(tc.target)) > 0; swept != tc.wantSweep {
			t.Errorf("%s: swept=%v at %s, want %v", tc.name, swept, now.Format(time.RFC3339), tc.wantSweep)
		}
		if next := sched.nextRun(tc.id + "/sweep").UTC().Format(time.RFC3339); next != tc.wantNext {
			t.Errorf("%s: next sweep %s, want %s", tc.name, next, tc.wantNext)
		}
	}
}
