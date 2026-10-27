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
// Ad-hoc discovery jobs (WP-632, JOB-01, F-358): the collector takes its
// open jobs over the scope channel, runs each one the server lets run with
// the plugins of the job type, uploads the records and reports the result.
//
// Retest:
//
//	cd collector && TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v \
//	  -run 'TestAuditScopeExpansionCompleteOrReported|TestAuditScopeScheduleSurvivesRestart|TestDiscoveryJobRunsOnCollector' ./collectorcmd/
package collectorcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// jobReport is a job status as the server receives it.
type jobReport struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Error  string `json:"error"`
	Result struct {
		Records int        `json:"records"`
		Scopes  int        `json:"scopes"`
		Errors  []runError `json:"errors"`
	} `json:"result"`
}

// TestDiscoveryJobRunsOnCollector: the collector asks the server for its
// open jobs over NATS, reports each one running and runs it only once the
// server recorded that; a sweep job runs the sweep, a full job all plugins
// of the scope, the records reach the ingest, and the result goes back
// with the record count. A poll job on a scope without poll plugins fails
// with cause config_error; a job the server does not let run is skipped.
func TestDiscoveryJobRunsOnCollector(t *testing.T) {
	const target = "192.0.2.31"
	sweepSpy := &auditSpy{name: "sweep", alive: func(targets []string) []plugins.Result {
		out := make([]plugins.Result, 0, len(targets))
		for _, tg := range targets {
			out = append(out, plugins.Result{IP: tg, Name: tg})
		}
		return out
	}}
	auditUsePlugins(t, sweepSpy)
	up, uploaded := auditUploadSink(t)
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000b1", Kind: "host", Target: target,
		Schedule: "24h", Plugins: []string{"sweep"}}}, nil)

	const (
		jobSweep   = "7a1c0000-0000-4000-8000-000000000001"
		jobRefused = "7a1c0000-0000-4000-8000-000000000002"
		jobPoll    = "7a1c0000-0000-4000-8000-000000000003"
		jobFull    = "7a1c0000-0000-4000-8000-000000000004"
	)
	nc, err := nats.Connect(auditNATSURL(t), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	subject := fmt.Sprintf("collector.job-%d.c1", time.Now().UnixNano())
	var mu sync.Mutex
	var reports []jobReport
	requests := 0
	if _, err = nc.Subscribe(subject+".jobs.request", func(m *nats.Msg) {
		mu.Lock()
		requests++
		mu.Unlock()
		_ = m.Respond([]byte(`{"jobs":[{"id":"` + jobSweep + `","job_type":"sweep","config":{}},{"id":"` + jobRefused +
			`","job_type":"full","config":{}},{"id":"` + jobPoll + `","job_type":"poll","config":{}},{"id":"` + jobFull +
			`","job_type":"full","config":{}}]}`))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = nc.Subscribe(subject+".jobs.status", func(m *nats.Msg) {
		var r jobReport
		if json.Unmarshal(m.Data, &r) != nil {
			_ = m.Respond([]byte(`{"error":"invalid report"}`))
			return
		}
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
		if r.JobID == jobRefused {
			_ = m.Respond([]byte(`{"error":"not recorded"}`)) // another request took it
			return
		}
		_ = m.Respond([]byte(`{"status":"recorded"}`))
	}); err != nil {
		t.Fatal(err)
	}
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	scopeChannel.Store(&scopeChannelState{nc: nc, subject: subject})
	t.Cleanup(func() { scopeChannel.Store(nil); nc.Close() })

	ran := runOpenJobs(context.Background(), &collectorConfig{}, up, &hostCache{})

	mu.Lock()
	got := slices.Clone(reports)
	mu.Unlock()
	if requests != 1 || ran != 3 {
		t.Errorf("jobs requests = %d, jobs run = %d; want 1 request and 3 jobs run (the refused one skipped)", requests, ran)
	}
	var sequence []string
	final := map[string]jobReport{}
	for _, r := range got {
		sequence = append(sequence, r.JobID[len(r.JobID)-1:]+":"+r.Status)
		if r.Status != jobRunning {
			final[r.JobID] = r
		}
	}
	if want := []string{"1:running", "1:completed", "2:running", "3:running", "3:failed", "4:running", "4:completed"}; !slices.Equal(sequence, want) {
		t.Fatalf("reported statuses %v, want %v (running before each run, nothing more for the refused job)", sequence, want)
	}
	for _, id := range []string{jobSweep, jobFull} {
		r := final[id]
		if r.Result.Records != 1 || r.Result.Scopes != 1 || len(r.Result.Errors) != 0 || r.Error != "" {
			t.Errorf("result of job %s = %+v, want 1 record from 1 scope without errors", id, r)
		}
	}
	poll := final[jobPoll]
	if poll.Error == "" || len(poll.Result.Errors) != 1 || poll.Result.Errors[0].Cause != "config_error" || poll.Result.Scopes != 0 {
		t.Errorf("poll job on a sweep-only scope = %+v, want failed with one config_error and no scope run", poll)
	}
	sweepSpy.mu.Lock()
	probes := slices.Clone(sweepSpy.firsts)
	sweepSpy.mu.Unlock()
	if !slices.Equal(probes, []string{target, target}) {
		t.Errorf("sweep probes %v, want two (sweep job and full job) of %s", probes, target)
	}
	recs := uploaded()
	if len(recs) != 2 {
		t.Fatalf("uploaded %d records, want 2 (one per job that ran a plugin)", len(recs))
	}
	for _, r := range recs {
		if r.TargetIP != target || r.ScopeID != "6f0b6f0e-0000-4000-8000-0000000000b1" {
			t.Errorf("uploaded record target %q scope %q, want %s of the scope", r.TargetIP, r.ScopeID, target)
		}
	}
}

// TestDiscoveryJobProtocols: the plugins of each job type.
func TestDiscoveryJobProtocols(t *testing.T) {
	scope := []string{"snmp", "sweep", "ssh"}
	for _, tc := range []struct {
		jobType string
		plugins []string
		want    []string
	}{
		{"sweep", scope, []string{"sweep"}},
		{"poll", scope, []string{"snmp", "ssh"}},
		{"full", scope, []string{"sweep", "snmp", "ssh"}},
		{"full", []string{"snmp"}, []string{"snmp"}},
		{"poll", []string{"sweep"}, []string{}},
		{"unknown", scope, nil},
	} {
		if got := jobProtocols(tc.jobType, tc.plugins); !slices.Equal(got, tc.want) {
			t.Errorf("jobProtocols(%q, %v) = %v, want %v", tc.jobType, tc.plugins, got, tc.want)
		}
	}
	if !slices.Equal(scope, []string{"snmp", "sweep", "ssh"}) {
		t.Errorf("jobProtocols changed the scope's plugins: %v", scope)
	}
}

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
