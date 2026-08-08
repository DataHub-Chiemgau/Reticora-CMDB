package wmi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	p := New()
	if p.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", p.Timeout, defaultTimeout)
	}
	if p.Concurrency != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", p.Concurrency, defaultConcurrency)
	}
	if p.UseHTTPS {
		t.Error("UseHTTPS should default to false")
	}
	if p.client == nil {
		t.Error("expected http client to be initialized")
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

// listenOrSkip starts a TCP listener on the given port, closing connections.
func listenOrSkip(t *testing.T, port int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:%d: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	return ln
}

func TestDiscoverDetectsWinRMHTTP(t *testing.T) {
	listenOrSkip(t, defaultHTTPPort)

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
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
	if r.Attributes["winrmScheme"] != "http" {
		t.Errorf("winrmScheme = %v, want http", r.Attributes["winrmScheme"])
	}
	if r.Attributes["winrmPort"] != defaultHTTPPort {
		t.Errorf("winrmPort = %v, want %d", r.Attributes["winrmPort"], defaultHTTPPort)
	}
	if r.Attributes["winrmCapable"] != true {
		t.Errorf("winrmCapable = %v, want true", r.Attributes["winrmCapable"])
	}
}

func TestDiscoverFallsBackToHTTPSPort(t *testing.T) {
	listenOrSkip(t, defaultHTTPSPort)

	p := &Plugin{Timeout: time.Second, Concurrency: 4}
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Attributes["winrmScheme"] != "https" {
		t.Errorf("winrmScheme = %v, want https", results[0].Attributes["winrmScheme"])
	}
	if results[0].Attributes["winrmPort"] != defaultHTTPSPort {
		t.Errorf("winrmPort = %v, want %d", results[0].Attributes["winrmPort"], defaultHTTPSPort)
	}
}

func TestDiscoverSkipsUnreachableTarget(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond, Concurrency: 2}
	results, err := p.Discover(context.Background(), []string{"192.0.2.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

func TestDiscoverNoTargetsReturnsEmptySlice(t *testing.T) {
	p := New()
	results, err := p.Discover(context.Background(), nil, nil)
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
	results, err := p.Discover(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestCollectRequiresCredentials(t *testing.T) {
	p := &Plugin{Timeout: 100 * time.Millisecond}
	_, err := p.Collect(context.Background(), "127.0.0.1", nil)
	if err == nil || !strings.Contains(err.Error(), "credentials required") {
		t.Errorf("expected credentials error, got %v", err)
	}
}

func TestCollectParsesWSManResponses(t *testing.T) {
	// WinRM SOAP responder that echoes field values for the requested class.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	defer srv.Close()

	p := New()
	p.Timeout = 2 * time.Second
	p.client = srv.Client()

	// Serve the test handler on the default WinRM port so Collect's fixed
	// http://target:5985/wsman URL reaches it without production changes.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultHTTPPort))
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:%d: %v", defaultHTTPPort, err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.Config.Handler.ServeHTTP(newConnResponseWriter(conn), mustReadRequest(conn))
		}
	}()

	res, err := p.Collect(context.Background(), "127.0.0.1", map[string]string{
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
	// Credentials provided, but nothing listens -> wsmanEnumerate silently
	// returns "" for every class, leaving fields empty but not failing.
	p := &Plugin{Timeout: 300 * time.Millisecond}
	res, err := p.Collect(context.Background(), "192.0.2.77", map[string]string{"username": "u", "password": "p"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Name != "192.0.2.77" {
		t.Errorf("Name = %q, want target fallback", res.Name)
	}
	if res.CIType != "windows-server" {
		t.Errorf("CIType = %q, want windows-server", res.CIType)
	}
	if res.Manufacturer != "" || res.Serial != "" {
		t.Errorf("expected empty hardware fields, got mfg=%q serial=%q", res.Manufacturer, res.Serial)
	}
}

// connResponseWriter is a minimal http.ResponseWriter over a raw connection,
// used to serve the httptest handler on the fixed WinRM port.
type connResponseWriter struct {
	conn       net.Conn
	header     http.Header
	statusCode int
}

func newConnResponseWriter(conn net.Conn) *connResponseWriter {
	return &connResponseWriter{conn: conn, header: http.Header{}}
}

func (w *connResponseWriter) Header() http.Header { return w.header }

func (w *connResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(b)
}

func (w *connResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.header.Set("Connection", "close")
	fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
	w.header.Write(w.conn)
	fmt.Fprintf(w.conn, "\r\n")
}

func mustReadRequest(conn net.Conn) *http.Request {
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		conn.Close()
		return &http.Request{Method: http.MethodPost, Header: http.Header{}, Body: http.NoBody}
	}
	return req
}
