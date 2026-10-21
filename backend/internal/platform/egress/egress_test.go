package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// staticResolver answers every lookup of a name from a table and counts the
// lookups.
type staticResolver struct {
	mu      sync.Mutex
	answers map[string][]string
	calls   map[string]int
}

func (r *staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[host]++
	var out []netip.Addr
	for _, a := range r.answers[host] {
		out = append(out, netip.MustParseAddr(a))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no such host %s", host)
	}
	return out, nil
}

// TestAllowed covers WP-049 (SEC-08): private, loopback, link-local,
// metadata, IPv6 unique-local and mapped or NAT64-embedded internal addresses
// are blocked; with AllowPrivate only metadata stays blocked.
func TestAllowed(t *testing.T) {
	strict, onPrem := Options{}, Options{AllowPrivate: true}
	for _, c := range []struct {
		ip             string
		strict, onPrem bool
	}{
		{"93.184.216.34", true, true},
		{"2606:4700::1111", true, true},
		{"10.1.2.3", false, true},
		{"172.20.0.1", false, true},
		{"192.168.1.1", false, true},
		{"127.0.0.1", false, true},
		{"::1", false, true},
		{"169.254.169.254", false, false},
		{"100.100.100.200", false, false},
		{"fd00:ec2::254", false, false},
		{"fd12:3456::1", false, true},
		{"fe80::1", false, true},
		{"::ffff:127.0.0.1", false, true},
		{"::ffff:169.254.169.254", false, false},
		{"64:ff9b::a00:1", false, true},
		{"0.0.0.0", false, false},
		{"224.0.0.1", false, false},
		// A zone does not take an address out of its network (WP-399).
		{"fe80::1%eth0", false, true},
		{"fd00:ec2::254%1", false, false},
	} {
		ip := netip.MustParseAddr(c.ip)
		if got := strict.Allowed(ip); got != c.strict {
			t.Errorf("strict Allowed(%s) = %v, want %v", c.ip, got, c.strict)
		}
		if got := onPrem.Allowed(ip); got != c.onPrem {
			t.Errorf("on-prem Allowed(%s) = %v, want %v", c.ip, got, c.onPrem)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hooks.example.com/x":         true,
		"http://93.184.216.34:8080/hook":      true,
		"ftp://example.com/x":                 false,
		"file:///etc/passwd":                  false,
		"https://user:pw@example.com/":        false,
		"http://127.0.0.1/":                   false,
		"http://[::1]/":                       false,
		"http://169.254.169.254/latest/":      false,
		"http://localhost:8080/":              false,
		"https://db.internal/":                false,
		"https://printer.local/":              false,
		"http:///nohost":                      false,
		"http://[fd00:ec2::254]/latest/meta/": false,
		"http://[fe80::1%25eth0]/":            false,
	} {
		err := Options{}.ValidateURL(raw)
		if (err == nil) != ok {
			t.Errorf("ValidateURL(%q) = %v, want ok=%v", raw, err, ok)
		}
		if err != nil && !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("ValidateURL(%q) error %v is not ErrBlockedDestination", raw, err)
		}
	}
	if err := (Options{AllowPrivate: true}).ValidateURL("http://localhost:8080/"); err != nil {
		t.Errorf("on-prem localhost: %v", err)
	}
	if err := (Options{AllowPrivate: true}).ValidateURL("http://169.254.169.254/"); err == nil {
		t.Error("on-prem metadata endpoint accepted")
	}
}

// TestValidateHost covers WP-399 (SEC-08): the host check of destinations
// that are no URL (an organization's own SMTP server) blocks IP literals
// and names of the internal network, with AllowPrivate only metadata.
func TestValidateHost(t *testing.T) {
	for _, c := range []struct {
		host           string
		strict, onPrem bool
	}{
		{"mail.example.org", true, true},
		{"Mail.Example.org.", true, true},
		{"93.184.216.34", true, true},
		{"10.0.0.5", false, true},
		{" 127.0.0.1 ", false, true},
		{"fd12:3456::25", false, true},
		{"localhost", false, true},
		{"relay.corp.internal", false, true},
		{"169.254.169.254", false, false},
		{"fd00:ec2::254", false, false},
		{"", false, false},
	} {
		if err := (Options{}).ValidateHost(c.host); (err == nil) != c.strict || (err != nil && !errors.Is(err, ErrBlockedDestination)) {
			t.Errorf("strict ValidateHost(%q) = %v, want ok=%v", c.host, err, c.strict)
		}
		if err := (Options{AllowPrivate: true}).ValidateHost(c.host); (err == nil) != c.onPrem {
			t.Errorf("on-prem ValidateHost(%q) = %v, want ok=%v", c.host, err, c.onPrem)
		}
	}
}

// recordingDial stands in for the network: it records the addresses the
// dialer connects to and returns one end of a pipe.
type recordingDial struct {
	mu    sync.Mutex
	addrs []string
}

func (r *recordingDial) dial(_ context.Context, _, address string) (net.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs = append(r.addrs, address)
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

// TestDialerChecksTheResolvedAddress covers WP-399 (SEC-08): the dialer of
// non-HTTP destinations judges the address a name resolves to when it
// connects, so a name that passed the check when it was stored and now
// points into the internal network (DNS rebinding) is refused, also when
// only one of its addresses does; a public destination is connected, to
// the address that was checked, with one lookup.
func TestDialerChecksTheResolvedAddress(t *testing.T) {
	resolver := &staticResolver{answers: map[string][]string{
		"mail.example.test": {"93.184.216.34"},
		"rebind.test":       {"10.0.0.5"},
		"loopback.test":     {"127.0.0.1"},
		"ula.test":          {"fd12:3456::25"},
		"metadata.test":     {"169.254.169.254"},
		"mixed.test":        {"93.184.216.34", "192.168.1.10"},
		"mapped.test":       {"::ffff:10.0.0.5"},
		"v6.example.test":   {"2606:4700::1111"},
	}}
	wire := &recordingDial{}
	d := NewDialer(Options{Resolver: resolver})
	d.dial = wire.dial
	for _, target := range []string{"rebind.test:25", "loopback.test:587", "ula.test:465", "metadata.test:25",
		"mixed.test:25", "mapped.test:25", "10.0.0.5:25", "[fd00:ec2::254]:25"} {
		conn, err := d.DialContext(context.Background(), "tcp", target)
		if err == nil {
			conn.Close()
		}
		if !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("dial %s: %v, want ErrBlockedDestination", target, err)
		}
		// The error reaches the tenant: it does not answer the lookup.
		if host, _, _ := net.SplitHostPort(target); err != nil && resolver.answers[host] != nil {
			for _, a := range resolver.answers[host] {
				if strings.Contains(err.Error(), a) {
					t.Errorf("dial %s: the error names the resolved address: %v", target, err)
				}
			}
		}
	}
	if len(wire.addrs) != 0 {
		t.Fatalf("blocked destinations were connected: %v", wire.addrs)
	}
	for target, want := range map[string]string{
		"mail.example.test:587": "93.184.216.34:587",
		"v6.example.test:465":   "[2606:4700::1111]:465",
		"93.184.216.34:25":      "93.184.216.34:25",
	} {
		conn, err := d.DialContext(context.Background(), "tcp", target)
		if err != nil {
			t.Errorf("dial %s: %v", target, err)
			continue
		}
		conn.Close()
		if got := wire.addrs[len(wire.addrs)-1]; got != want {
			t.Errorf("dial %s connected to %s, want %s", target, got, want)
		}
	}
	if resolver.calls["mail.example.test"] != 1 {
		t.Errorf("lookups of mail.example.test = %d, want 1", resolver.calls["mail.example.test"])
	}
	// Rebinding: the name that was public on the last connection points
	// into the internal network now.
	resolver.mu.Lock()
	resolver.answers["mail.example.test"] = []string{"10.0.0.5"}
	resolver.mu.Unlock()
	connected := len(wire.addrs)
	if _, err := d.DialContext(context.Background(), "tcp", "mail.example.test:587"); !errors.Is(err, ErrBlockedDestination) ||
		len(wire.addrs) != connected {
		t.Errorf("dial after rebinding to 10.0.0.5: %v, want ErrBlockedDestination and no connection", err)
	}

	// On premises the internal relay is reachable; metadata stays blocked.
	onPrem := NewDialer(Options{AllowPrivate: true, Resolver: resolver})
	onPrem.dial = wire.dial
	if conn, err := onPrem.DialContext(context.Background(), "tcp", "rebind.test:2525"); err != nil {
		t.Errorf("on-prem dial of an internal relay: %v", err)
	} else {
		conn.Close()
	}
	if _, err := onPrem.DialContext(context.Background(), "tcp", "metadata.test:25"); !errors.Is(err, ErrBlockedDestination) {
		t.Errorf("on-prem dial of the metadata endpoint: %v", err)
	}
}

// TestClientBlocksInternalDestinations: a local server is unreachable for
// the strict client, whether addressed by IP or by a name resolving to it,
// and a name with one internal address among public ones is rejected.
func TestClientBlocksInternalDestinations(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	_, p, _ := strings.Cut(srv.Listener.Addr().String(), ":")

	resolver := &staticResolver{answers: map[string][]string{
		"rebind.test": {"127.0.0.1"},
		"mixed.test":  {"93.184.216.34", "127.0.0.1"},
	}}
	client := NewClient(Options{Resolver: resolver})
	for _, target := range []string{srv.URL, "http://rebind.test:" + p, "http://mixed.test:" + p} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
		}
		if !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("GET %s: %v, want ErrBlockedDestination", target, err)
		}
	}
	if hit {
		t.Error("the internal server was reached")
	}
}

// TestClientPinsTheCheckedAddress: the connection goes to the address the
// check saw (the name is resolved once per connection, never again by the
// dialer), and the Host header keeps the name.
func TestClientPinsTheCheckedAddress(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { host = r.Host }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	resolver := &staticResolver{answers: map[string][]string{"svc.test": {"127.0.0.1"}}}
	client := NewClient(Options{AllowPrivate: true, Resolver: resolver})
	resp, err := client.Get("http://svc.test:" + u.Port() + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if host != "svc.test:"+u.Port() || resolver.calls["svc.test"] != 1 {
		t.Errorf("host %q, lookups %d; want the name and one lookup", host, resolver.calls["svc.test"])
	}
}

// TestClientRedirects: at most three redirects are followed and each target
// is checked again; a redirect to a metadata endpoint is refused.
func TestClientRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/hop/"):
			var n int
			fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/hop/"), "%d", &n)
			if n > 0 {
				http.Redirect(w, r, fmt.Sprintf("%s/hop/%d", srv.URL, n-1), http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}
	}))
	defer srv.Close()
	client := NewClient(Options{AllowPrivate: true})

	resp, err := client.Get(srv.URL + "/hop/3")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("three redirects: %v", err)
	}
	resp.Body.Close()
	if resp, err = client.Get(srv.URL + "/hop/4"); err == nil {
		resp.Body.Close()
		t.Error("four redirects were followed")
	}
	if resp, err = client.Get(srv.URL + "/metadata"); err == nil {
		resp.Body.Close()
		t.Error("redirect to the metadata endpoint was followed")
	} else if !errors.Is(err, ErrBlockedDestination) {
		t.Errorf("redirect to metadata: %v, want ErrBlockedDestination", err)
	}
}
