//go:build auditdefect

// Defect test found by WP-542 (t4-dis08-nachweis, DIS-08, WP-155; F-369): a host
// scope on an address that does not answer reports a clean run. runPass
// reports run errors only when Probe itself fails or when Collect fails for
// a probe hit (collectHits); a target that answers no probe produces no hit
// and therefore no error. The scope run reaches the server with
// error_count 0 and no error, last_result has no cause unreachable, and the
// scope health is ok. The real-stack case E2 of
// frontend/e2e-real/discovery-errors.spec.ts shows the same on the
// installed stack.
//
// Retest:
//
//	cd collector && TEST_NATS_SERVER_BIN=… go test -tags auditdefect -count=1 -v -run '^TestAuditUnreachableHostScopeReportsError$' ./collectorcmd/
package collectorcmd

import (
	"context"
	"testing"
	"time"
)

// TestAuditUnreachableHostScopeReportsError: a host scope with the SNMP
// plugin on 192.0.2.10 (TEST-NET-1, RFC 5737: not routed, nothing answers,
// the address of case E2) reports its run with a run error of cause
// unreachable, protocol snmp and the target (DIS-08).
func TestAuditUnreachableHostScopeReportsError(t *testing.T) {
	const target = "192.0.2.10"
	auditUsePlugins(t)
	runs := auditCaptureScopeRuns(t)
	up, _ := auditUploadSink(t)
	auditInstallBundle(bundleCredential{ID: "cred-v3", Kind: "snmp_v3", Secret: map[string]any{"username": "reticora",
		"auth_protocol": "SHA256", "auth_password": "audit-auth-pw", "priv_protocol": "AES", "priv_password": "audit-priv-pw"}})
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-000000000542", Kind: "host", Target: target, Schedule: "24h",
		CredentialIDs: []string{"cred-v3"}, Plugins: []string{"snmp"}}}, nil)

	runDueDiscovery(context.Background(), &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now())

	got := runs()
	if len(got) != 1 {
		t.Fatalf("scope runs reported = %d (%+v), want 1", len(got), got)
	}
	r := got[0].Result
	for _, e := range r.Errors {
		if e.Cause == "unreachable" && e.Protocol == "snmp" && e.Target == target {
			if r.ErrorCount < 1 {
				t.Errorf("error_count = %d with errors %+v, want at least 1", r.ErrorCount, r.Errors)
			}
			return
		}
	}
	t.Errorf("run of the host scope on the unreachable %s: records=%d error_count=%d errors=%+v; want a run error with cause unreachable, protocol snmp and target %s (DIS-08)",
		target, r.Records, r.ErrorCount, r.Errors, target)
}
