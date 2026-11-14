package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe/rate"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// startVendorAgent serves pdus with the sysObjectID of a vendor, so that
// the shipped profile with its monitor.bgp section is selected.
func startVendorAgent(t *testing.T, sysObjectID string, pdus ...gosnmp.SnmpPDU) *fakesnmp.Agent {
	t.Helper()
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "pe-1",
		SysDescr: "Reticora BGP fake", SysObjectID: sysObjectID}, pdus...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Serve() }()
	t.Cleanup(func() {
		_ = agent.Close()
		<-done
	})
	return agent
}

func bgpClock(t *testing.T) func(time.Duration) {
	t.Helper()
	at := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	bgpNow = func() time.Time { return at }
	t.Cleanup(func() { bgpNow = time.Now })
	return func(d time.Duration) { at = at.Add(d) }
}

func runStore(t *testing.T, store *rate.Store, s probewire.ProbeSpec) probewire.Result { //nolint:gocritic // a copy per check
	t.Helper()
	r := probe.Run(rate.WithStore(context.Background(), store), &s, v2Creds)
	if err := r.Validate(); err != nil {
		t.Fatalf("result invalid: %v (%+v)", err, r)
	}
	return r
}

func bgpKey(name, peer, as string) string {
	return probewire.ValueKey(name, map[string]string{LabelPeer: peer, LabelRemoteAS: as})
}

func newBGPStore() *rate.Store { return rate.NewStore(BGPMaxPeers*8+16, time.Minute) }

// TestBGPEstablished covers AK1, AK4 and AK5: two expected peers of
// BGP4-MIB established; state, established time and update counter per
// peer, no prefix counters from the core.
func TestBGPEstablished(t *testing.T) {
	bgpClock(t)
	agent := startAgent(t, walkFile(t, "bgp4-mib.walk")...)
	r := runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1","remote_as":65001},{"address":"192.0.2.2"}]}`))
	want(t, r, probewire.StatusOK, "")
	for k, v := range map[string]float64{
		bgpKey(ValueBGPPeerState, "192.0.2.1", "65001"):       6,
		bgpKey(ValueBGPEstablishedSecs, "192.0.2.1", "65001"): 86400,
		bgpKey(ValueBGPUpdatesInTotal, "192.0.2.2", "65002"):  800,
		bgpKey(ValueBGPUpdatesOutTotal, "192.0.2.1", "65001"): 1300,
		bgpKey(ValueBGPUpdatesOutTotal, "192.0.2.2", "65002"): 900,
		bgpKey(ValueBGPPeerState, "192.0.2.9", "65009"):       1,
		ValueBGPPeers:            3,
		ValueBGPPeersEstablished: 2,
	} {
		if got, ok := r.Values[k]; !ok || got != v {
			t.Errorf("%s = %v (%v), want %v", k, got, ok, v)
		}
	}
	for k := range r.Values {
		if n, _, _ := probewire.ParseValueKey(k); n == ValueBGPPrefixesReceived {
			t.Errorf("prefix value %s from the core", k)
		}
	}
	// all_peers: every peer with admin status start is expected.
	want(t, runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"all_peers":true}`)), probewire.StatusOK, "")
}

// TestBGPIdleIsDown covers AK4: an expected peer that falls back to idle
// (Agent.Set on bgpPeerState) is fail/check_failed with the label peer.
func TestBGPIdleIsDown(t *testing.T) {
	bgpClock(t)
	agent := startAgent(t, walkFile(t, "bgp4-mib.walk")...)
	s := spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1"},{"address":"192.0.2.2"}]}`)
	store := newBGPStore()
	want(t, runStore(t, store, s), probewire.StatusOK, "")
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.15.3.1.2.192.0.2.2", Type: gosnmp.Integer, Value: 1})
	r := runStore(t, store, s)
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelPeer] != "192.0.2.2" {
		t.Fatalf("labels = %v", r.Labels)
	}
	// all_peers catches it as well.
	want(t, runStore(t, store, spec(TypeBGP, agent, `{"all_peers":true}`)), probewire.StatusFail, probewire.WarnCheckFailed)
}

// TestBGPMissingExpectedPeerDown covers AK4: an expected peer the table
// does not have is fail/check_failed.
func TestBGPMissingExpectedPeerDown(t *testing.T) {
	bgpClock(t)
	agent := startAgent(t, walkFile(t, "bgp4-mib.walk")...)
	r := runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1"},{"address":"203.0.113.7"}]}`))
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelPeer] != "203.0.113.7" {
		t.Fatalf("labels = %v", r.Labels)
	}
}

// TestBGPSessionResetDegraded covers AK4: a smaller FsmEstablishedTime or
// a raised FsmEstablishedTransitions than in the previous run (previous
// values in the store of WP-994) is ok with check_failed; a different
// remote_as as well.
func TestBGPSessionResetDegraded(t *testing.T) {
	step := bgpClock(t)
	agent := startAgent(t, walkFile(t, "bgp4-mib.walk")...)
	store := newBGPStore()
	s := spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1"},{"address":"192.0.2.2"}]}`)
	want(t, runStore(t, store, s), probewire.StatusOK, "")
	step(time.Minute)
	want(t, runStore(t, store, s), probewire.StatusOK, "")
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.15.3.1.16.192.0.2.1", Type: gosnmp.Gauge32, Value: uint32(30)})
	step(time.Minute)
	r := runStore(t, store, s)
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Labels[LabelPeer] != "192.0.2.1" || r.Values[ValueBGPSessionResets] != 1 {
		t.Fatalf("labels %v resets %v", r.Labels, r.Values[ValueBGPSessionResets])
	}
	// The next run without a further reset is ok again.
	step(time.Minute)
	want(t, runStore(t, store, s), probewire.StatusOK, "")
	// A raised transition count is a reset too.
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.15.3.1.15.192.0.2.2", Type: gosnmp.Counter32, Value: uint32(4)})
	step(time.Minute)
	r = runStore(t, store, s)
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Labels[LabelPeer] != "192.0.2.2" {
		t.Fatalf("labels %v", r.Labels)
	}
	// A different remote AS is degraded.
	r = runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1","remote_as":65100}]}`))
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	// A 4-byte AS: the 2-byte bgpPeerRemoteAs reports AS_TRANS (23456),
	// which is no difference; another 2-byte value still is.
	agent.Set(gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.15.3.1.9.192.0.2.1", Type: gosnmp.Integer, Value: 23456})
	want(t, runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1","remote_as":4200000001}]}`)),
		probewire.StatusOK, "")
	want(t, runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.1","remote_as":65001}]}`)),
		probewire.StatusOK, probewire.WarnCheckFailed)
}

// TestBGPMinPrefixesDegraded covers AK4: fewer accepted prefixes than
// min_prefixes (profile with prefix counters) is ok with check_failed.
func TestBGPMinPrefixesDegraded(t *testing.T) {
	bgpClock(t)
	agent := startVendorAgent(t, ".1.3.6.1.4.1.9.1.2494", walkFile(t, "cisco-bgp4v2.walk")...)
	want(t, runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"198.51.100.1","min_prefixes":100}]}`)),
		probewire.StatusOK, "")
	r := runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"2001:db8::1","min_prefixes":100}]}`))
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
	if r.Labels[LabelPeer] != "2001:db8::1" {
		t.Fatalf("labels = %v", r.Labels)
	}
}

// TestBGPAdminStoppedIgnored covers AK4: an admin-stopped peer goes into no
// rating, also when listed, and all_peers does not expect it.
func TestBGPAdminStoppedIgnored(t *testing.T) {
	bgpClock(t)
	agent := startAgent(t, walkFile(t, "bgp4-mib.walk")...)
	want(t, runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"peers":[{"address":"192.0.2.9"},{"address":"192.0.2.1"}]}`)),
		probewire.StatusOK, "")
	want(t, runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"all_peers":true}`)), probewire.StatusOK, "")
}

// TestBGPIPv6PeerViaProfile covers AK2: the profiles cisco, juniper and
// arista (selected by sysObjectID) read their BGP4-V2 tables with an IPv6
// peer and prefix counters per address family.
func TestBGPIPv6PeerViaProfile(t *testing.T) {
	for _, tc := range []struct {
		name, oid, walk string
	}{
		{"cisco", ".1.3.6.1.4.1.9.1.2494", "cisco-bgp4v2.walk"},
		{"juniper", ".1.3.6.1.4.1.2636.1.1.1.2.25", "juniper-bgp4v2.walk"},
		{"arista", ".1.3.6.1.4.1.30065.1.3011", "arista-bgp4v2.walk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := bgpClock(t)
			agent := startVendorAgent(t, tc.oid, walkFile(t, tc.walk)...)
			store := newBGPStore()
			s := spec(TypeBGP, agent, `{"peers":[{"address":"198.51.100.1","remote_as":64512},{"address":"2001:db8::1","remote_as":64513}]}`)
			r := runStore(t, store, s)
			want(t, r, probewire.StatusOK, "")
			for k, v := range map[string]float64{
				bgpKey(ValueBGPPeerState, "2001:db8::1", "64513"):                                                                                    6,
				bgpKey(ValueBGPEstablishedSecs, "2001:db8::1", "64513"):                                                                              1800,
				bgpKey(ValueBGPUpdatesInTotal, "198.51.100.1", "64512"):                                                                              500,
				probewire.ValueKey(ValueBGPPrefixesReceived, map[string]string{LabelPeer: "2001:db8::1", LabelRemoteAS: "64513", LabelAFI: "ipv6"}):  80,
				probewire.ValueKey(ValueBGPPrefixesReceived, map[string]string{LabelPeer: "198.51.100.1", LabelRemoteAS: "64512", LabelAFI: "ipv4"}): 120,
			} {
				if got, ok := r.Values[k]; !ok || got != v {
					t.Errorf("%s = %v (%v), want %v (values %v)", k, got, ok, v, r.Values)
				}
			}
			// A reset of the IPv6 peer through the profile's table.
			for _, v := range agent.Variables() {
				if n, ok := v.Value.(uint32); ok && n == 1800 {
					agent.Set(gosnmp.SnmpPDU{Name: v.Name, Type: v.Type, Value: uint32(5)})
				}
			}
			step(time.Minute)
			r = runStore(t, store, s)
			want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
			if r.Labels[LabelPeer] != "2001:db8::1" {
				t.Fatalf("labels = %v", r.Labels)
			}
		})
	}
}

// juniperVRFPeers are two rows of jnxBgpM2PeerTable with the same remote
// address 198.51.100.1 in the routing instances 0 and 5 (peer indexes 1
// and 3), both established, with their own transitions and established
// times.
func juniperVRFPeers(stateVRF int) []gosnmp.SnmpPDU {
	const peerTable = ".1.3.6.1.4.1.2636.5.1.1.2.1.1.1."
	var pdus []gosnmp.SnmpPDU
	for _, p := range []struct {
		instance, index, state, trans, est int
	}{{0, 1, 6, 3, 7200}, {5, 3, stateVRF, 5, 600}} {
		idx := fmt.Sprintf("%d.1.4.198.51.100.254.1.4.198.51.100.1", p.instance)
		pdus = append(pdus,
			gosnmp.SnmpPDU{Name: peerTable + "2." + idx, Type: gosnmp.Integer, Value: p.state},
			gosnmp.SnmpPDU{Name: peerTable + "3." + idx, Type: gosnmp.Integer, Value: 2},
			gosnmp.SnmpPDU{Name: peerTable + "10." + idx, Type: gosnmp.Integer, Value: 1},
			gosnmp.SnmpPDU{Name: peerTable + "11." + idx, Type: gosnmp.OctetString, Value: []byte{198, 51, 100, 1}},
			gosnmp.SnmpPDU{Name: peerTable + "13." + idx, Type: gosnmp.Gauge32, Value: uint32(64512)},
			gosnmp.SnmpPDU{Name: peerTable + "14." + idx, Type: gosnmp.Gauge32, Value: uint32(p.index)},
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.4.1.2636.5.1.1.2.6.1.1.5.%d", p.index), Type: gosnmp.Counter32, Value: uint32(p.trans)},
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.4.1.2636.5.1.1.2.4.1.1.1.%d", p.index), Type: gosnmp.Gauge32, Value: uint32(p.est)},
		)
	}
	return pdus
}

// TestBGPSameAddressInTwoVRFs covers AK4 and AK5 with one peer address in
// two routing instances: the previous values are kept per row, so two
// runs report no reset; the values carry the label instance; a VRF row
// that is not established makes the expected peer fail, whatever the walk
// order.
func TestBGPSameAddressInTwoVRFs(t *testing.T) {
	step := bgpClock(t)
	agent := startVendorAgent(t, ".1.3.6.1.4.1.2636.1.1.1.2.25", juniperVRFPeers(6)...)
	store := newBGPStore()
	s := spec(TypeBGP, agent, `{"peers":[{"address":"198.51.100.1"}]}`)
	for range 3 {
		r := runStore(t, store, s)
		want(t, r, probewire.StatusOK, "")
		if r.Values[ValueBGPSessionResets] != 0 {
			t.Fatalf("false reset across VRFs: %v", r.Values)
		}
		step(time.Minute)
	}
	r := runStore(t, store, s)
	for _, inst := range []string{"0", "5"} {
		k := probewire.ValueKey(ValueBGPPeerState, map[string]string{LabelPeer: "198.51.100.1", LabelRemoteAS: "64512", LabelInstance: inst})
		if r.Values[k] != 6 {
			t.Errorf("%s = %v (values %v)", k, r.Values[k], r.Values)
		}
	}
	agent.Apply(juniperVRFPeers(1))
	r = runStore(t, store, s)
	want(t, r, probewire.StatusFail, probewire.WarnCheckFailed)
	if r.Labels[LabelPeer] != "198.51.100.1" {
		t.Fatalf("labels = %v", r.Labels)
	}
}

// TestBGPJoinIndependentOfMaxPeers covers AK3 and AK5 on the Juniper
// profile: with max_peers smaller than the peer count the counter and
// event time tables (ordered by jnxBgpM2PeerIndex, not by the peer table)
// are still joined for the peers read, and more than two address families
// per peer keep their prefix counters.
func TestBGPJoinIndependentOfMaxPeers(t *testing.T) {
	bgpClock(t)
	const peerTable = ".1.3.6.1.4.1.2636.5.1.1.2.1.1.1."
	var pdus []gosnmp.SnmpPDU
	for i := 1; i <= 4; i++ {
		// The peer table sorts 198.51.100.1 … .4 ascending, the peer
		// indexes run the other way (4 … 1).
		idx := fmt.Sprintf("0.1.4.198.51.100.254.1.4.198.51.100.%d", i)
		pi := 5 - i
		pdus = append(pdus,
			gosnmp.SnmpPDU{Name: peerTable + "2." + idx, Type: gosnmp.Integer, Value: 6},
			gosnmp.SnmpPDU{Name: peerTable + "3." + idx, Type: gosnmp.Integer, Value: 2},
			gosnmp.SnmpPDU{Name: peerTable + "13." + idx, Type: gosnmp.Gauge32, Value: uint32(64512)},
			gosnmp.SnmpPDU{Name: peerTable + "14." + idx, Type: gosnmp.Gauge32, Value: uint32(pi)},
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.4.1.2636.5.1.1.2.6.1.1.1.%d", pi), Type: gosnmp.Counter32, Value: uint32(100 * i)},
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.4.1.2636.5.1.1.2.6.1.1.5.%d", pi), Type: gosnmp.Counter32, Value: uint32(1)},
			gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.4.1.2636.5.1.1.2.4.1.1.1.%d", pi), Type: gosnmp.Gauge32, Value: uint32(60 * i)},
		)
		for _, af := range []string{"1.1", "1.128", "2.1", "2.128", "25.70"} {
			pdus = append(pdus, gosnmp.SnmpPDU{Name: fmt.Sprintf(".1.3.6.1.4.1.2636.5.1.1.2.6.2.1.4.%d.%s", pi, af),
				Type: gosnmp.Gauge32, Value: uint32(10)})
		}
	}
	agent := startVendorAgent(t, ".1.3.6.1.4.1.2636.1.1.1.2.25", pdus...)
	r := runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"max_peers":2,"peers":[{"address":"198.51.100.2","min_prefixes":50}]}`))
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueBGPPeers] != 2 || r.Values[ValueBGPTruncated] != 1 {
		t.Fatalf("peers %v truncated %v", r.Values[ValueBGPPeers], r.Values[ValueBGPTruncated])
	}
	for _, i := range []string{"1", "2"} {
		k := bgpKey(ValueBGPEstablishedSecs, "198.51.100."+i, "64512")
		if _, ok := r.Values[k]; !ok {
			t.Errorf("no established time of peer %s (values %v)", i, r.Values)
		}
	}
	n := 0
	for k := range r.Values {
		if name, labels, _ := probewire.ParseValueKey(k); name == ValueBGPPrefixesReceived && labels[LabelPeer] == "198.51.100.2" {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("%d address families of 198.51.100.2, want 5", n)
	}
	// 50 prefixes over five families meet min_prefixes 50; 51 do not.
	r = runStore(t, newBGPStore(), spec(TypeBGP, agent, `{"max_peers":2,"peers":[{"address":"198.51.100.2","min_prefixes":51}]}`))
	want(t, r, probewire.StatusOK, probewire.WarnCheckFailed)
}

// TestBGPTruncated covers AK5: 2001 peers truncate the walk at 2000
// (probe_bgp_truncated = 1); an expected peer behind the cut is no
// statement, only peers behind it give error/response_too_large.
func TestBGPTruncated(t *testing.T) {
	bgpClock(t)
	var pdus []gosnmp.SnmpPDU
	for i := range BGPMaxPeers + 1 {
		addr := fmt.Sprintf("10.%d.%d.1", i/256, i%256)
		pdus = append(pdus,
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.15.3.1.2." + addr, Type: gosnmp.Integer, Value: 6},
			gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.15.3.1.3." + addr, Type: gosnmp.Integer, Value: 2})
	}
	agent := startAgent(t, pdus...)
	last := fmt.Sprintf("10.%d.%d.1", BGPMaxPeers/256, BGPMaxPeers%256)
	s := spec(TypeBGP, agent, `{"peers":[{"address":"10.0.0.1"},{"address":"`+last+`"}]}`)
	s.TimeoutMS = 20_000
	r := runStore(t, newBGPStore(), s)
	want(t, r, probewire.StatusOK, "")
	if r.Values[ValueBGPTruncated] != 1 || r.Values[ValueBGPPeers] != BGPMaxPeers || r.Values[ValueBGPPeersUnseen] != 1 {
		t.Fatalf("truncated %v peers %v unseen %v", r.Values[ValueBGPTruncated], r.Values[ValueBGPPeers], r.Values[ValueBGPPeersUnseen])
	}
	s.Params = json.RawMessage(`{"peers":[{"address":"` + last + `"}]}`)
	want(t, runStore(t, newBGPStore(), s), probewire.StatusError, probewire.ReasonResponseTooLarge)
	// A smaller max_peers cuts earlier.
	s.Params = json.RawMessage(`{"max_peers":10,"peers":[{"address":"10.0.0.1"}]}`)
	if r = runStore(t, newBGPStore(), s); r.Values[ValueBGPPeers] != 10 || r.Values[ValueBGPTruncated] != 1 {
		t.Fatalf("max_peers 10: %v peers", r.Values[ValueBGPPeers])
	}
}

// TestBGPLimits covers AK3 and AK6: max_peers 2001, a bad or duplicate
// peer address, interval 59 s and timeout 31 s are field errors; the check
// is not executed (error/config).
func TestBGPLimits(t *testing.T) {
	typ, ok := probewire.Lookup(TypeBGP)
	if !ok || !typ.Available() || typ.MinIntervalS != 60 || typ.MaxTimeoutS != 30 || typ.MaxRows != BGPMaxPeers ||
		len(typ.CredentialNamespaces) != 1 || typ.CredentialNamespaces[0] != "snmp" {
		t.Fatalf("contract entry = %+v", typ)
	}
	base := probewire.ProbeSpec{ID: "p", Type: TypeBGP, Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 10_000, DownCount: 3, UpCount: 1}
	for name, tc := range map[string]struct {
		params string
		field  string
		mutate func(*probewire.ProbeSpec)
	}{
		"max_peers 2001": {params: `{"max_peers":2001}`, field: "params.max_peers"},
		"bad address":    {params: `{"peers":[{"address":"peer-1"}]}`, field: "params.peers[0].address"},
		"duplicate":      {params: `{"peers":[{"address":"192.0.2.1"},{"address":"192.0.2.1"}]}`, field: "params.peers[1].address"},
		"remote_as < 0":  {params: `{"peers":[{"address":"192.0.2.1","remote_as":-1}]}`, field: "params.peers[0].remote_as"},
		"interval 59 s":  {field: "interval_s", mutate: func(s *probewire.ProbeSpec) { s.IntervalMS = 59_000 }},
		"timeout 31 s": {field: "timeout_s", mutate: func(s *probewire.ProbeSpec) {
			s.IntervalMS, s.TimeoutMS = 300_000, 31_000
		}},
	} {
		s := base
		if tc.params != "" {
			s.Params = json.RawMessage(tc.params)
		}
		if tc.mutate != nil {
			tc.mutate(&s)
		}
		errs := probewire.Validate(&s)
		found := false
		for _, e := range errs {
			found = found || e.Field == tc.field
		}
		if !found {
			t.Errorf("%s: errs = %v, want field %s", name, errs, tc.field)
			continue
		}
		if r := probe.Run(context.Background(), &s, v2Creds); r.Status != probewire.StatusError || r.Code != probewire.ReasonConfig {
			t.Errorf("%s: result %s/%s, want error/config", name, r.Status, r.Code)
		}
	}
	edge := base
	edge.Params = json.RawMessage(`{"max_peers":2000,"all_peers":true,"peers":[{"address":"2001:db8::1","remote_as":4294967295,"min_prefixes":0}]}`)
	if errs := probewire.Validate(&edge); len(errs) > 0 {
		t.Fatalf("edges rejected: %v", errs)
	}
}
