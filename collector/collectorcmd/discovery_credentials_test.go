// Credential selection of the collector's discovery passes (WP-378; COL-02,
// DIS-04, DIS-09): a pass uses only the credentials of its scope, a plugin
// only the namespace of its protocol; the locally configured credentials
// apply only to the configured subnets (fallback pass, metric polling,
// traps). TestAuditCredentialKindRouting (F-028) and
// TestAuditPerScopeCredentials (F-013) moved here from
// discovery_auditdefect_test.go.
package collectorcmd

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
)

// TestAuditCredentialKindRouting: every vault credential kind reaches the
// plugin of its protocol, SSH secrets reach no other protocol, and NAS gets
// none until it has a kind of its own (COL-02, DIS-04).
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
	// NAS has no credential kind of its own until WP-379 (E-68): fail closed.
	for _, creds := range spies["nas"].seen(target) {
		if len(creds) != 0 {
			t.Errorf("nas plugin received credentials %v, want none", creds)
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

// answerAll is a probe that finds every target.
func answerAll(targets []string) []plugins.Result {
	out := make([]plugins.Result, 0, len(targets))
	for _, tg := range targets {
		out = append(out, plugins.Result{IP: tg, Name: tg})
	}
	return out
}

// auditPollSpy is an SNMP plugin spy that also records the metric polls.
type auditPollSpy struct {
	*auditSpy
	// onPoll runs after each poll (e.g. to stop the metric loop).
	onPoll func()

	pollMu sync.Mutex
	polls  map[string][]map[string]string // target -> credentials of each poll
}

func (s *auditPollSpy) Poll(_ context.Context, target string, creds map[string]string, _ []snmp.PollMetric) ([]plugins.MetricSample, error) {
	s.pollMu.Lock()
	if s.polls == nil {
		s.polls = map[string][]map[string]string{}
	}
	s.polls[target] = append(s.polls[target], maps.Clone(creds))
	s.pollMu.Unlock()
	if s.onPoll != nil {
		s.onPoll()
	}
	return []plugins.MetricSample{{Name: "ups_battery_charge_percent", Value: 100, Labels: map[string]string{"target": target}}}, nil
}

func (s *auditPollSpy) polled(target string) []map[string]string {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	return s.polls[target]
}

// TestScopePassUsesOnlyItsCredentialList: a scope's pass resolves exactly
// the IDs of its credential_ids (the effective list the server sends, E-67)
// and never falls back to other bundle credentials or to the locally
// configured ones; two credentials of one namespace are not mixed. Without
// server scopes the configured subnets use only the local credentials.
func TestScopePassUsesOnlyItsCredentialList(t *testing.T) {
	snmpSpy, sshSpy := &auditSpy{name: "snmp"}, &auditSpy{name: "ssh"}
	auditUsePlugins(t, snmpSpy, sshSpy)
	auditInstallBundle(
		bundleCredential{ID: "cred-v2c", Kind: "snmp_v2c", Secret: map[string]any{"community": "v2c-secret"}},
		bundleCredential{ID: "cred-v3", Kind: "snmp_v3", Secret: map[string]any{"username": "v3-user", "auth_password": "v3-auth"}},
		bundleCredential{ID: "cred-ssh", Kind: "ssh_password", Secret: map[string]any{"username": "root", "password": "ssh-secret"}},
	)
	local := map[string]string{"snmp.community": "local-community", "ssh.username": "local", "ssh.password": "local-ssh"}
	both := []string{"snmp", "ssh"}
	collectorScopes.set([]collectorScope{
		{ID: "6f0b6f0e-0000-4000-8000-0000000000b1", Kind: "host", Target: "192.0.2.31", Schedule: "24h", CredentialIDs: []string{}, Plugins: both},
		{ID: "6f0b6f0e-0000-4000-8000-0000000000b2", Kind: "host", Target: "192.0.2.32", Schedule: "24h", CredentialIDs: []string{"cred-unknown"}, Plugins: both},
		{ID: "6f0b6f0e-0000-4000-8000-0000000000b3", Kind: "host", Target: "192.0.2.33", Schedule: "24h", CredentialIDs: []string{"cred-v3", "cred-v2c"}, Plugins: both},
	}, nil)
	runDueDiscovery(context.Background(), &collectorConfig{Credentials: local}, nil, newScheduler(), &hostCache{}, time.Now())

	collectorScopes = &scopeCache{} // not enrolled: no scope list from the server
	fallback := collectorConfig{ScanSubnets: []string{"192.0.2.40/32"}, Protocols: both, Credentials: local}
	runDueDiscovery(context.Background(), &fallback, nil, newScheduler(), &hostCache{}, time.Now())

	none := map[string]string{}
	cases := []struct {
		name, target string
		snmp, ssh    map[string]string
	}{
		{"scope with an empty credential list", "192.0.2.31", none, none},
		{"scope with an ID missing from the bundle", "192.0.2.32", none, none},
		{"scope with an SNMPv3 and an SNMPv2c credential", "192.0.2.33", map[string]string{"username": "v3-user", "auth_password": "v3-auth"}, none},
		{"fallback pass without server scopes", "192.0.2.40", map[string]string{"community": "local-community"}, map[string]string{"username": "local", "password": "local-ssh"}},
	}
	for _, tc := range cases {
		for _, c := range []struct {
			spy  *auditSpy
			want map[string]string
		}{{snmpSpy, tc.snmp}, {sshSpy, tc.ssh}} {
			seen := c.spy.seen(tc.target)
			if len(seen) == 0 {
				t.Errorf("%s: %s plugin was not run for %s", tc.name, c.spy.name, tc.target)
			}
			for _, got := range seen {
				if !maps.Equal(got, c.want) {
					t.Errorf("%s: %s plugin received %v, want %v", tc.name, c.spy.name, got, c.want)
				}
			}
		}
	}
}

// TestPowerValuesUseTheCommunityOfTheirScope: the UPS readings of a power
// device are polled with the SNMP credentials of its scope's pass; a power
// target in the scope of client 2 is never polled with the community of
// client 1.
func TestPowerValuesUseTheCommunityOfTheirScope(t *testing.T) {
	power := &auditSpy{name: "power", alive: answerAll}
	poller := &auditPollSpy{auditSpy: &auditSpy{name: "snmp"}}
	auditUsePlugins(t, power, poller)
	auditInstallBundle(
		bundleCredential{ID: "cred-client-1", Kind: "snmp_v2c", Secret: map[string]any{"community": "c1-community"}},
		bundleCredential{ID: "cred-client-2", Kind: "snmp_v2c", Secret: map[string]any{"community": "c2-community"}},
	)
	collectorScopes.set([]collectorScope{
		{ID: "6f0b6f0e-0000-4000-8000-0000000000c1", Kind: "host", Target: "192.0.2.71", ClientID: "6f0b6f0e-0000-4000-8000-0000000001c1", Schedule: "24h",
			CredentialIDs: []string{"cred-client-1"}, Plugins: []string{"power"}},
		{ID: "6f0b6f0e-0000-4000-8000-0000000000c2", Kind: "host", Target: "198.51.100.71", ClientID: "6f0b6f0e-0000-4000-8000-0000000001c2", Schedule: "24h",
			CredentialIDs: []string{"cred-client-2"}, Plugins: []string{"power"}},
	}, nil)
	up, uploaded := auditUploadSink(t)
	runDueDiscovery(context.Background(), &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now())

	for target, want := range map[string]string{"192.0.2.71": "c1-community", "198.51.100.71": "c2-community"} {
		polls := poller.polled(target)
		if len(polls) == 0 {
			t.Errorf("the UPS values of %s were not polled", target)
		}
		for _, c := range polls {
			if !maps.Equal(c, map[string]string{"community": want}) {
				t.Errorf("the UPS values of %s were polled with %v, want only its scope's community %q", target, c, want)
			}
		}
		for _, c := range power.seen(target) {
			if !maps.Equal(c, map[string]string{"community": want}) {
				t.Errorf("the power plugin probed %s with %v, want only its scope's community %q", target, c, want)
			}
		}
	}
	if len(uploaded()) != 2 {
		t.Errorf("uploaded records = %d, want one per power device", len(uploaded()))
	}
}

// TestMetricPollerUsesOnlyLocalCredentials: the metric poller polls the
// configured subnets with the locally configured community and never gets
// a credential of a server scope (here an SNMPv3 account of client 1).
func TestMetricPollerUsesOnlyLocalCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	poller := &auditPollSpy{auditSpy: &auditSpy{name: "snmp"}, onPoll: cancel}
	auditUsePlugins(t, poller)
	auditInstallBundle(bundleCredential{ID: "cred-client-1", Kind: "snmp_v3", Secret: map[string]any{"username": "c1-v3-user", "auth_password": "c1-v3-auth"}})
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000c3", Kind: "cidr", Target: "198.51.100.0/24", ClientID: "client-1",
		Schedule: "24h", CredentialIDs: []string{"cred-client-1"}, Plugins: []string{"snmp"}}}, nil)
	up, _ := auditUploadSink(t)
	cfg := collectorConfig{ScanSubnets: []string{"192.0.2.50/32"}, MetricsInterval: time.Hour,
		PollMetrics: []snmp.PollMetric{{Name: "if_in_octets", OID: "1.3.6.1.2.1.2.2.1.10.1"}},
		Credentials: map[string]string{"snmp.community": "local-community", "ssh.password": "local-ssh"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMetricsLoop(ctx, cfg, up)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the metric loop did not poll 192.0.2.50")
	}

	polls := poller.polled("192.0.2.50")
	if len(polls) != 1 {
		t.Fatalf("polls of 192.0.2.50 = %v, want 1", polls)
	}
	if !maps.Equal(polls[0], map[string]string{"community": "local-community"}) {
		t.Errorf("the metric poller polled 192.0.2.50 with %v, want only the local community", polls[0])
	}
}

// TestTrapReceiverUsesOnlyTheLocalCommunity: the trap receiver checks the
// locally configured community, never one of a server scope's credentials.
func TestTrapReceiverUsesOnlyTheLocalCommunity(t *testing.T) {
	auditUsePlugins(t)
	auditInstallBundle(bundleCredential{ID: "cred-client-1", Kind: "snmp_v2c", Secret: map[string]any{"community": "c1-community"}})
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000c4", Kind: "cidr", Target: "198.51.100.0/24", ClientID: "client-1",
		Schedule: "24h", CredentialIDs: []string{"cred-client-1"}, Plugins: []string{"snmp"}}}, nil)
	if got := newTrapReceiver(collectorConfig{Credentials: map[string]string{"snmp.community": "local-community"}}, nil).Community; got != "local-community" {
		t.Errorf("trap community = %q, want the local one", got)
	}
	if got := newTrapReceiver(collectorConfig{}, nil).Community; got != "" {
		t.Errorf("trap community without a local one = %q, want none (not a scope's credential)", got)
	}
}

// credentialKinds is testdata/credential-kinds.json, the contract between
// the credential kinds of the backend (backend/internal/credential checks
// it against validKinds) and the plugins of the collector.
type credentialKinds struct {
	Kinds []struct {
		Kind      string   `json:"kind"`
		Namespace string   `json:"namespace"`
		Plugins   []string `json:"plugins"`
	} `json:"kinds"`
}

// TestCredentialKindsContract covers WP-378 (COL-02, F-028): every backend
// credential kind of the contract is listed once and maps to exactly its
// namespace; a credential of the kind reaches exactly the listed plugins;
// the collector maps no kind the contract does not list.
func TestCredentialKindsContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/credential-kinds.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract credentialKinds
	if err = json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.Kinds) == 0 {
		t.Fatal("the contract lists no credential kinds")
	}
	t.Cleanup(func() { collectorBundle.set(nil) })
	listed := map[string]bool{}
	for _, k := range contract.Kinds {
		if listed[k.Kind] {
			t.Errorf("kind %q is listed more than once", k.Kind)
		}
		listed[k.Kind] = true
		if ns, ok := credentialNamespace[k.Kind]; !ok || ns == "" || ns != k.Namespace {
			t.Errorf("kind %q maps to namespace %q (known %v), contract says %q", k.Kind, ns, ok, k.Namespace)
		}
		collectorBundle.set(&bundleContent{Credentials: []bundleCredential{{ID: "cred", Kind: k.Kind, Secret: map[string]any{"probe": "secret-of-" + k.Kind}}}})
		resolved := collectorBundle.resolve([]string{"cred"})
		var reached []string
		for name := range pluginRegistry {
			if plugins.CredentialsFor(name, resolved)["probe"] != "" {
				reached = append(reached, name)
			}
		}
		slices.Sort(reached)
		if want := slices.Sorted(slices.Values(k.Plugins)); !slices.Equal(reached, want) {
			t.Errorf("a credential of kind %q reaches the plugins %v, contract says %v", k.Kind, reached, want)
		}
	}
	for kind := range credentialNamespace {
		if !listed[kind] {
			t.Errorf("the collector maps kind %q, which the contract does not list", kind)
		}
	}
}
