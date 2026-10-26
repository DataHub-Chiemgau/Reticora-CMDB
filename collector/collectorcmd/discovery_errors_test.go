// Error causes in the collector's scope runs (WP-415; DIS-08, F-029): a
// collection failure reaches the scope run with the cause the plugin knows
// (auth_failed, unreachable), the protocol and the target; the server keeps
// that cause in last_result. TestAuditWMIAuthFailureReachesScopeRun moved
// here from discovery_auditdefect_test.go; TestSNMPv3AuthFailureReachesScopeRun
// is the SNMPv3 counterpart (WP-492, F-105).
//
// The scope run reports travel over NATS like in production: TEST_NATS_URL,
// or a nats-server from TEST_NATS_SERVER_BIN.
package collectorcmd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/wmi"
)

// wmiInstances is what the WinRM fake of the control delivers per class
// (empty: no instance).
var wmiInstances = map[string]string{
	"Win32_OperatingSystem":             `<p:Caption>Microsoft Windows Server 2022 Standard</p:Caption><p:Version>10.0.20348</p:Version><p:BuildNumber>20348</p:BuildNumber>`,
	"Win32_ComputerSystem":              `<p:Name>AUDIT-WIN-01</p:Name><p:DNSHostName>audit-win-01</p:DNSHostName><p:Domain>corp.example</p:Domain><p:Manufacturer>Dell Inc.</p:Manufacturer><p:Model>PowerEdge R650</p:Model>`,
	"Win32_BIOS":                        `<p:SerialNumber>SER-AUDIT-WMI-1</p:SerialNumber><p:SMBIOSBIOSVersion>1.9.2</p:SMBIOSBIOSVersion>`,
	"Win32_ComputerSystemProduct":       `<p:UUID>4C4C4544-0042-3510-8051-B4C04F4A3132</p:UUID>`,
	"Win32_NetworkAdapterConfiguration": "",
}

// winRMPull answers like a WinRM that ignores OptimizeEnumeration (DSP0226
// allows it): Enumerate returns only the enumeration context of the class,
// Pull with that context its instance and EndOfSequence; another context is
// refused with a SOAP fault.
func winRMPull(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := string(body)
	w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
	const env = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:n="http://schemas.xmlsoap.org/ws/2004/09/enumeration" ` +
		`xmlns:p="http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/%[1]s"><s:Body>%[2]s</s:Body></s:Envelope>`
	class := ""
	for c := range wmiInstances {
		if strings.Contains(req, "/root/cimv2/"+c+"<") {
			class = c
		}
	}
	if !strings.Contains(req, "/enumeration/Pull<") {
		_, _ = fmt.Fprintf(w, env, class, `<n:EnumerateResponse><n:EnumerationContext>uuid:ctx-`+class+`</n:EnumerationContext></n:EnumerateResponse>`)
		return
	}
	if class == "" || !strings.Contains(req, ">uuid:ctx-"+class+"<") {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, env, class, `<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>n:InvalidEnumerationContext</s:Value></s:Subcode></s:Code>`+
			`<s:Reason><s:Text xml:lang="en-US">The enumeration context supplied in the message is not valid.</s:Text></s:Reason></s:Fault>`)
		return
	}
	items := ""
	if props := wmiInstances[class]; props != "" {
		items = `<p:` + class + `>` + props + `</p:` + class + `>`
	}
	_, _ = fmt.Fprintf(w, env, class, `<n:PullResponse><n:Items>`+items+`</n:Items><n:EndOfSequence/></n:PullResponse>`)
}

// TestAuditWMIAuthFailureReachesScopeRun: a WinRM endpoint that refuses the
// credentials on every request shows up as exactly one error of the scope
// run with cause auth_failed, protocol wmi and the target (DIS-08), not as
// a clean run; a connection that breaks off as unreachable. The probe hit
// is still reported as the record. Control: a DSP0226-conformant WinRM
// (Enumerate, Pull) gives a clean run whose record carries serial number,
// model and OS.
func TestAuditWMIAuthFailureReachesScopeRun(t *testing.T) {
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", "Negotiate")
			w.WriteHeader(code)
		}
	}
	cases := []struct {
		name    string
		handler http.HandlerFunc
		// cause is the cause of the one error; empty for the control.
		cause string
	}{
		{"WinRM answers 401 to every request", status(http.StatusUnauthorized), "auth_failed"},
		{"WinRM answers 403 to every request", status(http.StatusForbidden), "auth_failed"},
		{"WinRM drops every connection", func(w http.ResponseWriter, _ *http.Request) {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
		}, "unreachable"},
		{"control: WinRM answers DSP0226-conformant", winRMPull, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runs := auditCaptureScopeRuns(t)
			up, uploaded := auditUploadSink(t)
			// WinRM over HTTPS on 5986 with a self-signed certificate that
			// the WMI TLS scope of the target pins (RETICORA_WMI_TLS_SCOPES).
			ln, ip := auditLoopback(t, 5986)
			srv := httptest.NewUnstartedServer(tc.handler)
			_ = srv.Listener.Close()
			srv.Listener = ln
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.StartTLS()
			t.Cleanup(srv.Close)
			_, network, err := net.ParseCIDR(ip + "/32")
			if err != nil {
				t.Fatal(err)
			}
			pin := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
			auditUsePlugins(t, wmi.New(wmi.WithTLSScopes(plugins.TLSScope{Network: network, PinsSHA256: [][]byte{pin[:]}})))
			// The scope's WMI credential comes from the bundle (WP-378: local
			// credentials do not apply to server scopes).
			auditInstallBundle(bundleCredential{ID: "cred-wmi", Kind: "wmi", Secret: map[string]any{"username": "u", "password": "wrong"}})
			collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000a9", Kind: "host", Target: ip, Schedule: "24h",
				CredentialIDs: []string{"cred-wmi"}, Plugins: []string{"wmi"}}}, nil)
			runDueDiscovery(context.Background(), &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now())

			all := runs()
			if len(all) != 1 {
				t.Fatalf("scope run reports = %d, want 1", len(all))
			}
			res := all[0].Result
			if tc.cause == "" {
				if res.Records != 1 || res.ErrorCount != 0 || len(res.Errors) != 0 {
					t.Errorf("control: scope run records=%d error_count=%d errors=%+v, want one record and no error", res.Records, res.ErrorCount, res.Errors)
				}
				recs := uploaded()
				if len(recs) != 1 || recs[0].TargetIP != ip || recs[0].Identity.SerialNumber != "SER-AUDIT-WMI-1" ||
					recs[0].Classification.Model != "PowerEdge R650" || recs[0].System.OSName != "Microsoft Windows Server 2022 Standard" {
					t.Errorf("control: uploaded %+v, want the WMI record of %s with serial number, model and OS", recs, ip)
				}
				return
			}
			if res.ErrorCount != 1 || len(res.Errors) != 1 {
				t.Fatalf("WinRM %s, but the scope run reported records=%d error_count=%d errors=%+v (want one error with cause %s, protocol wmi and target %s)",
					tc.name, res.Records, res.ErrorCount, res.Errors, tc.cause, ip)
			}
			if e := res.Errors[0]; e.Cause != tc.cause || e.Protocol != "wmi" || e.Target != ip || e.Message == "" {
				t.Errorf("scope run error = %+v, want cause %s, protocol wmi, target %s and a message", e, tc.cause, ip)
			}
			// The reachable device is still reported from its probe.
			if res.Records != 1 {
				t.Errorf("scope run records = %d, want the probe hit", res.Records)
			}
		})
	}
}

// startV3RejectingAgent is an SNMPv3 agent that refuses every credential as
// RFC 3414 3.2 prescribes: it answers the engine discovery with
// usmStatsUnknownEngineIDs and every authenticated request with the
// unauthenticated report usmStatsWrongDigests. It returns its port and the
// number of wrong-digest reports sent.
func startV3RejectingAgent(t *testing.T) (int, *atomic.Int32) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	const engineID = "\x80\x00\x1f\x88\x80reticora-reject"
	reports := &atomic.Int32{}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, peer, rerr := conn.ReadFromUDP(buf)
			if rerr != nil {
				return
			}
			// Only the header is needed; the encrypted PDU of an
			// authenticated request does not decode without the keys.
			decoder := &gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.NoAuthNoPriv,
				SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: "agent"}, Logger: gosnmp.NewLogger(nil)}
			req, _ := decoder.SnmpDecodePacket(slices.Clone(buf[:n]))
			if req == nil || req.MsgID == 0 {
				continue
			}
			counter := ".1.3.6.1.6.3.15.1.1.4.0" // usmStatsUnknownEngineIDs
			user := ""
			if req.MsgFlags&gosnmp.AuthNoPriv != 0 {
				counter = ".1.3.6.1.6.3.15.1.1.5.0" // usmStatsWrongDigests
				if usm, ok := req.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
					user = usm.UserName
				}
				reports.Add(1)
			}
			out, merr := (&gosnmp.SnmpPacket{
				Version: gosnmp.Version3, MsgFlags: gosnmp.NoAuthNoPriv, SecurityModel: gosnmp.UserSecurityModel,
				SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: user, AuthoritativeEngineID: engineID,
					AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 1000},
				MsgID: req.MsgID, RequestID: req.RequestID, MsgMaxSize: 65507, PDUType: gosnmp.Report, ContextEngineID: engineID,
				Variables: []gosnmp.SnmpPDU{{Name: counter, Type: gosnmp.Counter32, Value: uint32(1)}},
			}).MarshalMsg()
			if merr == nil {
				_, _ = conn.WriteToUDP(out, peer)
			}
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port, reports
}

// TestSNMPv3AuthFailureReachesScopeRun (WP-492; DIS-08, F-105): an SNMPv3
// agent that refuses the scope's credentials with usmStatsWrongDigests shows
// up as exactly one error of the scope run with cause auth_failed, protocol
// snmp and the target, not as a clean run without the device; the answering
// agent is still reported from its probe.
func TestSNMPv3AuthFailureReachesScopeRun(t *testing.T) {
	runs := auditCaptureScopeRuns(t)
	up, _ := auditUploadSink(t)
	port, reports := startV3RejectingAgent(t)
	const ip = "127.0.0.1"
	auditUsePlugins(t, &snmp.Plugin{Timeout: time.Second, Port: port, Concurrency: 4})
	auditInstallBundle(bundleCredential{ID: "cred-v3", Kind: "snmp_v3", Secret: map[string]any{"username": "reticora",
		"auth_protocol": "SHA256", "auth_password": "wrong-auth-pw", "priv_protocol": "AES", "priv_password": "wrong-priv-pw"}})
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000b3", Kind: "host", Target: ip, Schedule: "24h",
		CredentialIDs: []string{"cred-v3"}, Plugins: []string{"snmp"}}}, nil)
	runDueDiscovery(context.Background(), &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now())

	all := runs()
	if len(all) != 1 {
		t.Fatalf("scope run reports = %d, want 1", len(all))
	}
	res := all[0].Result
	if reports.Load() == 0 {
		t.Fatal("control: the agent refused no request")
	}
	if res.ErrorCount != 1 || len(res.Errors) != 1 {
		t.Fatalf("the SNMPv3 agent refused the credentials with %d usmStatsWrongDigests report(s), but the scope run reported "+
			"records=%d error_count=%d errors=%+v (want one error with cause auth_failed, protocol snmp and target %s)",
			reports.Load(), res.Records, res.ErrorCount, res.Errors, ip)
	}
	if e := res.Errors[0]; e.Cause != "auth_failed" || e.Protocol != "snmp" || e.Target != ip || !strings.Contains(e.Message, "usmStatsWrongDigests") {
		t.Errorf("scope run error = %+v, want cause auth_failed, protocol snmp, target %s and the report in the message", e, ip)
	}
	if res.Records != 1 {
		t.Errorf("scope run records = %d, want the probe hit", res.Records)
	}
}
