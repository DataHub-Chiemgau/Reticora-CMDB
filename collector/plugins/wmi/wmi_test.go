package wmi

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", p.Concurrency, defaultConcurrency)
	}
	if p.client == nil {
		t.Fatal("expected http client to be initialized")
	}
	// COL-02: the default client verifies certificates (system roots).
	if tr, ok := p.client.Transport.(*http.Transport); !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("the default WinRM client must verify certificates")
	}
	if p.Name() != "wmi" {
		t.Errorf("Name() = %q, want wmi", p.Name())
	}
}

// TestBuildWSManEnumerate: Enumerate and Pull carry the headers DSP0226
// requires in their namespaces; Enumerate asks for OptimizeEnumeration, Pull
// continues the enumeration context.
func TestBuildWSManEnumerate(t *testing.T) {
	const to = "https://192.0.2.30:5986/wsman"
	parse := func(env string) fakeRequest {
		t.Helper()
		var req fakeRequest
		if err := xml.Unmarshal([]byte(env), &req); err != nil {
			t.Fatalf("envelope is no SOAP 1.2 envelope: %v\n%s", err, env)
		}
		return req
	}
	env := buildWSManEnumerate(to, "Win32_OperatingSystem", "uuid:6f0b6f0e-0000-4000-8000-000000000001")
	req := parse(env)
	if req.Header.Action != "http://schemas.xmlsoap.org/ws/2004/09/enumeration/Enumerate" ||
		req.Header.ResourceURI != "http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/Win32_OperatingSystem" ||
		req.Header.MessageID != "uuid:6f0b6f0e-0000-4000-8000-000000000001" || req.Header.To != to {
		t.Errorf("Enumerate headers = %+v", req.Header)
	}
	if req.Body.Enumerate == nil || req.Body.Enumerate.Optimize == nil || req.Body.Pull != nil {
		t.Errorf("Enumerate body without wsman:OptimizeEnumeration:\n%s", env)
	}

	// Pull continues the (escaped) context under its own MessageID.
	env = buildWSManPull(to, "Win32_BIOS", "uuid:6f0b6f0e-0000-4000-8000-000000000002", "uuid:ctx&<1>")
	req = parse(env)
	if req.Header.Action != "http://schemas.xmlsoap.org/ws/2004/09/enumeration/Pull" ||
		req.Header.ResourceURI != "http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/Win32_BIOS" ||
		req.Header.MessageID != "uuid:6f0b6f0e-0000-4000-8000-000000000002" || req.Header.To != to {
		t.Errorf("Pull headers = %+v", req.Header)
	}
	if req.Body.Pull == nil || req.Body.Pull.Context != "uuid:ctx&<1>" || req.Body.Enumerate != nil {
		t.Errorf("Pull body does not continue the context:\n%s", env)
	}

	// Every request has its own MessageID.
	if a, b := newMessageID(), newMessageID(); a == b || len(a) != len("uuid:")+36 || !strings.HasPrefix(a, "uuid:") {
		t.Errorf("MessageIDs %q, %q: want distinct uuid: URIs", a, b)
	}
}

func TestExtractWMIField(t *testing.T) {
	resp := `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <p:Win32_ComputerSystem xmlns:p="http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2">
      <p:Name>WIN-SERVER-01</p:Name>
      <p:Manufacturer>Microsoft Corporation</p:Manufacturer>
      <p:Model>Virtual Machine</p:Model>
    </p:Win32_ComputerSystem>
  </s:Body>
</s:Envelope>`

	cases := []struct {
		name  string
		resp  string
		field string
		want  string
	}{
		{"name", resp, "Name", "WIN-SERVER-01"},
		{"manufacturer", resp, "Manufacturer", "Microsoft Corporation"},
		{"model", resp, "Model", "Virtual Machine"},
		{"missing field", resp, "SerialNumber", ""},
		{"empty response", "", "Name", ""},
		{"malformed xml", "<not-closed", "Name", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractWMIField(c.resp, c.field); got != c.want {
				t.Errorf("extractWMIField(_, %q) = %q, want %q", c.field, got, c.want)
			}
		})
	}
}

// acceptAndClose accepts connections on ln and closes them; it counts them.
func acceptAndClose(t *testing.T, ln net.Listener) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			conn.Close()
		}
	}()
	return &n
}

func TestDiscoverDetectsWinRMHTTPS(t *testing.T) {
	ln, ip := auditListen(t, winRMHTTPSPort)
	acceptAndClose(t, ln)

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Probe(context.Background(), []string{ip}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.CIType != "windows-server" {
		t.Errorf("CIType = %q, want windows-server", r.CIType)
	}
	if r.Attributes["winrmScheme"] != "https" {
		t.Errorf("winrmScheme = %v, want https", r.Attributes["winrmScheme"])
	}
	if r.Attributes["winrmPort"] != winRMHTTPSPort {
		t.Errorf("winrmPort = %v, want %d", r.Attributes["winrmPort"], winRMHTTPSPort)
	}
	if r.Attributes["winrmCapable"] != true {
		t.Errorf("winrmCapable = %v, want true", r.Attributes["winrmCapable"])
	}
}

// COL-02: a host that offers WinRM only over plain HTTP (5985) is not a WMI
// target; the probe does not even connect to that port.
func TestDiscoverIgnoresPlainHTTPWinRM(t *testing.T) {
	ln, ip := auditListen(t, plainWinRMPort)
	conns := acceptAndClose(t, ln)

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Probe(context.Background(), []string{ip}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 || conns.Load() != 0 {
		t.Fatalf("plain-HTTP WinRM must be ignored: results=%+v, connections to %d = %d", results, plainWinRMPort, conns.Load())
	}
}

func TestDiscoverSkipsUnreachableTarget(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond, Concurrency: 2}
	results, err := p.Probe(context.Background(), []string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Probe(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("expected empty non-nil slice, got %#v", results)
	}
}

func TestDiscoverCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Plugin{Timeout: 100 * time.Millisecond}
	results, err := p.Probe(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCollectRequiresCredentials(t *testing.T) {
	p := &Plugin{Timeout: 100 * time.Millisecond}
	_, err := p.collect(context.Background(), "127.0.0.1", nil)
	if err == nil || !strings.Contains(err.Error(), "credentials required") {
		t.Errorf("expected credentials error, got %v", err)
	}
}

// windowsServer is the inventory of a Windows server as WinRM delivers it:
// the properties of each instance per class.
var windowsServer = map[string][]string{
	"Win32_OperatingSystem": {`<p:BuildNumber>20348</p:BuildNumber><p:Caption>Microsoft Windows Server 2022 Standard</p:Caption><p:Version>10.0.20348</p:Version>`},
	"Win32_ComputerSystem": {`<p:DNSHostName>win-server-01</p:DNSHostName><p:Domain>corp.example</p:Domain><p:Manufacturer>Dell Inc.</p:Manufacturer>` +
		`<p:Model>PowerEdge R650</p:Model><p:Name>WIN-SERVER-01</p:Name><p:NumberOfProcessors>2</p:NumberOfProcessors>`},
	"Win32_BIOS":                  {`<p:SerialNumber>SVCTAG42</p:SerialNumber><p:SMBIOSBIOSVersion>1.9.2</p:SMBIOSBIOSVersion>`},
	"Win32_ComputerSystemProduct": {`<p:UUID>4C4C4544-0042-3510-8051-B4C04F4A3132</p:UUID>`},
	"Win32_NetworkAdapterConfiguration": {
		`<p:Description>Intel(R) Ethernet 10G</p:Description><p:IPEnabled>true</p:IPEnabled><p:MACAddress>B0:26:28:AA:BB:01</p:MACAddress>`,
		`<p:Description>WAN Miniport (IP)</p:Description><p:IPEnabled>false</p:IPEnabled><p:MACAddress xsi:nil="true"/>`,
		`<p:Description>Intel(R) Ethernet 10G #2</p:Description><p:IPEnabled>true</p:IPEnabled><p:MACAddress>B0:26:28:AA:BB:02</p:MACAddress>`,
	},
}

// collectFrom runs Collect against handler, served over HTTPS on 5986 with a
// self-signed certificate that the SPKI pin of the target's TLS scope
// trusts.
func collectFrom(t *testing.T, handler http.Handler) (*plugins.DeviceRecord, error) {
	t.Helper()
	ln, ip := auditListen(t, winRMHTTPSPort)
	srv := serveWinRM(t, ln, handler, true)
	p := New(WithTLSScopes(pinScope(t, ip, srv.Certificate())))
	return p.Collect(context.Background(), ip, map[string]string{"username": "admin", "password": "pw"})
}

// TestCollectParsesWSManResponses: Collect enumerates every class as DSP0226
// specifies – Enumerate with OptimizeEnumeration and MessageID, then Pull
// until EndOfSequence – and fills the DeviceRecord from the instances,
// whether WinRM honours OptimizeEnumeration or not and however it splits the
// instances into batches (DIS-04).
func TestCollectParsesWSManResponses(t *testing.T) {
	cases := []struct {
		name string
		fake *winRMFake
		// pulls is the number of Pull requests the instances need.
		pulls int
	}{
		{"OptimizeEnumeration honoured, all instances in the EnumerateResponse", &winRMFake{instances: windowsServer, batch: 10, optimize: true}, 0},
		{"OptimizeEnumeration honoured, one instance per response", &winRMFake{instances: windowsServer, batch: 1, optimize: true}, 2},
		{"OptimizeEnumeration ignored, every instance with Pull", &winRMFake{instances: windowsServer, batch: 1}, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := collectFrom(t, tc.fake)
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			requests, open := tc.fake.log()
			var enumerates, pulls int
			for _, r := range requests {
				switch {
				case strings.HasPrefix(r, "Enumerate ") && strings.HasSuffix(r, " optimize=true"):
					enumerates++
				case strings.HasPrefix(r, "Pull "):
					pulls++
				default:
					t.Errorf("request %q: want Enumerate with OptimizeEnumeration or Pull", r)
				}
			}
			if enumerates != len(wmiClasses) || pulls != tc.pulls || open != 0 {
				t.Errorf("requests %v: %d Enumerate (want %d), %d Pull (want %d), %d enumerations not pulled to EndOfSequence",
					requests, enumerates, len(wmiClasses), pulls, tc.pulls, open)
			}
			id, cl, sys := rec.Identity, rec.Classification, rec.System
			if id.SerialNumber != "SVCTAG42" || id.HardwareUUID != "4c4c4544-0042-3510-8051-b4c04f4a3132" || id.Hostname != "win-server-01" ||
				id.FQDN != "win-server-01.corp.example" || id.PrimaryMAC != "b0:26:28:aa:bb:01" {
				t.Errorf("identity = %+v", id)
			}
			if cl.CIType != "windows-server" || cl.Manufacturer != "Dell Inc." || cl.Model != "PowerEdge R650" {
				t.Errorf("classification = %+v", cl)
			}
			if sys.Name != "WIN-SERVER-01" || sys.OSName != "Microsoft Windows Server 2022 Standard" || sys.OSVersion != "10.0.20348" || sys.FirmwareVersion != "1.9.2" {
				t.Errorf("system = %+v", sys)
			}
			if rec.Attributes["domain"] != "corp.example" || rec.Attributes["osBuild"] != "20348" || rec.Attributes["cpuCount"] != "2" {
				t.Errorf("attributes = %v", rec.Attributes)
			}
			if len(rec.Interfaces) != 2 || rec.Interfaces[1].MAC != "b0:26:28:aa:bb:02" {
				t.Errorf("interfaces = %+v", rec.Interfaces)
			}
		})
	}
}

// TestCollectUnreachableWinRMFails: a WinRM port that accepts the
// connection but never answers fails the collection with unreachable after
// the timeout; it does not report a record with empty fields (DIS-08).
func TestCollectUnreachableWinRMFails(t *testing.T) {
	ln, ip := auditListen(t, winRMHTTPSPort)
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	p := New()
	p.client.Timeout = 300 * time.Millisecond

	rec, err := p.Collect(context.Background(), ip, map[string]string{"username": "u", "password": "p"})
	if !errors.Is(err, plugins.ErrUnreachable) || rec != nil {
		t.Fatalf("Collect = %+v, %v; want no record and an unreachable error", rec, err)
	}
	if got := plugins.ErrorCause(err); got != "unreachable" {
		t.Errorf("cause = %q, want unreachable", got)
	}
}

// TestApplyIdentityFillsDeviceRecordFields covers WP-152 (DIS-04): WMI
// delivers hardware UUID, host name/FQDN, adapters with MACs and uptime.
func TestApplyIdentityFillsDeviceRecordFields(t *testing.T) {
	cs := `<s:Body><p:Win32_ComputerSystem><p:Name>WIN-SERVER-01</p:Name><p:DNSHostName>win-server-01</p:DNSHostName><p:Domain>corp.example</p:Domain></p:Win32_ComputerSystem></s:Body>`
	product := `<s:Body><p:Win32_ComputerSystemProduct><p:UUID>4C4C4544-0042-3510-8051-B4C04F4A4E33</p:UUID></p:Win32_ComputerSystemProduct></s:Body>`
	nics := `<s:Body>
<p:Win32_NetworkAdapterConfiguration><p:Description>Intel(R) Ethernet 10G</p:Description><p:IPEnabled>true</p:IPEnabled><p:MACAddress>B0:26:28:AA:BB:01</p:MACAddress></p:Win32_NetworkAdapterConfiguration>
<p:Win32_NetworkAdapterConfiguration><p:Description>WAN Miniport</p:Description><p:IPEnabled>false</p:IPEnabled><p:MACAddress>00:00:00:00:00:01</p:MACAddress></p:Win32_NetworkAdapterConfiguration>
<p:Win32_NetworkAdapterConfiguration><p:Description>Intel(R) Ethernet 10G #2</p:Description><p:IPEnabled>true</p:IPEnabled><p:MACAddress>B0:26:28:AA:BB:02</p:MACAddress></p:Win32_NetworkAdapterConfiguration>
</s:Body>`
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	res := &plugins.Result{CIType: "windows-server", Name: "WIN-SERVER-01", IP: "192.0.2.30", Attributes: map[string]any{"osName": "Microsoft Windows Server 2022", "osVersion": "10.0.20348"}}
	applyIdentity(res, cs, product, nics, "20261013100000.000000+120", now) // booted 08:00 UTC

	rec := plugins.RecordFromResult("wmi", res, now)
	if rec.Identity.HardwareUUID != "4c4c4544-0042-3510-8051-b4c04f4a4e33" || rec.Identity.Hostname != "win-server-01" ||
		rec.Identity.FQDN != "win-server-01.corp.example" || rec.Identity.PrimaryMAC != "b0:26:28:aa:bb:01" {
		t.Errorf("identity = %+v", rec.Identity)
	}
	if rec.System.OSName != "Microsoft Windows Server 2022" || rec.System.OSVersion != "10.0.20348" || rec.System.UptimeSeconds != 7200 {
		t.Errorf("system = %+v", rec.System)
	}
	if len(rec.Interfaces) != 2 || rec.Interfaces[1].Name != "Intel(R) Ethernet 10G #2" {
		t.Errorf("interfaces = %+v", rec.Interfaces)
	}
	if v := rec.Validate(); len(v) > 0 {
		t.Errorf("record breaks the contract: %v", v)
	}
	if _, err := parseCIMDateTime("garbage"); err == nil {
		t.Error("a malformed datetime must be refused")
	}
}

// TestAuditWMICredentialTransport encodes COL-02 for the WMI plugin (F-010):
// the account is never readable on the wire, neither over plain HTTP nor to
// an HTTPS endpoint whose certificate is not trusted; such a collection
// fails with tls_untrusted before anything is sent. The SPKI pin of the
// target's TLS scope makes a self-signed certificate trusted.
func TestAuditWMICredentialTransport(t *testing.T) {
	wrongPin := sha256.Sum256([]byte("other key"))
	cases := []struct {
		name string
		// https serves WinRM over TLS (self-signed) on 5986 next to the
		// plain-HTTP WinRM on 5985 of the same address.
		https bool
		// scopes are the TLS scopes of the plugin for the endpoint.
		scopes   func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope
		trusted  bool
		redirect bool
	}{
		{name: "production default New(): plain HTTP WinRM on 5985 only"},
		{name: "untrusted self-signed certificate on 5986 without TLS scope", https: true},
		{name: "self-signed certificate on 5986 with a wrong pin", https: true,
			scopes: func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope {
				scope := pinScope(t, ip, cert)
				scope.PinsSHA256 = [][]byte{wrongPin[:]}
				return []plugins.TLSScope{scope}
			}},
		{name: "matching pin of another network", https: true,
			scopes: func(t *testing.T, _ string, cert *x509.Certificate) []plugins.TLSScope {
				scope := pinScope(t, "10.0.0.1", cert)
				return []plugins.TLSScope{scope}
			}},
		{name: "self-signed certificate on 5986 with the matching pin", https: true, trusted: true,
			scopes: func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope {
				return []plugins.TLSScope{pinScope(t, ip, cert)}
			}},
		{name: "trusted endpoint redirects to plain HTTP on 5985", https: true, trusted: true, redirect: true,
			scopes: func(t *testing.T, ip string, cert *x509.Certificate) []plugins.TLSScope {
				return []plugins.TLSScope{pinScope(t, ip, cert)}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A DSP0226-conformant WinRM without instances: one Enumerate
			// per class ends the sequence.
			soap := &winRMFake{optimize: true}
			ln, ip := auditListen(t, plainWinRMPort)
			plain := &auditRecorder{}
			serveWinRM(t, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				plain.add(r)
				soap.ServeHTTP(w, r)
			}), false)

			secure := &auditRecorder{}
			var opts []Option
			if tc.https {
				tlsLn, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(winRMHTTPSPort)))
				if err != nil {
					t.Fatal(err)
				}
				srv := serveWinRM(t, tlsLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					secure.add(r)
					if tc.redirect {
						http.Redirect(w, r, "http://"+net.JoinHostPort(ip, fmt.Sprint(plainWinRMPort))+"/wsman", http.StatusTemporaryRedirect)
						return
					}
					soap.ServeHTTP(w, r)
				}), true)
				if tc.scopes != nil {
					opts = append(opts, WithTLSScopes(tc.scopes(t, ip, srv.Certificate())...))
				}
			}

			p := New(opts...)
			p.Timeout = 2 * time.Second
			_, err := p.Collect(context.Background(), ip, map[string]string{"username": auditUser, "password": auditPassword})

			if leaked := plain.leaked(); len(leaked) > 0 || plain.count() > 0 {
				t.Errorf("COL-02 violated: the plain-HTTP WinRM endpoint %s received %d request(s), %d with the WMI password readable",
					net.JoinHostPort(ip, fmt.Sprint(plainWinRMPort)), plain.count(), len(leaked))
			}
			switch {
			case tc.https && !tc.trusted:
				if leaked := secure.leaked(); len(leaked) > 0 || secure.count() > 0 {
					t.Errorf("COL-02 violated: the WinRM endpoint %s with an untrusted certificate received %d request(s), %d with the WMI password",
						net.JoinHostPort(ip, fmt.Sprint(winRMHTTPSPort)), secure.count(), len(leaked))
				}
				if !errors.Is(err, plugins.ErrTLSUntrusted) || !strings.Contains(err.Error(), "tls_untrusted") {
					t.Errorf("Collect error = %v, want tls_untrusted", err)
				}
			case tc.redirect:
				if secure.count() == 0 {
					t.Error("the trusted endpoint received no request")
				}
			case tc.trusted:
				// The credentials reach the verified endpoint, over TLS only.
				if err != nil || secure.count() != len(wmiClasses) || len(secure.leaked()) != len(wmiClasses) {
					t.Errorf("trusted endpoint: err=%v, requests=%d, with credentials=%d (want %d)", err, secure.count(), len(secure.leaked()), len(wmiClasses))
				}
			}
		})
	}
}

// TestAuditWMIFailuresAreErrors encodes DIS-04/DIS-08 (F-029, moved from
// wmi_auditdefect_test.go in WP-415): WMI either delivers the DeviceRecord
// fields or reports a failure with its cause; it never reports success with
// empty fields. Refused credentials are auth_failed, a failed or broken
// connection is unreachable; any other non-2xx answer, SOAP fault or
// non-SOAP answer is an error of its own. Control: a DSP0226-conformant
// WinRM (EnumerationContext, then Pull) delivers serial number, model and OS.
func TestAuditWMIFailuresAreErrors(t *testing.T) {
	status := func(code int, header ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if len(header) > 0 {
				w.Header().Set("WWW-Authenticate", header[0])
			}
			w.WriteHeader(code)
		}
	}
	fault := func(subcode, reason string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { writeFault(w, "", subcode, reason) }
	}
	cases := []struct {
		name    string
		handler http.Handler // nil: nothing listens (connection refused)
		// wantErr is the cause the error carries; nil: an error that is
		// neither auth_failed nor unreachable.
		wantErr error
		// wantText is part of the error message; empty for the control.
		wantText string
	}{
		{name: "WinRM refuses the credentials (401 Negotiate)", handler: status(http.StatusUnauthorized, "Negotiate"),
			wantErr: plugins.ErrAuthFailed, wantText: "401 Unauthorized"},
		{name: "WinRM denies the account (403)", handler: status(http.StatusForbidden),
			wantErr: plugins.ErrAuthFailed, wantText: "403 Forbidden"},
		{name: "WMI denies the account (SOAP fault AccessDenied)", handler: fault("w:AccessDenied", "Access is denied."),
			wantErr: plugins.ErrAuthFailed, wantText: "SOAP fault AccessDenied: Access is denied."},
		{name: "WinRM port refuses connections", handler: nil,
			wantErr: plugins.ErrUnreachable, wantText: "connection refused"},
		{name: "WinRM drops the connection", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				conn.Close()
			}
		}), wantErr: plugins.ErrUnreachable, wantText: "Enumerate Win32_OperatingSystem"},
		{name: "WinRM answers a SOAP fault (HTTP 500)", handler: fault("w:InternalError", "The WS-Management service cannot process the request."),
			wantText: "SOAP fault InternalError (HTTP status 500): The WS-Management service cannot process the request."},
		{name: "WinRM answers 503 without a body", handler: status(http.StatusServiceUnavailable),
			wantText: "unexpected HTTP status 503 Service Unavailable"},
		{name: "WinRM answers 200 without a SOAP envelope", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "<html><body>IIS</body></html>")
		}), wantText: "malformed response"},
		{name: "WinRM answers the Enumerate without EnumerateResponse", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeEnvelope(w, http.StatusOK, wsmanActionEnumerate+"Response", "", "", "")
		}), wantText: "without EnumerateResponse"},
		{name: "WinRM faults the Pull", handler: &winRMFake{instances: windowsServer, pullFault: "w:TimedOut"},
			wantText: "Pull Win32_OperatingSystem: SOAP fault TimedOut"},
		{name: "WinRM never ends the sequence", handler: &winRMFake{instances: windowsServer, endless: true},
			wantText: fmt.Sprintf("no EndOfSequence after %d Pull requests", maxPulls)},
		{name: "control: WinRM answers DSP0226-conformant (EnumerationContext + Pull)", handler: &winRMFake{instances: windowsServer, batch: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec *plugins.DeviceRecord
			var err error
			if tc.handler == nil {
				ln, ip := auditListen(t, winRMHTTPSPort)
				_ = ln.Close()
				rec, err = New().Collect(context.Background(), ip, map[string]string{"username": "u", "password": "p"})
			} else {
				rec, err = collectFrom(t, tc.handler)
			}
			if tc.wantText == "" {
				if err != nil {
					t.Fatalf("control: Collect: %v", err)
				}
				if rec.Identity.SerialNumber != "SVCTAG42" || rec.Classification.Model != "PowerEdge R650" ||
					rec.System.OSName != "Microsoft Windows Server 2022 Standard" || rec.System.OSVersion != "10.0.20348" {
					t.Errorf("control: serial=%q model=%q os=%q %q, want the WinRM endpoint's values",
						rec.Identity.SerialNumber, rec.Classification.Model, rec.System.OSName, rec.System.OSVersion)
				}
				return
			}
			if err == nil {
				t.Fatalf("Collect reported success: serial=%q hostname=%q (want an error with %q)", rec.Identity.SerialNumber, rec.Identity.Hostname, tc.wantText)
			}
			if rec != nil {
				t.Errorf("Collect returned a record next to the error %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not name %q", err, tc.wantText)
			}
			for _, cause := range []error{plugins.ErrAuthFailed, plugins.ErrUnreachable} {
				if errors.Is(err, cause) != (cause == tc.wantErr) {
					t.Errorf("error %q: errors.Is(%v) = %v, want cause %v", err, cause, errors.Is(err, cause), tc.wantErr)
				}
			}
		})
	}
}
