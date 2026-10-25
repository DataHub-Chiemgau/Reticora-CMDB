//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests for the
// collector's discovery passes (must fail until the defects are fixed).
//
// WP-151 (DIS-02), WP-153 (DIS-09), WP-154 (DIS-10).
//
// Findings encoded here (one test each):
//   - TestAuditScopeExpansionCompleteOrReported – WP-153 P2 "Scopes larger
//     than ~4096 addresses are silently truncated by the collector".
//   - TestAuditScopeScheduleSurvivesRestart – WP-154 P2 "Scope schedules
//     (cron/interval) are ignored at collector start: every scope sweeps
//     immediately and again after each restart". The test feeds the scope
//     list as the server would send it with the run state the server holds
//     (last_run_at/next_run_at, the audit's recommended fields).
//   - TestAuditClassifiedDeviceNotUploadedAsFallback – WP-151 P1 "Every swept
//     device gets a (recurring) false unclassified_device review" (collector
//     part: the evidence-less sweep record of an SNMP-classified device is
//     uploaded as classified_by=fallback).
//
// TestAuditCredentialKindRouting and TestAuditPerScopeCredentials (F-028,
// F-013) moved to discovery_credentials_test.go (WP-378),
// TestAuditWMIAuthFailureReachesScopeRun (F-029) to
// discovery_errors_test.go (WP-415); the shared helpers are in
// discovery_helpers_test.go.
//
// The scope run reports travel over NATS like in production; the tests need
// TEST_NATS_URL (a plain NATS server, e.g. nats://localhost:54222).
//
// Retest:
//
//	cd collector && TEST_NATS_URL=nats://localhost:54222 go test -tags auditdefect -count=1 -v \
//	  -run 'TestAuditScopeExpansionCompleteOrReported|TestAuditScopeScheduleSurvivesRestart|TestAuditClassifiedDeviceNotUploadedAsFallback' ./collectorcmd/
package collectorcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/sweep"
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

// TestAuditClassifiedDeviceNotUploadedAsFallback: a device that SNMP
// classified (Catalyst sysObjectID) is not uploaded a second time as an
// evidence-less generic_device with classified_by=fallback from its sweep
// record, which makes the server open a false unclassified_device review
// every sweep (DIS-02).
func TestAuditClassifiedDeviceNotUploadedAsFallback(t *testing.T) {
	up, uploaded := auditUploadSink(t)
	ln, ip := auditLoopback(t, 22)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	realSweep := sweep.New()
	realSweep.TCPPorts = []int{22}
	realSweep.Timeout = time.Second
	hit := func(targets []string) []plugins.Result {
		var out []plugins.Result
		for _, tg := range targets {
			out = append(out, plugins.Result{IP: tg, Name: tg})
		}
		return out
	}
	sweepSpy := &auditSpy{name: "sweep", alive: hit, collect: func(ctx context.Context, target string) (*plugins.DeviceRecord, error) {
		return realSweep.Collect(ctx, target, nil)
	}}
	snmpSpy := &auditSpy{name: "snmp", alive: hit, collect: func(_ context.Context, target string) (*plugins.DeviceRecord, error) {
		return plugins.CollectRecord("snmp", &plugins.Result{CIType: "network.device", Name: "core-sw-1", IP: target, Attributes: map[string]any{
			"sysObjectID": "1.3.6.1.4.1.9.1.2494",
			"sysDescr":    "Cisco IOS XE Software, Catalyst L3 Switch Software (CAT9K_IOSXE), C9300-48P",
			"sysName":     "core-sw-1",
		}}, nil, time.Now())
	}}
	auditUsePlugins(t, sweepSpy, snmpSpy)
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000aa", Kind: "host", Target: ip, Schedule: "24h", Plugins: []string{"sweep", "snmp"}}}, nil)
	runDueDiscovery(context.Background(), &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now())

	recs := uploaded()
	var classified, fallback []string
	for _, r := range recs {
		if r.TargetIP != ip {
			continue
		}
		by, _ := r.Attributes["classified_by"].(string)
		entry := fmt.Sprintf("%s:%s/%s", r.Source, r.Classification.CIType, by)
		if by == "fallback" {
			fallback = append(fallback, entry)
		} else {
			classified = append(classified, entry)
		}
	}
	if len(classified) == 0 {
		t.Fatalf("control: the SNMP record of %s was not uploaded as classified (uploaded %d records: classified=%v fallback=%v)", ip, len(recs), classified, fallback)
	}
	if len(fallback) > 0 {
		t.Errorf("DIS-02 violated: the SNMP-classified device %s was also uploaded as %v next to %v; the server opens an unclassified_device review for it on every sweep",
			ip, fallback, classified)
	}
}
