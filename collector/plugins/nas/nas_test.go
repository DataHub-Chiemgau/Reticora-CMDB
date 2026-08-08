package nas

import (
	"context"
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
	if p.Name() != "nas" {
		t.Errorf("Name() = %q, want nas", p.Name())
	}
}

// newTestPlugin rewrites all plugin HTTP requests to the test server, which
// lets the fixed vendor API ports be exercised without production changes.
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

func TestVerifyVendorStatusRules(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		vendor     string
		want       bool
	}{
		{"synology endpoint 200", http.StatusOK, "synology", true},
		{"synology endpoint 403 still counts", http.StatusForbidden, "synology", true},
		{"qnap endpoint 200", http.StatusOK, "qnap", true},
		{"truenas endpoint 401 still counts", http.StatusUnauthorized, "truenas", true},
		{"server error rejects", http.StatusInternalServerError, "synology", false},
		{"unknown vendor rejected", http.StatusOK, "buffalo", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.statusCode)
			}))
			defer srv.Close()

			p := newTestPlugin(t, srv)
			if got := p.verifyVendor(context.Background(), srv.URL, c.vendor); got != c.want {
				t.Errorf("verifyVendor(status=%d, vendor=%q) = %v, want %v", c.statusCode, c.vendor, got, c.want)
			}
		})
	}
}

func TestVerifyVendorUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately unreachable
	p := newTestPlugin(t, srv)
	if p.verifyVendor(context.Background(), "http://127.0.0.1:1", "synology") {
		t.Error("verifyVendor should be false for unreachable endpoint")
	}
}

func TestDetectNASVendorSynology(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:5000")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:5000: %v", err)
	}
	defer ln.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	go http.Serve(ln, handler)

	p := newTestPlugin(t, srv)
	vendor, port := p.detectNASVendor(context.Background(), "127.0.0.1")
	if vendor != "synology" || port != 5000 {
		t.Errorf("detectNASVendor = (%q, %d), want (synology, 5000)", vendor, port)
	}
}

func TestDetectNASVendorNone(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond}
	vendor, port := p.detectNASVendor(context.Background(), "192.0.2.1")
	if vendor != "" || port != 0 {
		t.Errorf("detectNASVendor = (%q, %d), want (\"\", 0)", vendor, port)
	}
}

func TestSynologyLoginParsesSID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webapi/auth.cgi" {
			fmt.Fprint(w, `{"success":true,"data":{"sid":"abc123sid"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	sid := p.synologyLogin(context.Background(), srv.URL, "admin", "pw")
	if sid != "abc123sid" {
		t.Errorf("synologyLogin = %q, want abc123sid", sid)
	}
}

func TestSynologyLoginFailureReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":false,"error":{"code":400}}`)
	}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	if sid := p.synologyLogin(context.Background(), srv.URL, "admin", "wrong"); sid != "" {
		t.Errorf("synologyLogin = %q, want empty on failure", sid)
	}
}

func TestSynologySystemInfoParsesData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"data":{"model":"DS920+","serial":"20A1B2C3","firmware_ver":"DSM 7.2-64570","hostname":"nas01"}}`)
	}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	info := p.synologySystemInfo(context.Background(), srv.URL, "sid")
	if info == nil {
		t.Fatal("synologySystemInfo returned nil")
	}
	if info["model"] != "DS920+" || info["hostname"] != "nas01" {
		t.Errorf("unexpected info: %v", info)
	}
}

func TestCollectSynologyEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/webapi/auth.cgi":
			fmt.Fprint(w, `{"success":true,"data":{"sid":"sid-1"}}`)
		case "/webapi/entry.cgi":
			fmt.Fprint(w, `{"success":true,"data":{"model":"DS920+","serial":"20A1B2C3","firmware_ver":"DSM 7.2-64570","hostname":"nas01"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	res, err := p.collectSynology(context.Background(), "192.0.2.30", 5001, "admin", "pw")
	if err != nil {
		t.Fatalf("collectSynology: %v", err)
	}
	if res.Manufacturer != "Synology" {
		t.Errorf("Manufacturer = %q, want Synology", res.Manufacturer)
	}
	if res.Model != "DS920+" || res.Name != "DS920+" {
		t.Errorf("Model/Name = %q/%q, want DS920+", res.Model, res.Name)
	}
	if res.Serial != "20A1B2C3" {
		t.Errorf("Serial = %q", res.Serial)
	}
	if res.Firmware != "DSM 7.2-64570" {
		t.Errorf("Firmware = %q", res.Firmware)
	}
	if res.Attributes["dsmVersion"] != "DSM 7.2-64570" {
		t.Errorf("dsmVersion = %v", res.Attributes["dsmVersion"])
	}
}

func TestCollectSynologyWithoutCredentials(t *testing.T) {
	p := New()
	res, err := p.collectSynology(context.Background(), "192.0.2.31", 5000, "", "")
	if err != nil {
		t.Fatalf("collectSynology: %v", err)
	}
	if res.Model != "" || res.Serial != "" {
		t.Errorf("expected unauthenticated result without hardware fields, got %+v", res)
	}
	if res.CIType != "nas" || res.Attributes["vendor"] != "synology" {
		t.Errorf("unexpected base result: %+v", res)
	}
}

func TestCollectTrueNASEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2.0/system/info" {
			fmt.Fprint(w, `{"hostname":"truenas01","version":"TrueNAS-SCALE-23.10.1","system_product":"M50","system_serial":"TRU123","system_manufacturer":"iXsystems"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := newTestPlugin(t, srv)
	res, err := p.collectTrueNAS(context.Background(), "192.0.2.32", 443, "root", "pw")
	if err != nil {
		t.Fatalf("collectTrueNAS: %v", err)
	}
	if res.Name != "truenas01" {
		t.Errorf("Name = %q, want truenas01", res.Name)
	}
	if res.Firmware != "TrueNAS-SCALE-23.10.1" {
		t.Errorf("Firmware = %q", res.Firmware)
	}
	if res.Model != "M50" || res.Serial != "TRU123" {
		t.Errorf("Model/Serial = %q/%q", res.Model, res.Serial)
	}
	if res.Manufacturer != "iXsystems" {
		t.Errorf("Manufacturer = %q, want iXsystems", res.Manufacturer)
	}
}

func TestCollectQNAPBaseResult(t *testing.T) {
	p := New()
	res, err := p.collectQNAP(context.Background(), "192.0.2.33", 8080, "", "")
	if err != nil {
		t.Fatalf("collectQNAP: %v", err)
	}
	if res.Manufacturer != "QNAP" {
		t.Errorf("Manufacturer = %q, want QNAP", res.Manufacturer)
	}
	if res.Attributes["vendor"] != "qnap" || res.Attributes["apiPort"] != 8080 {
		t.Errorf("unexpected attributes: %v", res.Attributes)
	}
}

func TestCollectDetectsVendor(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:5000")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:5000: %v", err)
	}
	defer ln.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webapi/query.cgi" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	go http.Serve(ln, handler)

	p := newTestPlugin(t, srv)
	res, err := p.Collect(context.Background(), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Attributes["vendor"] != "synology" {
		t.Errorf("vendor = %v, want synology", res.Attributes["vendor"])
	}
}

func TestCollectNoVendorDetected(t *testing.T) {
	p := &Plugin{Timeout: 200 * time.Millisecond}
	_, err := p.Collect(context.Background(), "192.0.2.1", nil)
	if err == nil || !strings.Contains(err.Error(), "no NAS vendor detected") {
		t.Errorf("expected vendor detection error, got %v", err)
	}
}

func TestDiscoverFindsNAS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:5000")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:5000: %v", err)
	}
	defer ln.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	go http.Serve(ln, handler)

	p := newTestPlugin(t, srv)
	results, err := p.Discover(context.Background(), []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.CIType != "nas" {
		t.Errorf("CIType = %q, want nas", r.CIType)
	}
	if r.Attributes["vendor"] != "synology" {
		t.Errorf("vendor = %v, want synology", r.Attributes["vendor"])
	}
	if r.Attributes["nasDevice"] != true {
		t.Errorf("nasDevice = %v, want true", r.Attributes["nasDevice"])
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
