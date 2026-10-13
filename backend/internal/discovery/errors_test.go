package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestClassifyCause covers WP-155 (DIS-08): error texts of the protocols
// map onto causes.
func TestClassifyCause(t *testing.T) {
	for msg, want := range map[string]string{
		"ssh: login 10.0.0.5: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]": CauseAuthFailed,
		"redfish: service root 10.0.0.9: HTTP 401: Unauthorized":                                                     CauseAuthFailed,
		"dial tcp 10.0.0.7:22: i/o timeout":                                                                          CauseTimeout,
		"dial tcp 10.0.0.7:443: connect: connection refused":                                                         CauseRefused,
		"dial udp 10.9.9.9:161: connect: no route to host":                                                           CauseUnreachable,
		"tls: failed to verify certificate: x509: certificate signed by unknown authority":                           CauseTLS,
		"snmp: SNMPv3 requires authPriv (username, auth and privacy passphrases)":                                    CauseConfig,
		"ipmi: bad checksum": CauseProtocol,
		"something odd":      CauseUnknown,
	} {
		if got := ClassifyCause(msg); got != want {
			t.Errorf("ClassifyCause(%q) = %s, want %s", msg, got, want)
		}
	}
	if ClassifyCause("x509: certificate has expired") != CauseTLS {
		t.Error("certificate errors are TLS errors")
	}
}

func TestRunResultStatesAndMetrics(t *testing.T) {
	before := testutil.ToFloat64(discoveryErrors.WithLabelValues("snmp", CauseTimeout))
	beforeFailed := testutil.ToFloat64(scopeRuns.WithLabelValues(ScopeStateFailed))

	ok := NormalizeRunResult(map[string]any{"status": "ok", "records": float64(12)})
	if ok["status"] != ScopeStateOK || ok["errors"] != nil {
		t.Errorf("ok run = %v", ok)
	}
	partial := NormalizeRunResult(map[string]any{"records": float64(3), "errors": []any{
		map[string]any{"protocol": "snmp", "target": "10.0.0.5", "message": "request timeout (after 2 retries)"},
		"ssh: ssh: unable to authenticate",
	}})
	errs, _ := partial["errors"].([]ErrorState)
	if partial["status"] != ScopeStatePartial || len(errs) != 2 || errs[0].Cause != CauseTimeout || errs[0].Target != "10.0.0.5" ||
		errs[1].Protocol != "ssh" || errs[1].Cause != CauseAuthFailed {
		t.Errorf("partial run = %+v", partial)
	}
	many := make([]any, 80)
	for i := range many {
		many[i] = map[string]any{"protocol": "snmp", "message": "timeout", "cause": "made-up"}
	}
	failed := NormalizeRunResult(map[string]any{"records": 0, "errors": many})
	if errs, _ = failed["errors"].([]ErrorState); failed["status"] != ScopeStateFailed || len(errs) != maxRunErrors || errs[0].Cause != CauseTimeout {
		t.Errorf("failed run: %v, %d errors", failed["status"], len(errs))
	}
	if got := testutil.ToFloat64(discoveryErrors.WithLabelValues("snmp", CauseTimeout)) - before; got != 1+maxRunErrors {
		t.Errorf("discovery_errors_total{snmp,timeout} rose by %v", got)
	}
	if got := testutil.ToFloat64(scopeRuns.WithLabelValues(ScopeStateFailed)) - beforeFailed; got != 1 {
		t.Errorf("discovery_scope_runs_total{failed} rose by %v", got)
	}
}

// TestScopeHealthAfterRun: a reported run shows up as the scope's health
// with the causes; a failed job carries its cause.
func TestScopeHealthAfterRun(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	col := &Collector{OrganizationID: "org-1", Name: "col", Config: map[string]any{}}
	if err := repo.RegisterCollector(ctx, col); err != nil {
		t.Fatal(err)
	}
	s := NewScope("org-1", &ScopeInput{CollectorID: col.ID, Kind: "cidr", Target: "10.0.0.0/24"})
	if err := repo.CreateScope(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetScope(ctx, "org-1", s.ID); got.Health.State != ScopeStateNever {
		t.Errorf("before the first run: %+v", got.Health)
	}
	h := NewHandler(repo, nil)
	if err := h.RecordCollectorScopeRun(ctx, "org-1", col.ID, &ScopeResultReport{ScopeID: s.ID, At: time.Now(),
		Result: map[string]any{"records": 0, "errors": []any{map[string]any{"protocol": "redfish", "target": "10.0.0.9", "message": "HTTP 401: Unauthorized"}}}}); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetScope(ctx, "org-1", s.ID)
	if got.Health.State != ScopeStateFailed || len(got.Health.Errors) != 1 || got.Health.Errors[0].Cause != CauseAuthFailed ||
		got.Health.Errors[0].Target != "10.0.0.9" {
		t.Errorf("health after a failed run: %+v", got.Health)
	}

	job := &Job{Status: "failed", ResultSummary: map[string]any{"error": "dial tcp 10.0.0.1:161: i/o timeout"}}
	if e := JobErrorOf(job); e == nil || e.Cause != CauseTimeout {
		t.Errorf("failed job error = %+v", e)
	}
	if JobErrorOf(&Job{Status: "completed"}) != nil {
		t.Error("a completed job has no error")
	}
}
