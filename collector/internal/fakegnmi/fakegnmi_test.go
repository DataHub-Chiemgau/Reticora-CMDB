package fakegnmi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const tree = `{
  "interfaces": {
    "interface[name=eth0]": {"state": {"oper-status": "UP", "counters": {"in-octets": 42, "out-octets": 7}}},
    "interface[name=eth1]": {"state": {"oper-status": "DOWN"}}
  },
  "system": {"state": {"hostname": "pe-1"}}
}`

func dial(t *testing.T, s *Server) gpb.GNMIClient {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(s.CAPEM()) {
		t.Fatal("CA PEM")
	}
	conn, err := grpc.NewClient(s.Addr(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return gpb.NewGNMIClient(conn)
}

func path(t *testing.T, s string) *gpb.Path {
	t.Helper()
	p, err := parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func leaves(n *gpb.Notification) map[string]string {
	out := map[string]string{}
	for _, u := range n.GetUpdate() {
		v := u.GetVal()
		switch {
		case v.GetJsonIetfVal() != nil:
			out[pathString(u.GetPath())] = string(v.GetJsonIetfVal())
		case v.GetStringVal() != "":
			out[pathString(u.GetPath())] = v.GetStringVal()
		default:
			b, _ := json.Marshal(v.GetValue())
			out[pathString(u.GetPath())] = string(b)
		}
	}
	return out
}

// TestFakeGNMITreeAndUpdates: the fake serves the leaves of its JSON tree
// below the requested paths over TLS of its own CA, in JSON_IETF or PROTO,
// sees changed values at once, answers Subscribe ONCE with the values and a
// sync response, can refuse Get with Unimplemented, checks
// username/password and pads its answers to a set size (WP-1015, PRT-21).
func TestFakeGNMITreeAndUpdates(t *testing.T) {
	s, err := Start(&Config{Tree: []byte(tree)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := dial(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	caps, err := c.Capabilities(ctx, &gpb.CapabilityRequest{})
	if err != nil || len(caps.GetSupportedEncodings()) != 2 || caps.GetSupportedEncodings()[0] != gpb.Encoding_JSON_IETF {
		t.Fatalf("capabilities: %v, %v", caps, err)
	}

	get := func(enc gpb.Encoding, p string) map[string]string {
		t.Helper()
		resp, gerr := c.Get(ctx, &gpb.GetRequest{Path: []*gpb.Path{path(t, p)}, Encoding: enc})
		if gerr != nil || len(resp.GetNotification()) != 1 {
			t.Fatalf("get %s: %v, %v", p, resp, gerr)
		}
		return leaves(resp.GetNotification()[0])
	}
	got := get(gpb.Encoding_JSON_IETF, "/interfaces/interface[name=eth0]")
	if len(got) != 3 || got["/interfaces/interface[name=eth0]/state/counters/in-octets"] != "42" ||
		got["/interfaces/interface[name=eth0]/state/oper-status"] != `"UP"` {
		t.Errorf("eth0: %v", got)
	}
	if got = get(gpb.Encoding_PROTO, "/system/state/hostname"); got["/system/state/hostname"] != "pe-1" {
		t.Errorf("hostname over PROTO: %v", got)
	}
	resp, err := c.Get(ctx, &gpb.GetRequest{Path: []*gpb.Path{path(t, "/interfaces/interface[name=eth0]/state/counters/in-octets")}, Encoding: gpb.Encoding_PROTO})
	if err != nil || resp.GetNotification()[0].GetUpdate()[0].GetVal().GetIntVal() != 42 {
		t.Errorf("in-octets over PROTO: %v, %v", resp, err)
	}

	// Values change while the server runs.
	s.SetValue("/interfaces/interface[name=eth0]/state/counters/in-octets", 4242)
	if got = get(gpb.Encoding_JSON_IETF, "/interfaces/interface[name=eth0]/state/counters"); got["/interfaces/interface[name=eth0]/state/counters/in-octets"] != "4242" {
		t.Errorf("after SetValue: %v", got)
	}

	// Subscribe ONCE: the values, then the sync response.
	stream, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&gpb.SubscribeRequest{Request: &gpb.SubscribeRequest_Subscribe{Subscribe: &gpb.SubscriptionList{
		Mode: gpb.SubscriptionList_ONCE, Encoding: gpb.Encoding_JSON_IETF,
		Subscription: []*gpb.Subscription{{Path: path(t, "/interfaces/interface[name=eth1]")}},
	}}}); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || leaves(first.GetUpdate())["/interfaces/interface[name=eth1]/state/oper-status"] != `"DOWN"` {
		t.Fatalf("once update: %v, %v", first, err)
	}
	if sync, serr := stream.Recv(); serr != nil || !sync.GetSyncResponse() {
		t.Errorf("once sync: %v, %v", sync, serr)
	}

	// Get refused, encodings and answer size set at run time.
	s.SetRejectGet(true)
	if _, err = c.Get(ctx, &gpb.GetRequest{Path: []*gpb.Path{path(t, "/system")}}); status.Code(err) != codes.Unimplemented {
		t.Errorf("rejected Get: %v, want Unimplemented", err)
	}
	s.SetRejectGet(false)
	s.SetEncodings(gpb.Encoding_JSON)
	if caps, err = c.Capabilities(ctx, &gpb.CapabilityRequest{}); err != nil || len(caps.GetSupportedEncodings()) != 1 || caps.GetSupportedEncodings()[0] != gpb.Encoding_JSON {
		t.Errorf("encodings after SetEncodings: %v, %v", caps, err)
	}
	s.SetResponseBytes(1000)
	if got = get(gpb.Encoding_JSON, "/system"); len(got[PaddingPath]) != 1000 || got["/system/state/hostname"] == "" {
		t.Errorf("padding: %d bytes, %v", len(got[PaddingPath]), len(got))
	}

	// Username and password.
	secured, err := Start(&Config{Tree: []byte(tree), Username: "probe", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer secured.Close()
	if _, err = dial(t, secured).Capabilities(ctx, &gpb.CapabilityRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("without credentials: %v, want Unauthenticated", err)
	}
	if secured.Calls() != 1 {
		t.Errorf("calls %d, want 1", secured.Calls())
	}
	if !strings.HasPrefix(string(secured.CAPEM()), "-----BEGIN CERTIFICATE-----") || len(secured.SPKISHA256()) != 32 {
		t.Error("CA or pin missing")
	}
}
