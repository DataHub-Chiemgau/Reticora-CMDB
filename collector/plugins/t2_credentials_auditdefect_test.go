//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the
// per-protocol credential selection of the collector.
//
//   - WP-048 (COL-02), finding P1 "WMI plugin receives the SSH account (alias
//     wmi->ssh) and sends it as HTTP Basic over plaintext; WMI credentials are
//     ignored". Gap (critical) "Per-protocol isolation for all plugins incl.
//     WMI/NAS/IPMI". Acceptance criterion of WP-048: "Credentials werden je
//     Protokoll ausgewaehlt". The server bundle namespaces a credential of
//     kind wmi as "wmi.*" (collectorcmd/bundle.go credentialNamespace), but
//     plugins.CredentialsFor maps wmi to the ssh namespace (plugin.go
//     credentialAliases), so the SSH account reaches every WinRM target and
//     the stored WMI credential is never used. The cleartext transport itself
//     is covered by group T4 (collector/plugins/wmi/wmi_auditdefect_test.go).
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./plugins/
package plugins

import (
	"maps"
	"testing"
)

// TestAuditT2CredentialsArePerProtocol: a protocol that has its own
// credential kind gets exactly the entries of its own namespace; the SSH
// account never reaches WMI, IPMI or Redfish targets. power -> snmp is the
// documented sharing (power polling is SNMP).
func TestAuditT2CredentialsArePerProtocol(t *testing.T) {
	all := map[string]string{
		"ssh.username": "root", "ssh.password": "ssh-pw",
		"wmi.username": "win-svc", "wmi.password": "win-pw",
		"ipmi.username": "bmc", "ipmi.password": "ipmi-pw",
		"redfish.username": "rf", "redfish.password": "rf-pw",
		"snmp.community": "c0mmunity",
	}
	for _, c := range []struct {
		protocol string
		want     map[string]string
	}{
		{"wmi", map[string]string{"username": "win-svc", "password": "win-pw"}},
		{"ipmi", map[string]string{"username": "bmc", "password": "ipmi-pw"}},
		{"redfish", map[string]string{"username": "rf", "password": "rf-pw"}},
		{"ssh", map[string]string{"username": "root", "password": "ssh-pw"}},
		{"snmp", map[string]string{"community": "c0mmunity"}},
		{"power", map[string]string{"community": "c0mmunity"}},
	} {
		got := CredentialsFor(c.protocol, all)
		if !maps.Equal(got, c.want) {
			t.Errorf("CredentialsFor(%q) = %v, want %v (COL-02: credentials per protocol)", c.protocol, got, c.want)
		}
		if c.protocol != "ssh" && (got["password"] == "ssh-pw" || got["username"] == "root") {
			t.Errorf("CredentialsFor(%q) hands out the SSH account", c.protocol)
		}
	}

	// Edge: no WMI credential configured -> no credentials at all, never
	// the SSH account as a fallback.
	sshOnly := map[string]string{"ssh.username": "root", "ssh.password": "ssh-pw"}
	if got := CredentialsFor("wmi", sshOnly); len(got) != 0 {
		t.Errorf("CredentialsFor(\"wmi\") without wmi.* entries = %v, want an empty map", got)
	}
}
