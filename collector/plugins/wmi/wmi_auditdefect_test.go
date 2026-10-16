//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests (must fail
// until the defect is fixed).
//
// WP-152 (DIS-04), WP-147 / WP-155 (COL-02, DIS-08).
//
// Findings encoded here:
//   - P0 "WMI plugin sends credentials in cleartext (HTTP Basic over
//     http://target:5985)": New() talks plain HTTP to 5985 with Basic auth;
//     the HTTPS mode uses InsecureSkipVerify. COL-02: device credentials are
//     never sent unencrypted or to unverified endpoints.
//   - P1 "WMI Collect reports success with empty fields when WinRM refuses
//     auth or answers protocol-conformantly": wsmanEnumerate swallows
//     transport errors and HTTP status codes and never sends a WS-Man Pull,
//     so a 401, a refused connection or a DSP0226-conformant
//     EnumerationContext answer all yield a "successful" empty record
//     (DIS-04 fields, DIS-08 visible failures).
//
// The fake WinRM endpoints listen on a random 127.x.y.z loopback address on
// the fixed WinRM ports so parallel runs do not collide.
//
// Retest:
//
//	cd collector && go test -tags auditdefect -count=1 -v -run 'TestAuditWMI' ./plugins/wmi/
package wmi

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	auditUser     = `CORP\audit-admin`
	auditPassword = "S3cret!-audit-wmi"
)

// auditListen binds port on a random 127.x.y.z address.
func auditListen(t *testing.T, port int) (net.Listener, string) {
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

// auditRecorder records every request a fake WinRM endpoint receives.
type auditRecorder struct {
	mu   sync.Mutex
	dump []string
}

func (r *auditRecorder) add(req *http.Request) {
	raw, _ := httputil.DumpRequest(req, true)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dump = append(r.dump, string(raw))
}

// leaked reports the requests that carry the password readable (plain text
// or as HTTP Basic credentials).
func (r *auditRecorder) leaked() []string {
	basic := base64.StdEncoding.EncodeToString([]byte(auditUser + ":" + auditPassword))
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.dump {
		if strings.Contains(d, auditPassword) || strings.Contains(d, basic) {
			first, _, _ := strings.Cut(d, "\r\n")
			out = append(out, first)
		}
	}
	return out
}

// TestAuditWMICredentialTransport encodes COL-02 for the WMI plugin: the
// account is never readable on the wire, neither over plain HTTP nor to an
// HTTPS endpoint whose certificate is not trusted.
func TestAuditWMICredentialTransport(t *testing.T) {
	cases := []struct {
		name  string
		port  int
		tls   bool
		https bool
	}{
		{name: "production default New(): plain HTTP WinRM on 5985", port: defaultHTTPPort},
		{name: "HTTPS mode: untrusted self-signed certificate on 5986", port: defaultHTTPSPort, tls: true, https: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, ip := auditListen(t, tc.port)
			rec := &auditRecorder{}
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.add(r)
				w.Header().Set("Content-Type", "application/soap+xml")
				_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`)
			}))
			_ = srv.Listener.Close()
			srv.Listener = ln
			if tc.tls {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()

			p := New()
			p.Timeout = 2 * time.Second
			p.UseHTTPS = tc.https
			_, _ = p.Collect(context.Background(), ip, map[string]string{"username": auditUser, "password": auditPassword})

			if leaked := rec.leaked(); len(leaked) > 0 {
				t.Errorf("COL-02 violated: the WinRM endpoint %s (tls=%v) received the WMI password readable in %d request(s), e.g. %q",
					net.JoinHostPort(ip, fmt.Sprint(tc.port)), tc.tls, len(leaked), leaked[0])
			}
		})
	}
}

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
			ln, ip := auditListen(t, defaultHTTPPort)
			if tc.handler == nil {
				_ = ln.Close()
			} else {
				srv := &http.Server{Handler: tc.handler, ReadHeaderTimeout: 2 * time.Second}
				go func() { _ = srv.Serve(ln) }()
				defer srv.Close()
			}
			p := New()
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
