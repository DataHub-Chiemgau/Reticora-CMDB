//go:build auditdefect

// Defect test found by WP-539 (t2-redfish-tls-evidence, COL-02, DIS-08;
// F-368): the run of a Redfish target whose certificate is not trusted
// reports nothing. The Redfish probe (plugins/redfish/redfish.go, Probe)
// drops the handshake error, so the pass has no hit, no record and no run
// error; the scope run reports zero errors and the operator never learns
// that the BMC was refused. The probe part of
// TestCollectRejectsUntrustedCertificateByDefault
// (plugins/redfish/redfish_test.go) encodes the silent probe (no result,
// error ignored) and is rewritten with the fix.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run '^TestAuditRedfishUntrustedCertificateReported$' ./collectorcmd/

package collectorcmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/redfish"
)

// TestAuditRedfishUntrustedCertificateReported: a pass against a Redfish
// target without a trusting TLS scope (none, or a wrong pin) reports a run
// error of protocol redfish for the target that names the certificate error
// (tls_untrusted), while still sending nothing to the target.
func TestAuditRedfishUntrustedCertificateReported(t *testing.T) {
	bmc := startRedfishEmulator(t)
	scope := collectorScope{ID: "6f0b6f0e-0000-4000-8000-000000000539", Kind: "host", Target: "127.0.0.1", Schedule: "24h",
		CredentialIDs: []string{"cred-ssh", "cred-redfish"}, Plugins: []string{"redfish"}}
	for name, tlsScopes := range map[string]string{
		"no TLS scope": "",
		"wrong pin":    "127.0.0.1/32=pin:" + spkiPin(t, filepath.Join(redfishTLSDir, "other.pem")),
	} {
		t.Run(name, func(t *testing.T) {
			bmc.reset()
			t.Setenv("RETICORA_REDFISH_TLS_SCOPES", tlsScopes)
			auditUsePlugins(t, redfish.New(redfish.WithTLSScopes(tlsScopesFromEnv("RETICORA_REDFISH_TLS_SCOPES")...), redfish.WithPort(bmc.port)))
			installRedfishTLSBundle(t)
			collectorScopes.set([]collectorScope{scope}, nil)
			passes := discoveryPasses(&collectorConfig{})
			_, errs := runPass(context.Background(), &collectorConfig{}, nil, &passes[0], []string{"redfish"}, &hostCache{})
			if n := len(bmc.received()); n != 0 {
				t.Fatalf("%d requests reached the untrusted target", n)
			}
			for _, e := range errs {
				if e.Protocol == "redfish" && e.Target == "127.0.0.1" &&
					(strings.Contains(e.Message, "certificate") || strings.Contains(e.Message, "tls_untrusted")) {
					return
				}
			}
			t.Errorf("run errors = %+v, want a redfish error for 127.0.0.1 that names the certificate error (DIS-08)", errs)
		})
	}
}
