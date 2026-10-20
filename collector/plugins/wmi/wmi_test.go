package wmi

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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

func TestBuildWSManEnumerate(t *testing.T) {
	env := buildWSManEnumerate("Win32_OperatingSystem")
	for _, want := range []string{
		"<s:Envelope",
		"wsen:Enumerate",
		"http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/Win32_OperatingSystem",
		"http://schemas.xmlsoap.org/ws/2004/09/enumeration/Enumerate",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("envelope missing %q", want)
		}
	}

	// Different class must land in the ResourceURI.
	env2 := buildWSManEnumerate("Win32_BIOS")
	if !strings.Contains(env2, "root/cimv2/Win32_BIOS") {
		t.Errorf("envelope for Win32_BIOS missing ResourceURI: %s", env2)
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

func TestCollectParsesWSManResponses(t *testing.T) {
	// WinRM SOAP responder that echoes field values for the requested class.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		fmt.Fprintf(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<Name>WIN-SERVER-01</Name>
<Manufacturer>Dell Inc.</Manufacturer>
<Model>PowerEdge T640</Model>
<SerialNumber>SVCTAG42</SerialNumber>
<SMBIOSBIOSVersion>2.14.0</SMBIOSBIOSVersion>
<Caption>Microsoft Windows Server 2022</Caption>
<Version>10.0.20348</Version>
<BuildNumber>20348</BuildNumber>
<Domain>corp.example</Domain>
</s:Body></s:Envelope>`)
	})
	// WinRM over HTTPS on the fixed port; the self-signed certificate is
	// trusted through the SPKI pin of the target's TLS scope.
	ln, ip := auditListen(t, winRMHTTPSPort)
	srv := serveWinRM(t, ln, handler, true)

	p := New(WithTLSScopes(pinScope(t, ip, srv.Certificate())))
	p.Timeout = 2 * time.Second

	res, err := p.collect(context.Background(), ip, map[string]string{
		"username": "admin", "password": "pw",
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Name != "WIN-SERVER-01" {
		t.Errorf("Name = %q, want WIN-SERVER-01", res.Name)
	}
	if res.Manufacturer != "Dell Inc." {
		t.Errorf("Manufacturer = %q", res.Manufacturer)
	}
	if res.Serial != "SVCTAG42" {
		t.Errorf("Serial = %q", res.Serial)
	}
	if res.Attributes["osName"] != "Microsoft Windows Server 2022" {
		t.Errorf("osName = %v", res.Attributes["osName"])
	}
	if res.Attributes["domain"] != "corp.example" {
		t.Errorf("domain = %v", res.Attributes["domain"])
	}
}

func TestCollectUnreachableWinRMReturnsEmptyFields(t *testing.T) {
	// Credentials provided, but nothing listens on the WinRM port ->
	// wsmanEnumerate silently returns "" for every class, leaving fields
	// empty but not failing. Use New() so the HTTP client is initialized.
	p := New()
	p.Timeout = 300 * time.Millisecond
	p.client.Timeout = 300 * time.Millisecond

	// Bind 5986 to ensure the port is occupied before closing it, then use a
	// port we control to guarantee connection refusal.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", winRMHTTPSPort))
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:%d: %v", winRMHTTPSPort, err)
	}
	ln.Close() // now 127.0.0.1:5986 refuses connections

	res, err := p.collect(context.Background(), "127.0.0.1", map[string]string{"username": "u", "password": "p"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Name != "127.0.0.1" {
		t.Errorf("Name = %q, want target fallback", res.Name)
	}
	if res.CIType != "windows-server" {
		t.Errorf("CIType = %q, want windows-server", res.CIType)
	}
	if res.Manufacturer != "" || res.Serial != "" {
		t.Errorf("expected empty hardware fields, got mfg=%q serial=%q", res.Manufacturer, res.Serial)
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
	soap := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`)
	})
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
			ln, ip := auditListen(t, plainWinRMPort)
			plain := &auditRecorder{}
			serveWinRM(t, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				plain.add(r)
				soap(w, r)
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
					soap(w, r)
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
