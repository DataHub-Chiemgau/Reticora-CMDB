package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Capability report of the collector (PRT-18, WP-966). DetectCapabilities
// finds at the start and every CapabilityRefresh what the collector can
// execute: the ICMP socket per address family (unprivileged, raw or none:
// the order of ListenICMP, the same sockets the multiplexer opens), whether
// it may bind the privileged UDP ports 67 and 68 (DHCP, WP-1012), whether
// the system clock is synchronized (Linux: adjtimex, flag STA_UNSYNC;
// elsewhere unknown) and the probe types of the executor register
// (Registered). The report goes with every heartbeat and alive message;
// the server stores it and assigns a probe only to a member that can
// execute it.
//
// A probe whose capability is missing is reported as error with code
// unsupported (ICMP: icmp_unavailable), which the state machine turns into
// unknown, never into down (Require, CapabilityResult).

// CapabilityRefresh is how often the capabilities are detected again.
const CapabilityRefresh = 15 * time.Minute

// Errors of Require.
var (
	// ErrUnsupportedType: the executor register does not implement the type.
	ErrUnsupportedType = errors.New("probe: type not implemented by this collector")
	// ErrNoPrivilegedUDP: the type needs the UDP ports 67 and 68.
	ErrNoPrivilegedUDP = errors.New("probe: the privileged UDP ports 67 and 68 cannot be bound")
)

// CapabilityDetector detects the capabilities; the zero value uses the
// system. Tests replace the probes of the system.
type CapabilityDetector struct {
	// ListenICMP opens the ICMP socket of a family (nil: ListenICMP).
	ListenICMP ListenFunc
	// ListenUDP binds a UDP port of the host (nil: net.ListenPacket).
	ListenUDP func(port int) (io.Closer, error)
	// TimeSync reports the synchronization of the clock (nil: the system).
	TimeSync func() string
	// Types lists the implemented probe types (nil: Registered).
	Types func() []string
	// Now is the clock of DetectedAt (nil: time.Now).
	Now func() time.Time
}

// DetectCapabilities detects the capabilities of the system.
func DetectCapabilities() probewire.ProbeCapabilities {
	var d CapabilityDetector
	return d.Detect()
}

// Detect runs every detection once.
func (d *CapabilityDetector) Detect() probewire.ProbeCapabilities {
	listenICMP := d.ListenICMP
	if listenICMP == nil {
		listenICMP = ListenICMP
	}
	listenUDP := d.ListenUDP
	if listenUDP == nil {
		listenUDP = func(port int) (io.Closer, error) {
			return net.ListenPacket("udp", ":"+strconv.Itoa(port))
		}
	}
	timeSync := d.TimeSync
	if timeSync == nil {
		timeSync = systemTimeSync
	}
	types := d.Types
	if types == nil {
		types = Registered
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	icmpOf := func(family int) string {
		c, unprivileged, err := listenICMP(family)
		if err != nil {
			return probewire.CapabilityNone
		}
		_ = c.Close()
		if unprivileged {
			return probewire.CapabilityUnprivileged
		}
		return probewire.CapabilityRaw
	}
	bindable := func(port int) bool {
		c, err := listenUDP(port)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
	list := types()
	return probewire.ProbeCapabilities{
		ICMP4: icmpOf(4), ICMP6: icmpOf(6),
		UDP67: bindable(67), UDP68: bindable(68),
		TimeSync:   timeSync(),
		ProbeTypes: append([]string{}, list...),
		DetectedAt: now().UTC(),
	}
}

// Capabilities holds the current report of the collector; it is safe for
// concurrent use.
type Capabilities struct {
	report atomic.Pointer[probewire.ProbeCapabilities]
}

// Set replaces the report.
func (c *Capabilities) Set(r *probewire.ProbeCapabilities) { //nolint:gocritic // the report is copied on purpose
	cp := *r
	cp.ProbeTypes = append([]string{}, r.ProbeTypes...)
	c.report.Store(&cp)
}

// Report returns a copy of the current report (nil before the first
// detection).
func (c *Capabilities) Report() *probewire.ProbeCapabilities {
	r := c.report.Load()
	if r == nil {
		return nil
	}
	cp := *r
	cp.ProbeTypes = append([]string{}, r.ProbeTypes...)
	return &cp
}

// Require checks that the collector can execute a probe of type typ (AK3):
// the type is implemented, a type that needs ICMP has an ICMP socket of
// some family (ErrICMPUnavailable, reported as icmp_unavailable), a type
// that needs the privileged UDP ports can bind 67 and 68. Before the first
// detection everything is allowed.
func (c *Capabilities) Require(typ string) error {
	r := c.report.Load()
	if r == nil {
		return nil
	}
	if !r.Implements(typ) {
		return fmt.Errorf("%w: %s", ErrUnsupportedType, typ)
	}
	t, ok := probewire.Lookup(typ)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedType, typ)
	}
	for _, need := range t.Capabilities {
		switch need {
		case probewire.NeedICMP:
			if r.ICMP4 == probewire.CapabilityNone && r.ICMP6 == probewire.CapabilityNone {
				return ErrICMPUnavailable
			}
		case probewire.NeedPrivilegedUDPPorts:
			if !r.UDP67 || !r.UDP68 {
				return ErrNoPrivilegedUDP
			}
		}
	}
	return nil
}

// CapabilityResult is the result of a check that Require refused: error
// with code icmp_unavailable for a missing ICMP socket, else unsupported.
// Both lead to unknown in the state machine, never to down.
func CapabilityResult(err error) probewire.Result {
	if errors.Is(err, ErrICMPUnavailable) {
		return failResult(probewire.StatusError, probewire.ReasonICMPUnavailable)
	}
	return failResult(probewire.StatusError, probewire.ReasonUnsupported)
}

// RunCapabilityRefresh detects the capabilities now and every
// CapabilityRefresh until ctx ends and hands every report to set.
func RunCapabilityRefresh(ctx context.Context, d *CapabilityDetector, set func(probewire.ProbeCapabilities)) {
	detect := func() {
		r := d.Detect()
		set(r)
		slog.Info("probe capabilities detected", "icmp4", r.ICMP4, "icmp6", r.ICMP6, "udp_67", r.UDP67, "udp_68", r.UDP68,
			"time_sync", r.TimeSync, "probe_types", len(r.ProbeTypes))
	}
	detect()
	t := time.NewTicker(CapabilityRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			detect()
		}
	}
}
