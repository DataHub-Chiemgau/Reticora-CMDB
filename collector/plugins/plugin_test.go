// Per-protocol credential selection of the collector plugins (WP-048,
// COL-02). TestAuditT2CredentialsArePerProtocol (F-010, the cause of F-028)
// moved here from t2_credentials_auditdefect_test.go (WP-378): the aliases
// wmi->ssh and nas->ssh are gone, so the SSH account no longer reaches
// WinRM or NAS targets and the stored WMI credential is used.
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
		// NAS has no credential kind until WP-379 (E-68): fail closed.
		{"nas", map[string]string{}},
		{"sweep", map[string]string{}},
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
	// the SSH account as a fallback; the same for NAS.
	sshOnly := map[string]string{"ssh.username": "root", "ssh.password": "ssh-pw"}
	for _, protocol := range []string{"wmi", "nas"} {
		if got := CredentialsFor(protocol, sshOnly); len(got) != 0 {
			t.Errorf("CredentialsFor(%q) without %s.* entries = %v, want an empty map", protocol, protocol, got)
		}
	}

	// power -> snmp is the only shared namespace.
	if want := map[string]string{"power": "snmp"}; !maps.Equal(credentialAliases, want) {
		t.Errorf("credentialAliases = %v, want %v", credentialAliases, want)
	}
}
