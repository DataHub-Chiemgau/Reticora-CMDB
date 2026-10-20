// Per-protocol credential selection of the collector plugins (WP-048,
// COL-02). TestAuditT2CredentialsArePerProtocol (F-010, the cause of F-028)
// moved here from t2_credentials_auditdefect_test.go (WP-378): the aliases
// wmi->ssh and nas->ssh are gone, so the SSH account no longer reaches
// WinRM or NAS targets and the stored WMI credential is used; NAS has its
// own kind and namespace nas (WP-379, E-68).
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
		"nas.username": "dsm-admin", "nas.password": "nas-pw",
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
		// NAS has its own kind nas (WP-379, E-68).
		{"nas", map[string]string{"username": "dsm-admin", "password": "nas-pw"}},
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

// TestOctetText: the text of an SNMP OctetString (SNMP and power plugin)
// without NUL padding; a binary value stays empty.
func TestOctetText(t *testing.T) {
	for in, want := range map[string]string{
		"core-sw-1":           "core-sw-1",
		"FOC1234X0YZ\x00\x00": "FOC1234X0YZ", // NUL padding
		" DC1 R1 \x00 ":       "DC1 R1",
		"Cisco IOS Software\r\nTechnical Support": "Cisco IOS Software\r\nTechnical Support",
		"Büro 2":           "Büro 2",
		"\x00\x11\x00\x22": "", // binary
		"":                 "",
	} {
		if got := OctetText([]byte(in)); got != want {
			t.Errorf("OctetText(%q) = %q, want %q", in, got, want)
		}
	}
}
