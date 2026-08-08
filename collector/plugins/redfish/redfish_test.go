package redfish

import (
	"context"
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
