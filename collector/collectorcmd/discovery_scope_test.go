// Scope expansion of the collector's discovery passes (WP-494, DIS-09):
// every scope the API accepts (up to 65,536 addresses) is discovered
// completely; a larger one is cut and the run reports it (F-106).
//
// The scope run reports travel over NATS like in production; the test needs
// TEST_NATS_URL or TEST_NATS_SERVER_BIN.
//
// Retest:
//
//	cd collector && TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v \
//	  -run 'TestAuditScopeExpansionCompleteOrReported' ./collectorcmd/
package collectorcmd

import (
	"context"
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
