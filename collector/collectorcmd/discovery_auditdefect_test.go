//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests for the
// collector's discovery passes (must fail until the defects are fixed).
//
// WP-147 (COL-02), WP-151 (DIS-02), WP-152 (DIS-04), WP-153 (DIS-09),
// WP-154 (DIS-10), WP-155 (DIS-08).
//
// Findings encoded here (one test each):
//   - TestAuditCredentialKindRouting – WP-152 P1 "Vault credentials of kind
//     'wmi' never reach the WMI plugin; SSH credentials go to WMI and NAS
//     targets instead" (plugin.go aliases wmi/nas -> ssh, bundle.go
//     namespaces kind wmi as "wmi").
//   - TestAuditPerScopeCredentials – WP-153 P1 / WP-147 P2 "Collector ignores
//     the scope's credential_ids: the first credential per protocol is used
//     for all scopes (cross-client)".
//   - TestAuditScopeExpansionCompleteOrReported – WP-153 P2 "Scopes larger
//     than ~4096 addresses are silently truncated by the collector".
//   - TestAuditScopeScheduleSurvivesRestart – WP-154 P2 "Scope schedules
//     (cron/interval) are ignored at collector start: every scope sweeps
//     immediately and again after each restart". The test feeds the scope
//     list as the server would send it with the run state the server holds
//     (last_run_at/next_run_at, the audit's recommended fields).
//   - TestAuditWMIAuthFailureReachesScopeRun – WP-155 P2 "Credential failures
//     of WMI and SNMPv3 are invisible: the scope run reports ok" (WMI part).
//   - TestAuditClassifiedDeviceNotUploadedAsFallback – WP-151 P1 "Every swept
//     device gets a (recurring) false unclassified_device review" (collector
//     part: the evidence-less sweep record of an SNMP-classified device is
//     uploaded as classified_by=fallback).
//
// The scope run reports travel over NATS like in production; the tests need
// TEST_NATS_URL (a plain NATS server, e.g. nats://localhost:54222).
//
// Retest:
//
//	cd collector && TEST_NATS_URL=nats://localhost:54222 go test -tags auditdefect -count=1 -v \
//	  -run 'TestAuditCredentialKindRouting|TestAuditPerScopeCredentials|TestAuditScopeExpansionCompleteOrReported|TestAuditScopeScheduleSurvivesRestart|TestAuditWMIAuthFailureReachesScopeRun|TestAuditClassifiedDeviceNotUploadedAsFallback' ./collectorcmd/
package collectorcmd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/sweep"
)

// auditSpy is a protocol plugin that records what discovery hands it.
type auditSpy struct {
	name string
	// alive returns the probe hits; nil means nothing answers.
	alive   func(targets []string) []plugins.Result
	collect func(ctx context.Context, target string) (*plugins.DeviceRecord, error)
	// countOnly records only the number of targets per probe.
	countOnly bool

	mu     sync.Mutex
	counts []int
	firsts []string
	creds  map[string][]map[string]string // target -> credentials seen
}

func (s *auditSpy) Name() string { return s.name }

func (s *auditSpy) Probe(_ context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	s.mu.Lock()
	s.counts = append(s.counts, len(targets))
	if len(targets) > 0 {
		s.firsts = append(s.firsts, targets[0])
	}
	if !s.countOnly {
		if s.creds == nil {
			s.creds = map[string][]map[string]string{}
		}
		for _, t := range targets {
			s.creds[t] = append(s.creds[t], maps.Clone(creds))
		}
	}
	s.mu.Unlock()
	if s.alive == nil {
		return nil, nil
	}
	return s.alive(targets), nil
}

func (s *auditSpy) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.DeviceRecord, error) {
	s.mu.Lock()
	if !s.countOnly {
		if s.creds == nil {
			s.creds = map[string][]map[string]string{}
		}
		s.creds[target] = append(s.creds[target], maps.Clone(creds))
	}
	s.mu.Unlock()
	if s.collect == nil {
		return nil, nil
	}
	return s.collect(ctx, target)
}

func (s *auditSpy) seen(target string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creds[target]
}

// auditUsePlugins swaps plugins into the registry for one test and resets
// the scope list, the bundle and the scope channel afterwards.
func auditUsePlugins(t *testing.T, plugs ...plugins.Plugin) {
	t.Helper()
	for _, p := range plugs {
		previous, had := pluginRegistry[p.Name()]
		pluginRegistry[p.Name()] = p
		t.Cleanup(func() {
			if had {
				pluginRegistry[p.Name()] = previous
			} else {
				delete(pluginRegistry, p.Name())
			}
		})
	}
	t.Cleanup(func() {
		collectorScopes = &scopeCache{}
		collectorBundle.set(nil)
	})
}

// auditInstallBundle installs an opened credential bundle the way
// syncBundle does after fetchBundle.
func auditInstallBundle(creds ...bundleCredential) {
	collectorBundle.set((&bundleContent{Credentials: creds}).namespaced())
}

// auditScopeRun is a scope run report as the server receives it.
type auditScopeRun struct {
	ScopeID string `json:"scope_id"`
	Result  struct {
		Records    int        `json:"records"`
		ErrorCount int        `json:"error_count"`
		Errors     []runError `json:"errors"`
	} `json:"result"`
}

// auditCaptureScopeRuns connects the scope channel to TEST_NATS_URL and
// answers the collector's scopes.result requests like the server.
func auditCaptureScopeRuns(t *testing.T) func() []auditScopeRun {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required: the scope run reports travel over NATS")
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	subject := fmt.Sprintf("collector.audit-%d.c%d", time.Now().UnixNano(), rand.IntN(1_000_000))
	var mu sync.Mutex
	var runs []auditScopeRun
	sub, err := nc.Subscribe(subject+".scopes.result", func(m *nats.Msg) {
		var r auditScopeRun
		if json.Unmarshal(m.Data, &r) == nil {
			mu.Lock()
			runs = append(runs, r)
			mu.Unlock()
		}
		_ = m.Respond([]byte(`{"ok":true}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	scopeChannel.Store(&scopeChannelState{nc: nc, subject: subject})
	t.Cleanup(func() {
		scopeChannel.Store(nil)
		_ = sub.Unsubscribe()
		nc.Close()
	})
	return func() []auditScopeRun {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(runs)
	}
}

// auditUploadSink is an ingest endpoint that keeps the uploaded records.
func auditUploadSink(t *testing.T) (*uploader, func() []plugins.DeviceRecord) {
	t.Helper()
	var mu sync.Mutex
	var got []plugins.DeviceRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/monitoring/metrics") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var batch plugins.DeviceRecordBatch
		if err = json.NewDecoder(gz).Decode(&batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, batch.Records...)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	up, err := newUploader(context.Background(), testConfig(srv.URL, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	return up, func() []plugins.DeviceRecord {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// auditLoopback binds port on a random 127.x.y.z address.
func auditLoopback(t *testing.T, port int) (net.Listener, string) {
	t.Helper()
	var lastErr error
	for range 20 {
		ip := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(250), rand.IntN(250), 1+rand.IntN(250))
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
		if err == nil {
			return ln, ip
		}
		lastErr = err
	}
	t.Fatalf("cannot bind a loopback address on port %d: %v", port, lastErr)
	return nil, ""
}

// TestAuditCredentialKindRouting: every vault credential kind reaches the
// plugin of its protocol, and SSH secrets reach no other protocol (COL-02,
// DIS-04).
func TestAuditCredentialKindRouting(t *testing.T) {
	names := []string{"ssh", "wmi", "nas", "redfish", "ipmi", "snmp"}
	spies := map[string]*auditSpy{}
	var plugs []plugins.Plugin
	for _, n := range names {
		spies[n] = &auditSpy{name: n}
		plugs = append(plugs, spies[n])
	}
	auditUsePlugins(t, plugs...)
	auditInstallBundle(
		bundleCredential{ID: "cred-wmi", Kind: "wmi", Secret: map[string]any{"username": "CORP\\win-audit", "password": "wmi-secret"}},
		bundleCredential{ID: "cred-ssh", Kind: "ssh_password", Secret: map[string]any{"username": "root", "password": "ssh-secret"}},
		bundleCredential{ID: "cred-bmc", Kind: "redfish", Secret: map[string]any{"username": "bmc", "password": "redfish-secret"}},
		bundleCredential{ID: "cred-ipmi", Kind: "ipmi", Secret: map[string]any{"username": "ipmi", "password": "ipmi-secret"}},
		bundleCredential{ID: "cred-snmp", Kind: "snmp_v2c", Secret: map[string]any{"community": "snmp-secret"}},
	)
	const target = "192.0.2.10"
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000a1", Kind: "host", Target: target, Schedule: "24h",
		CredentialIDs: []string{"cred-wmi", "cred-ssh", "cred-bmc", "cred-ipmi", "cred-snmp"}, Plugins: names}}, nil)
	runDueDiscovery(context.Background(), &collectorConfig{}, nil, newScheduler(), &hostCache{}, time.Now())

	want := map[string]string{"ssh": "ssh-secret", "wmi": "wmi-secret", "redfish": "redfish-secret", "ipmi": "ipmi-secret"}
	for _, n := range names {
		seen := spies[n].seen(target)
		if len(seen) == 0 {
			t.Errorf("%s: plugin was not run for %s", n, target)
			continue
		}
		creds := seen[0]
		if w, ok := want[n]; ok && creds["password"] != w {
			t.Errorf("%s plugin received password %q, want the %s credential's %q", n, creds["password"], n, w)
		}
		if n == "snmp" && creds["community"] != "snmp-secret" {
			t.Errorf("snmp plugin received community %q", creds["community"])
		}
		if n != "ssh" {
			for k, v := range creds {
				if v == "ssh-secret" || (k == "username" && v == "root") {
					t.Errorf("COL-02 violated: the SSH credential reached the %s plugin (%s=%q)", n, k, v)
				}
			}
		}
	}
}

// TestAuditPerScopeCredentials: each scope's targets are probed only with
// that scope's credentials (DIS-09, COL-02).
func TestAuditPerScopeCredentials(t *testing.T) {
	spy := &auditSpy{name: "snmp"}
	auditUsePlugins(t, spy)
	auditInstallBundle(
		bundleCredential{ID: "cred-client-1", Kind: "snmp_v2c", Secret: map[string]any{"community": "c1-secret"}},
		bundleCredential{ID: "cred-client-2", Kind: "snmp_v2c", Secret: map[string]any{"community": "c2-secret"}},
	)
	collectorScopes.set([]collectorScope{
		{ID: "6f0b6f0e-0000-4000-8000-0000000000a2", Kind: "host", Target: "192.0.2.1", ClientID: "client-1", Schedule: "24h", CredentialIDs: []string{"cred-client-1"}, Plugins: []string{"snmp"}},
		{ID: "6f0b6f0e-0000-4000-8000-0000000000a3", Kind: "host", Target: "198.51.100.1", ClientID: "client-2", Schedule: "24h", CredentialIDs: []string{"cred-client-2"}, Plugins: []string{"snmp"}},
	}, nil)
	runDueDiscovery(context.Background(), &collectorConfig{}, nil, newScheduler(), &hostCache{}, time.Now())

	for target, want := range map[string]string{"192.0.2.1": "c1-secret", "198.51.100.1": "c2-secret"} {
		seen := spy.seen(target)
		if len(seen) == 0 {
			t.Errorf("%s was not probed", target)
			continue
		}
		for _, c := range seen {
			if c["community"] != want {
				t.Errorf("target %s was probed with community %q, want its scope's %q (other client's secret disclosed)", target, c["community"], want)
			}
		}
	}
}

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

// TestAuditWMIAuthFailureReachesScopeRun: a WinRM endpoint that refuses the
// credentials shows up as an error of the scope run with protocol and
// target (DIS-08), not as a clean run.
func TestAuditWMIAuthFailureReachesScopeRun(t *testing.T) {
	runs := auditCaptureScopeRuns(t)
	up, _ := auditUploadSink(t)
	ln, ip := auditLoopback(t, 5985)
	srv := &http.Server{ReadHeaderTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", "Negotiate")
		w.WriteHeader(http.StatusUnauthorized)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	auditUsePlugins(t) // reset state; the shipped wmi plugin stays registered
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000a9", Kind: "host", Target: ip, Schedule: "24h", Plugins: []string{"wmi"}}}, nil)
	// Credentials in both namespaces so the alias defect cannot mask the result.
	cfg := collectorConfig{Credentials: map[string]string{"wmi.username": "u", "wmi.password": "wrong", "ssh.username": "u", "ssh.password": "wrong"}}
	runDueDiscovery(context.Background(), &cfg, up, newScheduler(), &hostCache{}, time.Now())

	all := runs()
	if len(all) != 1 {
		t.Fatalf("scope run reports = %d, want 1", len(all))
	}
	for _, e := range all[0].Result.Errors {
		if e.Protocol == "wmi" && e.Target == ip {
			return
		}
	}
	t.Errorf("WinRM answered 401 for every request, but the scope run reported records=%d error_count=%d errors=%+v (want an error with protocol wmi and target %s)",
		all[0].Result.Records, all[0].Result.ErrorCount, all[0].Result.Errors, ip)
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
