//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests (must fail
// until the defect is fixed).
//
// WP-152 (DIS-04), WP-155 (DIS-08).
//
// Finding encoded here:
//   - P1 "WMI Collect reports success with empty fields when WinRM refuses
//     auth or answers protocol-conformantly": wsmanEnumerate swallows
//     transport errors and HTTP status codes and never sends a WS-Man Pull,
//     so a 401, a refused connection or a DSP0226-conformant
//     EnumerationContext answer all yield a "successful" empty record
//     (DIS-04 fields, DIS-08 visible failures).
//
// TestAuditWMICredentialTransport (F-010) moved to wmi_test.go (WP-377); the
// helpers are in wmi_helpers_test.go. The fake WinRM endpoints speak HTTPS on
// 5986 (the only WinRM transport of the plugin) with a pinned self-signed
// certificate.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditWMIFailuresAreErrors' ./plugins/wmi/
package wmi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// wsmanPullFake is a protocol-faithful WinRM: Enumerate answers only an
// EnumerationContext (no OptimizeEnumeration), the instances come with Pull.
func wsmanPullFake(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := string(body)
	w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
	const env = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:n="http://schemas.xmlsoap.org/ws/2004/09/enumeration" xmlns:w="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd" xmlns:p="http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/%[1]s"><s:Body>%[2]s</s:Body></s:Envelope>`
	class := ""
	for _, c := range []string{"Win32_OperatingSystem", "Win32_ComputerSystemProduct", "Win32_ComputerSystem", "Win32_BIOS", "Win32_NetworkAdapterConfiguration"} {
		// Enumerate names the class in the ResourceURI, Pull in the context.
		if strings.Contains(req, "cimv2/"+c) || strings.Contains(req, "audit-"+c) {
			class = c
			break
		}
	}
	if !strings.Contains(req, "enumeration/Pull") {
		_, _ = fmt.Fprintf(w, env, class, `<n:EnumerateResponse><n:EnumerationContext>uuid:audit-`+class+`</n:EnumerationContext></n:EnumerateResponse>`)
		return
	}
	items := map[string]string{
		"Win32_OperatingSystem":       `<p:Caption>Microsoft Windows Server 2022 Standard</p:Caption><p:Version>10.0.20348</p:Version><p:BuildNumber>20348</p:BuildNumber>`,
		"Win32_ComputerSystem":        `<p:Name>AUDIT-WIN-01</p:Name><p:DNSHostName>audit-win-01</p:DNSHostName><p:Domain>corp.example</p:Domain><p:Manufacturer>Dell Inc.</p:Manufacturer><p:Model>PowerEdge R650</p:Model>`,
		"Win32_BIOS":                  `<p:SerialNumber>SER-AUDIT-WMI-1</p:SerialNumber><p:SMBIOSBIOSVersion>1.9.2</p:SMBIOSBIOSVersion>`,
		"Win32_ComputerSystemProduct": `<p:UUID>4C4C4544-0042-3510-8051-B4C04F4A3132</p:UUID>`,
	}[class]
	_, _ = fmt.Fprintf(w, env, class, `<n:PullResponse><n:Items><p:`+class+`>`+items+`</p:`+class+`></n:Items><n:EndOfSequence/></n:PullResponse>`)
}

// TestAuditWMIFailuresAreErrors encodes DIS-04/DIS-08: WMI either delivers
// the DeviceRecord fields or reports a failure; it never reports success
// with empty fields.
func TestAuditWMIFailuresAreErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc // nil: nothing listens (connection refused)
		// wantSerial is the serial the endpoint delivers; empty when it
		// delivers nothing, so only an error is acceptable.
		wantSerial string
	}{
		{name: "WinRM refuses the credentials (401 Negotiate)", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", "Negotiate")
			w.WriteHeader(http.StatusUnauthorized)
		}},
		{name: "WinRM answers DSP0226-conformant (EnumerationContext + Pull)", handler: wsmanPullFake, wantSerial: "SER-AUDIT-WMI-1"},
		{name: "WinRM port refuses connections", handler: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, ip := auditListen(t, winRMHTTPSPort)
			var opts []Option
			if tc.handler == nil {
				_ = ln.Close()
			} else {
				srv := serveWinRM(t, ln, tc.handler, true)
				opts = append(opts, WithTLSScopes(pinScope(t, ip, srv.Certificate())))
			}
			p := New(opts...)
			p.Timeout = 2 * time.Second
			p.client.Timeout = 2 * time.Second
			rec, err := p.Collect(context.Background(), ip, map[string]string{"username": "u", "password": "p"})
			if err != nil {
				return // the failure is reported (DIS-08)
			}
			serial := ""
			if rec != nil {
				serial = rec.Identity.SerialNumber
			}
			hostname := ""
			if rec != nil {
				hostname = rec.Identity.Hostname
			}
			switch {
			case tc.wantSerial == "":
				t.Errorf("Collect reported success without WMI data: err=<nil> serial=%q hostname=%q (want an error)", serial, hostname)
			case serial != tc.wantSerial:
				t.Errorf("Collect reported success without WMI data: err=<nil> serial=%q hostname=%q (want an error, or serial %q from the WinRM endpoint)",
					serial, hostname, tc.wantSerial)
			}
		})
	}
}
