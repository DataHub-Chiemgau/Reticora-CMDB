package gnmi_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakegnmi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/gnmi"
)

// Tests of the gNMI client against fakegnmi (WP-1015, PRT-16, PRT-21).

const tree = `{
  "interfaces": {
    "interface[name=eth0]": {"state": {"oper-status": "UP", "counters": {"in-octets": 42, "out-octets": 7}}},
    "interface[name=eth1]": {"state": {"oper-status": "DOWN", "counters": {"in-octets": 1}}}
  }
}`

func start(t *testing.T, cfg fakegnmi.Config) *fakegnmi.Server {
	t.Helper()
	if cfg.Tree == nil {
		cfg.Tree = []byte(tree)
	}
	s, err := fakegnmi.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func dial(t *testing.T, cfg gnmi.Config) *gnmi.Client {
	t.Helper()
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	c, err := gnmi.Dial(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func withCA(s *fakegnmi.Server, extra map[string]string) map[string]string {
	creds := map[string]string{"ca": string(s.CAPEM())}
	for k, v := range extra {
		creds[k] = v
	}
	return creds
}

func values(t *testing.T, res gnmi.Result) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, u := range res.Updates {
		if f, ok := u.Float(); ok {
			out[u.Path] = f
		}
	}
	return out
}

func TestGNMIClientCapabilitiesAndGet(t *testing.T) {
	s := start(t, fakegnmi.Config{})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)})
	ctx := context.Background()
	caps, err := c.Capabilities(ctx)
	if err != nil || caps.Encoding != gpb.Encoding_JSON_IETF || len(caps.Models) != 1 || caps.Version == "" {
		t.Fatalf("capabilities: %+v, %v", caps, err)
	}
	res, err := c.Get(ctx, []string{"/interfaces/interface[name=eth0]/state/counters"})
	if err != nil || res.Once || res.Truncated {
		t.Fatalf("get: %+v, %v", res, err)
	}
	got := values(t, res)
	if len(got) != 2 || got["/interfaces/interface[name=eth0]/state/counters/in-octets"] != 42 {
		t.Errorf("values: %v", got)
	}
	s.SetValue("/interfaces/interface[name=eth0]/state/counters/in-octets", 50)
	if res, err = c.Get(ctx, []string{"/interfaces/interface[name=eth0]/state/counters/in-octets"}); err != nil ||
		values(t, res)["/interfaces/interface[name=eth0]/state/counters/in-octets"] != 50 {
		t.Errorf("after change: %+v, %v", res, err)
	}

	// Encoding preference: JSON_IETF, otherwise PROTO, otherwise JSON.
	for _, tc := range []struct {
		offered []gpb.Encoding
		want    gpb.Encoding
	}{
		{[]gpb.Encoding{gpb.Encoding_JSON, gpb.Encoding_PROTO}, gpb.Encoding_PROTO},
		{[]gpb.Encoding{gpb.Encoding_JSON}, gpb.Encoding_JSON},
		{[]gpb.Encoding{gpb.Encoding_JSON, gpb.Encoding_PROTO, gpb.Encoding_JSON_IETF}, gpb.Encoding_JSON_IETF},
	} {
		s.SetEncodings(tc.offered...)
		cc := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)})
		res, err := cc.Get(ctx, []string{"/interfaces/interface[name=eth1]/state/counters/in-octets"})
		if err != nil || cc.Encoding() != tc.want.String() || values(t, res)["/interfaces/interface[name=eth1]/state/counters/in-octets"] != 1 {
			t.Errorf("offered %v: encoding %s, %+v, %v; want %s", tc.offered, cc.Encoding(), res, err, tc.want)
		}
	}
	s.SetEncodings(gpb.Encoding_ASCII)
	if _, err = dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)}).Capabilities(ctx); !errors.Is(err, gnmi.ErrNoEncoding) {
		t.Errorf("only ASCII: %v, want ErrNoEncoding", err)
	}
}

func TestGNMIClientSubscribeOnceFallback(t *testing.T) {
	s := start(t, fakegnmi.Config{RejectGet: true})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)})
	res, err := c.Get(context.Background(), []string{"/interfaces/interface[name=eth1]"})
	if err != nil || !res.Once {
		t.Fatalf("fallback: %+v, %v", res, err)
	}
	if got := values(t, res); got["/interfaces/interface[name=eth1]/state/counters/in-octets"] != 1 {
		t.Errorf("values over ONCE: %v", got)
	}
	// The mode once never tries Get.
	s.SetRejectGet(false)
	once := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil), Mode: gnmi.ModeOnce})
	if res, err = once.Get(context.Background(), []string{"/interfaces"}); err != nil || !res.Once || len(res.Updates) != 5 {
		t.Errorf("mode once: %d updates, once %v, %v", len(res.Updates), res.Once, err)
	}
}

func TestGNMIClientWrongCAIsUntrusted(t *testing.T) {
	s := start(t, fakegnmi.Config{})
	other := start(t, fakegnmi.Config{})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: map[string]string{"ca": string(other.CAPEM())}})
	if _, err := c.Capabilities(context.Background()); !errors.Is(err, plugins.ErrTLSUntrusted) {
		t.Errorf("foreign CA: %v, want ErrTLSUntrusted", err)
	}
	// Without a CA the system roots apply: the fake CA is not among them.
	sys := dial(t, gnmi.Config{Target: s.Addr()})
	if _, err := sys.Get(context.Background(), []string{"/interfaces"}); !errors.Is(err, plugins.ErrTLSUntrusted) {
		t.Errorf("system roots: %v, want ErrTLSUntrusted", err)
	}
	if s.Calls() != 0 {
		t.Errorf("%d calls reached the untrusted server", s.Calls())
	}
}

// TestGNMIClientUnreachableAfterUntrusted: a failed handshake is not
// remembered beyond the next connection attempt; once the device refuses
// the connection, the call is not reachable, not untrusted.
func TestGNMIClientUnreachableAfterUntrusted(t *testing.T) {
	s := start(t, fakegnmi.Config{})
	other := start(t, fakegnmi.Config{})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: map[string]string{"ca": string(other.CAPEM())}})
	if _, err := c.Capabilities(context.Background()); !errors.Is(err, plugins.ErrTLSUntrusted) {
		t.Fatalf("foreign CA: %v, want ErrTLSUntrusted", err)
	}
	s.Close()
	// gRPC reconnects after its backoff (about 1 s); until then it repeats
	// the error of the last attempt.
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, err := c.Capabilities(context.Background())
		if errors.Is(err, plugins.ErrUnreachable) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed device: %v, want ErrUnreachable", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestGNMIClientPinnedScope(t *testing.T) {
	s := start(t, fakegnmi.Config{})
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	scope := plugins.TLSScope{Network: loopback, PinsSHA256: [][]byte{s.SPKISHA256()}}
	c := dial(t, gnmi.Config{Target: s.Addr(), TLSScopes: []plugins.TLSScope{scope}})
	if _, err := c.Get(context.Background(), []string{"/interfaces/interface[name=eth0]"}); err != nil {
		t.Fatalf("pinned scope: %v", err)
	}
	wrong := plugins.TLSScope{Network: loopback, PinsSHA256: [][]byte{make([]byte, 32)}}
	if _, err := dial(t, gnmi.Config{Target: s.Addr(), TLSScopes: []plugins.TLSScope{wrong}}).Capabilities(context.Background()); !errors.Is(err, plugins.ErrTLSUntrusted) {
		t.Errorf("wrong pin: %v, want ErrTLSUntrusted", err)
	}
}

func TestGNMIClientWrongPasswordIsAuthFailed(t *testing.T) {
	s := start(t, fakegnmi.Config{Username: "probe", Password: "secret"})
	good := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, map[string]string{"username": "probe", "password": "secret"})})
	if _, err := good.Get(context.Background(), []string{"/interfaces"}); err != nil {
		t.Fatalf("right password: %v", err)
	}
	bad := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, map[string]string{"username": "probe", "password": "wrong"})})
	_, err := bad.Get(context.Background(), []string{"/interfaces"})
	if !errors.Is(err, plugins.ErrAuthFailed) {
		t.Errorf("wrong password: %v, want ErrAuthFailed", err)
	}
	if err != nil && (strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), "secret")) {
		t.Errorf("error names the password: %v", err)
	}
}

func TestGNMIClientClientCertificate(t *testing.T) {
	s := start(t, fakegnmi.Config{RequireClientCert: true})
	certPEM, keyPEM, err := s.IssueClientCert("probe")
	if err != nil {
		t.Fatal(err)
	}
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, map[string]string{"client_cert": string(certPEM), "client_key": string(keyPEM)})})
	if _, err = c.Get(context.Background(), []string{"/interfaces"}); err != nil {
		t.Fatalf("with client certificate: %v", err)
	}
	without := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)})
	if _, err = without.Get(context.Background(), []string{"/interfaces"}); err == nil {
		t.Error("server requiring a client certificate answered without one")
	}
	if _, err = gnmi.Dial(gnmi.Config{Target: s.Addr(), Credentials: map[string]string{"client_cert": string(certPEM), "client_key": "garbage"}}); err == nil {
		t.Error("invalid key pair accepted")
	}
}

func TestGNMIClientRejectsPlaintextServer(t *testing.T) {
	s := start(t, fakegnmi.Config{Plaintext: true})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: map[string]string{"username": "probe", "password": "secret"}})
	_, err := c.Get(context.Background(), []string{"/interfaces"})
	if err == nil {
		t.Fatal("plaintext server answered")
	}
	if !errors.Is(err, gnmi.ErrTLSHandshake) {
		t.Errorf("plaintext server: %v, want ErrTLSHandshake", err)
	}
	if s.Calls() != 0 {
		t.Errorf("%d calls reached the plaintext server: no plaintext fallback allowed", s.Calls())
	}
}

func TestGNMIClientMessageLimit(t *testing.T) {
	s := start(t, fakegnmi.Config{ResponseBytes: gnmi.MaxMessageBytes + 1})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)})
	if _, err := c.Get(context.Background(), []string{"/interfaces"}); !errors.Is(err, gnmi.ErrMessageTooLarge) {
		t.Errorf("answer over 4 MiB: %v, want ErrMessageTooLarge", err)
	}
	s.SetResponseBytes(gnmi.MaxMessageBytes - 64<<10)
	if _, err := c.Get(context.Background(), []string{"/interfaces"}); err != nil {
		t.Errorf("answer below 4 MiB: %v", err)
	}
}

func TestGNMIClientPathLimit(t *testing.T) {
	s := start(t, fakegnmi.Config{})
	c := dial(t, gnmi.Config{Target: s.Addr(), Credentials: withCA(s, nil)})
	paths := make([]string, gnmi.MaxPaths+1)
	for i := range paths {
		paths[i] = "/interfaces"
	}
	if _, err := c.Get(context.Background(), paths); !errors.Is(err, gnmi.ErrTooManyPaths) {
		t.Errorf("33 paths: %v, want ErrTooManyPaths", err)
	}
	if s.Calls() != 0 {
		t.Errorf("%d calls sent for a refused request", s.Calls())
	}
	if _, err := c.Get(context.Background(), paths[:gnmi.MaxPaths]); err != nil {
		t.Errorf("32 paths: %v", err)
	}
	if _, err := c.Get(context.Background(), []string{"interfaces"}); !errors.Is(err, gnmi.ErrPath) {
		t.Errorf("relative path: %v, want ErrPath", err)
	}
}

func TestGNMIPathRoundTrip(t *testing.T) {
	for _, p := range []string{"/", "/a/b", "/interfaces/interface[name=eth0]/state", "/x/y[a=1][b=2]/z", `/acl/set[name=a\]b]/e`, `/acl/set[name=a\\]/e`, `/acl/set[name=a\\b\\\]c]/e`} {
		parsed, err := gnmi.ParsePath(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if got := gnmi.PathString(parsed); got != p {
			t.Errorf("round trip %s = %s", p, got)
		}
	}
	if parsed, err := gnmi.ParsePath(`/acl/set[name=a\\b\\\]c]/e`); err != nil || parsed.Elem[1].Key["name"] != `a\b\]c` {
		t.Errorf("escaped key value: %v, %v", parsed, err)
	}
	for _, bad := range []string{"", "a/b", "/a//b", "/a[b", "/a[=1]"} {
		if _, err := gnmi.ParsePath(bad); !errors.Is(err, gnmi.ErrPath) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}
