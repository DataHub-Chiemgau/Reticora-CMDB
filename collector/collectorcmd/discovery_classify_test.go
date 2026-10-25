// Classification of the collector's discovery passes (WP-417; DIS-02,
// F-030): the records of one run are merged per target before the upload.
// The sweep record of a target that another protocol classified is folded
// into that protocol's record and never reaches the server as an
// unclassified generic_device; a target no protocol classified stays a
// generic_device with classified_by "fallback".
// TestAuditClassifiedDeviceNotUploadedAsFallback moved here from
// discovery_auditdefect_test.go.
package collectorcmd

import (
	"context"
	"fmt"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/sweep"
)

// TestAuditClassifiedDeviceNotUploadedAsFallback: a device that SNMP
// classified (Catalyst sysObjectID) is not uploaded a second time as an
// evidence-less generic_device with classified_by=fallback from its sweep
// record, which makes the server open a false unclassified_device review
// every sweep (DIS-02): only snmp:switch is uploaded, with the sweep's ARP
// MAC. Control: a host only the sweep found is uploaded as the unclassified
// device.
func TestAuditClassifiedDeviceNotUploadedAsFallback(t *testing.T) {
	up, uploaded := auditUploadSink(t)
	// An unprivileged port: CI runs the tests without root.
	ln, ip := auditLoopback(t, 0)
	port := ln.Addr().(*net.TCPAddr).Port
	other, otherIP := auditLoopback(t, port)
	for _, l := range []net.Listener{ln, other} {
		t.Cleanup(func() { _ = l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
	}
	realSweep := sweep.New()
	realSweep.TCPPorts = []int{port}
	realSweep.Timeout = time.Second
	macs := map[string]string{ip: "00:1b:21:41:70:01", otherIP: "00:1b:21:41:70:02"}
	sweepSpy := &auditSpy{name: "sweep", alive: func(targets []string) []plugins.Result {
		var out []plugins.Result
		for _, tg := range targets {
			// The sweep learns the MAC from the ARP table.
			out = append(out, plugins.Result{IP: tg, Name: tg, MAC: macs[tg]})
		}
		return out
	}, collect: func(ctx context.Context, target string) (*plugins.DeviceRecord, error) {
		return realSweep.Collect(ctx, target, nil)
	}}
	// SNMP answers only on the switch.
	snmpSpy := &auditSpy{name: "snmp", alive: func(targets []string) []plugins.Result {
		if slices.Contains(targets, ip) {
			return []plugins.Result{{IP: ip, Name: ip}}
		}
		return nil
	}, collect: func(_ context.Context, target string) (*plugins.DeviceRecord, error) {
		return plugins.CollectRecord("snmp", &plugins.Result{CIType: "network.device", Name: "core-sw-1", IP: target, Attributes: map[string]any{
			"sysObjectID": "1.3.6.1.4.1.9.1.2494",
			"sysDescr":    "Cisco IOS XE Software, Catalyst L3 Switch Software (CAT9K_IOSXE), C9300-48P",
			"sysName":     "core-sw-1",
		}}, nil, time.Now())
	}}
	auditUsePlugins(t, sweepSpy, snmpSpy)
	collectorScopes.set([]collectorScope{
		{ID: "6f0b6f0e-0000-4000-8000-0000000000aa", Kind: "host", Target: ip, Schedule: "24h", Plugins: []string{"sweep", "snmp"}},
		{ID: "6f0b6f0e-0000-4000-8000-0000000000ab", Kind: "host", Target: otherIP, Schedule: "24h", Plugins: []string{"sweep", "snmp"}},
	}, nil)
	runDueDiscovery(context.Background(), &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now())

	recs := uploaded()
	var classified, fallback, others []string
	for _, r := range recs {
		by, _ := r.Attributes["classified_by"].(string)
		entry := fmt.Sprintf("%s:%s/%s", r.Source, r.Classification.CIType, by)
		switch {
		case r.TargetIP == otherIP:
			others = append(others, entry)
		case r.TargetIP != ip:
		case by == "fallback":
			fallback = append(fallback, entry)
		default:
			classified = append(classified, entry)
			if r.Identity.PrimaryMAC != macs[ip] {
				t.Errorf("%s of %s carries MAC %q, want the sweep's ARP MAC %s", entry, ip, r.Identity.PrimaryMAC, macs[ip])
			}
		}
	}
	if len(classified) == 0 {
		t.Fatalf("control: the SNMP record of %s was not uploaded as classified (uploaded %d records: classified=%v fallback=%v)", ip, len(recs), classified, fallback)
	}
	if len(fallback) > 0 {
		t.Errorf("DIS-02 violated: the SNMP-classified device %s was also uploaded as %v next to %v; the server opens an unclassified_device review for it on every sweep",
			ip, fallback, classified)
	}
	if !slices.Equal(classified, []string{"snmp:switch/snmp"}) {
		t.Errorf("records of the switch %s = %v, want only snmp:switch/snmp", ip, classified)
	}
	// No protocol classified the other host: its sweep record is the
	// unclassified device the server reviews.
	if !slices.Equal(others, []string{"sweep:generic_device/fallback"}) {
		t.Errorf("control: records of the sweep-only host %s = %v, want sweep:generic_device/fallback", otherIP, others)
	}
}
