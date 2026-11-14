package probe

import (
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The capability report of the collector (PRT-18, WP-966).

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// fixedICMP answers the ICMP socket of a family with cap (raw,
// unprivileged or none).
func fixedICMP(v4, v6 string) ListenFunc {
	return func(family int) (PacketConn, bool, error) {
		c := v4
		if family == 6 {
			c = v6
		}
		switch c {
		case probewire.CapabilityUnprivileged:
			return newFakeICMP(family, true), true, nil
		case probewire.CapabilityRaw:
			return newFakeICMP(family, false), false, nil
		}
		return nil, false, ErrICMPUnavailable
	}
}

// TestCapabilitiesWithoutICMP covers AK3: without an ICMP socket of either
// family Require("icmp") refuses with ErrICMPUnavailable; its result is
// error with code icmp_unavailable and the state machine makes the probe
// unknown, never down.
func TestCapabilitiesWithoutICMP(t *testing.T) {
	d := CapabilityDetector{ListenICMP: fixedICMP(probewire.CapabilityNone, probewire.CapabilityNone),
		ListenUDP: func(int) (io.Closer, error) { return nopCloser{}, nil },
		TimeSync:  func() string { return probewire.TimeUnknown }}
	r := d.Detect()
	if r.ICMP4 != probewire.CapabilityNone || r.ICMP6 != probewire.CapabilityNone {
		t.Fatalf("icmp4 %q, icmp6 %q, want none and none", r.ICMP4, r.ICMP6)
	}
	var caps Capabilities
	if err := caps.Require(TypeICMP); err != nil {
		t.Fatalf("before the first detection Require = %v, want everything allowed", err)
	}
	caps.Set(&r)
	err := caps.Require(TypeICMP)
	if !errors.Is(err, ErrICMPUnavailable) {
		t.Fatalf("Require(icmp) = %v, want ErrICMPUnavailable", err)
	}
	if tcpErr := caps.Require("tcp"); tcpErr != nil {
		t.Fatalf("Require(tcp) = %v, want nil (needs no ICMP)", tcpErr)
	}
	res := CapabilityResult(err)
	if res.Status != probewire.StatusError || res.Code != probewire.ReasonICMPUnavailable {
		t.Fatalf("result %s/%s, want error/icmp_unavailable", res.Status, res.Code)
	}
	sm := newStateMachine(t)
	spec := probewire.ProbeSpec{ID: "p-caps", Type: TypeICMP, Target: "192.0.2.1", IntervalMS: 30_000, TimeoutMS: 10_000,
		DownCount: 1, UpCount: 1, Epoch: 1}
	for range 3 {
		res.ProbeID, res.Epoch, res.ObservedAt = spec.ID, spec.Epoch, time.Now().UTC()
		_, change, err := sm.Observe(&spec, res)
		if err != nil {
			t.Fatal(err)
		}
		if change != nil && change.To == probewire.StateDown {
			t.Fatalf("missing ICMP made the probe down: %+v", change)
		}
	}
	// One family is enough for the type; the family of the target is
	// decided by the executor (icmp_unavailable for the missing one).
	r.ICMP6 = probewire.CapabilityRaw
	caps.Set(&r)
	if err := caps.Require(TypeICMP); err != nil {
		t.Fatalf("with ICMPv6: Require(icmp) = %v", err)
	}
}

// TestCapabilitiesListsImplementedTypes covers AK1: the report lists the
// types of the executor register; a type outside it is unsupported.
func TestCapabilitiesListsImplementedTypes(t *testing.T) {
	r := DetectCapabilities()
	if want := Registered(); !slices.Equal(r.ProbeTypes, want) {
		t.Fatalf("probe types %v, want the register %v", r.ProbeTypes, want)
	}
	for _, typ := range []string{TypeICMP, "tcp", "http"} {
		if !r.Implements(typ) {
			t.Errorf("type %s missing from %v", typ, r.ProbeTypes)
		}
	}
	if r.DetectedAt.IsZero() || r.DetectedAt.Location() != time.UTC {
		t.Errorf("detected_at %v, want a UTC time", r.DetectedAt)
	}
	for _, v := range []string{r.ICMP4, r.ICMP6} {
		if v != probewire.CapabilityNone && v != probewire.CapabilityRaw && v != probewire.CapabilityUnprivileged {
			t.Errorf("icmp capability %q", v)
		}
	}
	if r.TimeSync != probewire.TimeSynchronized && r.TimeSync != probewire.TimeUnsynchronized && r.TimeSync != probewire.TimeUnknown {
		t.Errorf("time_sync %q", r.TimeSync)
	}
	var caps Capabilities
	caps.Set(&r)
	if err := caps.Require("snmp_status"); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("Require(snmp_status) = %v, want ErrUnsupportedType", err)
	}
	if res := CapabilityResult(caps.Require("snmp_status")); res.Status != probewire.StatusError || res.Code != probewire.ReasonUnsupported {
		t.Fatalf("result %s/%s, want error/unsupported", res.Status, res.Code)
	}
}

// TestCapabilitiesDetectBusyUDPPort covers AK1: a UDP port that cannot be
// bound (here: taken by another socket) is reported as not bindable, and a
// type that needs the privileged ports is unsupported.
func TestCapabilitiesDetectBusyUDPPort(t *testing.T) {
	busy, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP socket: %v", err)
	}
	defer func() { _ = busy.Close() }()
	_, portText, _ := net.SplitHostPort(busy.LocalAddr().String())
	taken, _ := strconv.Atoi(portText)
	// Ports 67 and 68 stand for the free and the taken port.
	d := CapabilityDetector{ListenICMP: fixedICMP(probewire.CapabilityUnprivileged, probewire.CapabilityRaw),
		ListenUDP: func(port int) (io.Closer, error) {
			p := 0
			if port == 68 {
				p = taken
			}
			return net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(p))
		},
		Types: func() []string { return []string{TypeICMP, "dhcp"} },
	}
	r := d.Detect()
	if !r.UDP67 || r.UDP68 {
		t.Fatalf("udp_67 %v, udp_68 %v; want true (free) and false (taken)", r.UDP67, r.UDP68)
	}
	if r.ICMP4 != probewire.CapabilityUnprivileged || r.ICMP6 != probewire.CapabilityRaw {
		t.Fatalf("icmp4 %q, icmp6 %q", r.ICMP4, r.ICMP6)
	}
	var caps Capabilities
	caps.Set(&r)
	if err := caps.Require("dhcp"); !errors.Is(err, ErrNoPrivilegedUDP) {
		t.Fatalf("Require(dhcp) = %v, want ErrNoPrivilegedUDP", err)
	}
	if res := CapabilityResult(caps.Require("dhcp")); res.Code != probewire.ReasonUnsupported {
		t.Fatalf("code %s, want unsupported", res.Code)
	}
}
