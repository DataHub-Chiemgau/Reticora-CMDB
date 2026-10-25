//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests for the
// collector's discovery passes (must fail until the defects are fixed).
//
// WP-153 (DIS-09), WP-154 (DIS-10).
//
// Findings encoded here (one test each):
//   - TestAuditScopeExpansionCompleteOrReported – WP-153 P2 "Scopes larger
//     than ~4096 addresses are silently truncated by the collector".
//   - TestAuditScopeScheduleSurvivesRestart – WP-154 P2 "Scope schedules
//     (cron/interval) are ignored at collector start: every scope sweeps
//     immediately and again after each restart". The test feeds the scope
//     list as the server would send it with the run state the server holds
//     (last_run_at/next_run_at, the audit's recommended fields).
//
// TestAuditCredentialKindRouting and TestAuditPerScopeCredentials (F-028,
// F-013) moved to discovery_credentials_test.go (WP-378),
// TestAuditWMIAuthFailureReachesScopeRun (F-029) to
// discovery_errors_test.go (WP-415),
// TestAuditClassifiedDeviceNotUploadedAsFallback (F-030) to
// discovery_classify_test.go (WP-417); the shared helpers are in
// discovery_helpers_test.go.
//
// The scope run reports travel over NATS like in production; the tests need
// TEST_NATS_URL (a plain NATS server, e.g. nats://localhost:54222).
//
// Retest:
//
//	cd collector && TEST_NATS_URL=nats://localhost:54222 go test -tags auditdefect -count=1 -v \
//	  -run 'TestAuditScopeExpansionCompleteOrReported|TestAuditScopeScheduleSurvivesRestart' ./collectorcmd/
package collectorcmd

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestAuditScopeExpansionCompleteOrReported: a scope the API accepts
// (up to 65,536 addresses) is discovered completely, or the run reports
// the truncation as a scope error (DIS-08/DIS-09).
func TestAuditScopeExpansionCompleteOrReported(t *testing.T) {
	runs := auditCaptureScopeRuns(t)
	spy := &auditSpy{name: "sweep", countOnly: true}
	auditUsePlugins(t, spy)
	scopes := []struct {
		scope collectorScope
		want  int
		first string
	}{
		{scope: collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a4", Kind: "cidr", Target: "172.17.0.0/16", Schedule: "24h", Plugins: []string{"sweep"}}, want: 65534, first: "172.17.0.1"},
		{scope: collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a5", Kind: "range", Target: "10.0.0.0-10.0.255.255", Schedule: "24h", Plugins: []string{"sweep"}}, want: 65536, first: "10.0.0.0"},
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
	reported := map[string]int{}
	for _, r := range runs() {
		reported[r.ScopeID] = r.Result.ErrorCount + len(r.Result.Errors)
	}
	for _, s := range scopes {
		got := counts[s.first]
		if got != s.want && reported[s.scope.ID] == 0 {
			t.Errorf("scope %s (%s): %d of %d addresses discovered and the run reported no error (silent truncation)",
				s.scope.ID, s.scope.Target, got, s.want)
		}
	}
}

// TestAuditScopeScheduleSurvivesRestart: after a (re)start the collector
// honours the run state the server holds: a cron or interval scope that ran
// recently is not due; only a never-run scope sweeps at once (DIS-10).
func TestAuditScopeScheduleSurvivesRestart(t *testing.T) {
	spy := &auditSpy{name: "sweep"}
	auditUsePlugins(t, spy)
	now := time.Date(2026, 10, 15, 10, 7, 0, 0, time.UTC)
	serverScopes := `[
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000a6","kind":"host","target":"192.0.2.21","schedule":"0 2 * * *","plugins":["sweep"],"credential_ids":[],
	   "last_run_at":"2026-10-15T02:00:00Z","next_run_at":"2026-10-16T02:00:00Z"},
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000a7","kind":"host","target":"192.0.2.22","schedule":"24h","plugins":["sweep"],"credential_ids":[],
	   "last_run_at":"2026-10-15T09:07:00Z","next_run_at":"2026-10-16T09:07:00Z"},
	  {"id":"6f0b6f0e-0000-4000-8000-0000000000a8","kind":"host","target":"192.0.2.23","schedule":"24h","plugins":["sweep"],"credential_ids":[]}
	]`
	var scopes []collectorScope
	if err := json.Unmarshal([]byte(serverScopes), &scopes); err != nil {
		t.Fatal(err)
	}
	collectorScopes.set(scopes, nil)
	// A fresh scheduler is what a collector start (or auto-update restart) has.
	runDueDiscovery(context.Background(), &collectorConfig{}, nil, newScheduler(), &hostCache{}, now)

	cases := []struct {
		name, target string
		wantSweep    bool
	}{
		{"cron 0 2 * * * scope that ran at 02:00 is not due at 10:07", "192.0.2.21", false},
		{"24h scope that ran an hour ago is not due after a restart", "192.0.2.22", false},
		{"never-run scope sweeps at once (control)", "192.0.2.23", true},
	}
	for _, tc := range cases {
		if swept := len(spy.seen(tc.target)) > 0; swept != tc.wantSweep {
			t.Errorf("%s: swept=%v at %s, want %v", tc.name, swept, now.Format(time.RFC3339), tc.wantSweep)
		}
	}
}
