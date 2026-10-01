package redfish

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		t.Error("expected http client to be initialized")
	}
	if p.Name() != "redfish" {
		t.Errorf("Name() = %q, want redfish", p.Name())
	}
}

// newTestPlugin returns a plugin whose HTTP client rewrites any request to
// the given httptest server, allowing Collect's hardcoded https://:443 URLs
// to be exercised without touching production code.
func newTestPlugin(t *testing.T, srv *httptest.Server) *Plugin {
	t.Helper()
	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	p := New()
	base := http.DefaultTransport
	p.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			r := req.Clone(req.Context())
			r.URL.Scheme = srvURL.Scheme
			r.URL.Host = srvURL.Host
			r.Host = srvURL.Host
			return base.RoundTrip(r)
		}),
	}
	return p
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeBMC serves a minimal Redfish ServiceRoot, Systems collection, and one
// ComputerSystem.
func fakeBMC(t *testing.T, sys computerSystem) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, serviceRoot{
			RedfishVersion: "1.13.0",
			UUID:           "bmc-uuid-123",
			Product:        "iDRAC",
			Vendor:         "Dell",
			Systems:        link{ID: "/redfish/v1/Systems"},
		})
	})
	mux.HandleFunc("/redfish/v1/Systems", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, systemCollection{Members: []link{{ID: "/redfish/v1/Systems/1"}}})
	})
	mux.HandleFunc("/redfish/v1/Systems/1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, sys)
	})
	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func TestCollectParsesSystemInventory(t *testing.T) {
	sys := computerSystem{
		ID:           "1",
		Name:         "compute-node-01",
		Manufacturer: "Dell Inc.",
		Model:        "PowerEdge R750",
		SerialNumber: "SN12345",
		HostName:     "node01.example.com",
		BiosVersion:  "2.7.3",
		PowerState:   "On",
		SKU:          "ABC123",
	}
	sys.Status.Health = "OK"
	sys.ProcessorSummary.Count = 2
	sys.ProcessorSummary.Model = "Intel Xeon Gold 6338"
	sys.MemorySummary.TotalSystemMemoryGiB = 256

	p := newTestPlugin(t, fakeBMC(t, sys))
	res, err := p.Collect(context.Background(), "192.0.2.10", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if res.CIType != "server" {
		t.Errorf("CIType = %q, want server (upgraded from bmc)", res.CIType)
	}
	if res.Name != "compute-node-01" {
		t.Errorf("Name = %q, want system name", res.Name)
	}
	if res.Manufacturer != "Dell Inc." {
		t.Errorf("Manufacturer = %q", res.Manufacturer)
	}
	if res.Model != "PowerEdge R750" {
		t.Errorf("Model = %q", res.Model)
	}
	if res.Serial != "SN12345" {
		t.Errorf("Serial = %q", res.Serial)
	}
	if res.Firmware != "2.7.3" {
		t.Errorf("Firmware = %q", res.Firmware)
	}

	attrs := map[string]any{
		"redfishVersion": "1.13.0",
		"bmcUUID":        "bmc-uuid-123",
		"bmcProduct":     "iDRAC",
		"powerState":     "On",
		"healthState":    "OK",
		"cpuCount":       2,
		"memoryGiB":      256.0,
		"sku":            "ABC123",
	}
	for k, want := range attrs {
		if got := res.Attributes[k]; got != want {
			t.Errorf("Attributes[%q] = %v, want %v", k, got, want)
		}
	}
}

func TestCollectFallsBackToHostNameThenTarget(t *testing.T) {
	// No Name on the system -> HostName should win.
	srv := fakeBMC(t, computerSystem{HostName: "fallback-host"})
	p := newTestPlugin(t, srv)
	res, err := p.Collect(context.Background(), "192.0.2.11", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Name != "fallback-host" {
		t.Errorf("Name = %q, want HostName fallback", res.Name)
	}
}

func TestCollectWithoutSystemsKeepsBMCResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/redfish/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, serviceRoot{RedfishVersion: "1.0.0"})
	})
	p := newTestPlugin(t, httptest.NewServer(mux))

	res, err := p.Collect(context.Background(), "192.0.2.12", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.CIType != "bmc" {
		t.Errorf("CIType = %q, want bmc when no systems present", res.CIType)
	}
	if res.Name != "192.0.2.12" {
		t.Errorf("Name = %q, want target fallback", res.Name)
	}
}

func TestCollectServiceRootError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	p := newTestPlugin(t, srv)

	_, err := p.Collect(context.Background(), "192.0.2.13", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected HTTP 500 error, got %v", err)
	}
}

func TestCollectSendsBasicAuth(t *testing.T) {
	var gotUser, gotPass string
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		if ok {
			gotUser, gotPass, sawAuth = u, pw, true
		}
		writeJSON(w, serviceRoot{})
	}))
	p := newTestPlugin(t, srv)

	if _, err := p.Collect(context.Background(), "192.0.2.14", map[string]string{
		"username": "admin", "password": "s3cret",
	}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !sawAuth || gotUser != "admin" || gotPass != "s3cret" {
		t.Errorf("expected basic auth admin/s3cret, got %q/%q (auth=%v)", gotUser, gotPass, sawAuth)
	}
}

func TestDiscoverDetectsOKAndUnauthorized(t *testing.T) {
	srvOK := fakeBMC(t, computerSystem{})
	srvAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	srvNotRedfish := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))

	cases := []struct {
		name      string
		srv       *httptest.Server
		wantFound bool
		wantAuth  bool
	}{
		{"status 200 detected and authenticated", srvOK, true, true},
		{"status 401 detected not authenticated", srvAuth, true, false},
		{"status 404 ignored", srvNotRedfish, false, false},
		{"status 500 ignored", srvErr, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestPlugin(t, c.srv)
			results, err := p.Discover(context.Background(), []string{"192.0.2.20"}, map[string]string{
				"username": "u", "password": "p",
			})
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if !c.wantFound {
				if len(results) != 0 {
					t.Fatalf("expected no results, got %d", len(results))
				}
				return
			}
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0].CIType != "bmc" {
				t.Errorf("CIType = %q, want bmc", results[0].CIType)
			}
			if got := results[0].Attributes["authenticated"]; got != c.wantAuth {
				t.Errorf("authenticated = %v, want %v", got, c.wantAuth)
			}
			root, _ := results[0].Attributes["redfishRoot"].(string)
			if !strings.Contains(root, serviceRootPath) {
				t.Errorf("redfishRoot = %q, want path %q", root, serviceRootPath)
			}
		})
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
	p := New()
	results, err := p.Discover(ctx, []string{"192.0.2.1"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover error = %v, want context.Canceled", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}

func TestDiscoverRequestTargetsPort443(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		writeJSON(w, serviceRoot{})
	}))
	p := New()
	base := http.DefaultTransport
	p.client = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// Observe the original host:port, then rewrite to the test server.
			gotHost = req.URL.Host
			srvURL, _ := url.Parse(srv.URL)
			r := req.Clone(req.Context())
			r.URL.Scheme = srvURL.Scheme
			r.URL.Host = srvURL.Host
			return base.RoundTrip(r)
		}),
	}
	if _, err := p.Discover(context.Background(), []string{"192.0.2.99"}, nil); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	_, port, err := net.SplitHostPort(gotHost)
	if err != nil || port != fmt.Sprintf("%d", defaultPort) {
		t.Errorf("requested host = %q, want port %d", gotHost, defaultPort)
	}
}

// tlsBMC starts a TLS Redfish endpoint with the httptest certificate (issued
// for 127.0.0.1) and returns the plugin port and the certificate.
func tlsBMC(t *testing.T, sawAuth *bool) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok && sawAuth != nil {
			*sawAuth = true
		}
		writeJSON(w, serviceRoot{RedfishVersion: "1.13.0", UUID: "tls-bmc"})
	}))
	t.Cleanup(srv.Close)
	_, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return srv, port
}

func loopback(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// COL-02: an untrusted certificate is rejected by default and no credentials
// are sent to the endpoint.
func TestCollectRejectsUntrustedCertificateByDefault(t *testing.T) {
	sawAuth := false
	_, port := tlsBMC(t, &sawAuth)
	p := New()
	p.port = port

	_, err := p.Collect(context.Background(), "127.0.0.1", map[string]string{"username": "admin", "password": "secret"})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate verification error, got %v", err)
	}
	if sawAuth {
		t.Fatal("credentials must not be sent to an unverified endpoint")
	}
	results, _ := p.Discover(context.Background(), []string{"127.0.0.1"}, map[string]string{"username": "admin"})
	if len(results) != 0 || sawAuth {
		t.Fatalf("discovery must not accept an unverified endpoint: %v (auth sent: %v)", results, sawAuth)
	}
}

func TestCollectAcceptsScopedCAAndPin(t *testing.T) {
	srv, port := tlsBMC(t, nil)
	cert := srv.Certificate()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	wrong := sha256.Sum256([]byte("other key"))

	cases := []struct {
		name  string
		scope TLSScope
		ok    bool
	}{
		{"CA bundle of the scope", TLSScope{Network: loopback(t, "127.0.0.0/8"), RootCAs: pool}, true},
		{"matching SPKI pin", TLSScope{Network: loopback(t, "127.0.0.1/32"), PinsSHA256: [][]byte{pin[:]}}, true},
		{"CA plus matching pin", TLSScope{Network: loopback(t, "127.0.0.1/32"), RootCAs: pool, PinsSHA256: [][]byte{pin[:]}}, true},
		{"wrong pin", TLSScope{Network: loopback(t, "127.0.0.1/32"), PinsSHA256: [][]byte{wrong[:]}}, false},
		{"scope of another network", TLSScope{Network: loopback(t, "10.0.0.0/8"), RootCAs: pool}, false},
		{"scope without CA or pin is ignored", TLSScope{Network: loopback(t, "127.0.0.0/8")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(WithTLSScopes(tc.scope))
			p.port = port
			res, err := p.Collect(context.Background(), "127.0.0.1", nil)
			if tc.ok && (err != nil || res.Attributes["bmcUUID"] != "tls-bmc") {
				t.Fatalf("expected verified connection, got %v, %v", res, err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected the certificate to be rejected")
			}
		})
	}
}

// COL-02: credentials are selected per protocol; the SSH account never reaches
// the Redfish plugin.
func TestCredentialsForSeparatesProtocols(t *testing.T) {
	all := map[string]string{
		"ssh.username":     "root",
		"ssh.password":     "ssh-secret",
		"snmp.community":   "public",
		"redfish.username": "bmc-admin",
		"redfish.password": "bmc-secret",
		"username":         "legacy-unnamespaced",
	}
	redfishCreds := plugins.CredentialsFor("redfish", all)
	if len(redfishCreds) != 2 || redfishCreds["username"] != "bmc-admin" || redfishCreds["password"] != "bmc-secret" {
		t.Fatalf("redfish credentials = %v", redfishCreds)
	}
	onlySSH := plugins.CredentialsFor("redfish", map[string]string{"ssh.username": "root", "ssh.password": "x"})
	if len(onlySSH) != 0 {
		t.Fatalf("SSH credentials leaked to redfish: %v", onlySSH)
	}
	if got := plugins.CredentialsFor("power", all); len(got) != 1 || got["community"] != "public" {
		t.Fatalf("power must use the SNMP community only, got %v", got)
	}
	if got := plugins.CredentialsFor("ssh", all); got["username"] != "root" || got["community"] != "" {
		t.Fatalf("ssh credentials = %v", got)
	}
}
