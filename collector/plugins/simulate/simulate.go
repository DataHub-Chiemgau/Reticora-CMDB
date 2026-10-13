// Package simulate generates a fixed fleet of 30 devices for development
// and demos (SIM-02): RETICORA_SIMULATE=true makes the collector report
// these devices as DeviceRecords through the real ingest path. The fleet is
// deterministic: two runs at the same time produce identical records, and
// the identities (serials, MACs, addresses) never change between runs.
package simulate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Subnet holds the addresses of the simulated devices (TEST-NET-2, RFC 5737).
const Subnet = "198.51.100.0/27"

// DeviceCount is the size of the simulated fleet (SIM-02).
const DeviceCount = 30

// device is one simulated device of the fleet.
type device struct {
	name, ciType, source, vendor, model, os, osVersion string
	// uplinks are the LLDP neighbors the device reports, by device name
	// and local port.
	uplinks []link
	// outlets are the outlets of a PDU or UPS, named after what they feed.
	outlets []string
	pdu     bool
}

type link struct{ port, peer, peerPort string }

// fleet is the fixed simulated topology: two core switches, four access
// switches, two firewalls and a router, a UPS feeding two PDUs, sixteen
// servers with two power supplies each and two NAS systems.
var fleet = buildFleet()

func buildFleet() []device {
	switchDev := func(name string, uplinks ...link) device {
		return device{name: name, ciType: "switch", source: "snmp", vendor: "Cisco", model: "C9300-48P",
			os: "IOS-XE", osVersion: "17.9.4", uplinks: uplinks}
	}
	devs := []device{
		switchDev("sim-core-01"),
		switchDev("sim-core-02", link{"Te1/1/1", "sim-core-01", "Te1/1/1"}),
	}
	for i := 1; i <= 4; i++ {
		devs = append(devs, switchDev(fmt.Sprintf("sim-access-%02d", i),
			link{"Te1/1/1", "sim-core-01", fmt.Sprintf("Gi1/0/%d", i)},
			link{"Te1/1/2", "sim-core-02", fmt.Sprintf("Gi1/0/%d", i)}))
	}
	for i := 1; i <= 2; i++ {
		devs = append(devs, device{name: fmt.Sprintf("sim-fw-%02d", i), ciType: "firewall", source: "snmp",
			vendor: "Fortinet", model: "FortiGate-100F", os: "FortiOS", osVersion: "7.4.3",
			uplinks: []link{{"port1", fmt.Sprintf("sim-core-%02d", i), "Gi1/0/24"}}})
	}
	devs = append(devs, device{name: "sim-rtr-01", ciType: "router", source: "snmp", vendor: "Juniper",
		model: "MX204", os: "Junos", osVersion: "23.2R1", uplinks: []link{{"et-0/0/0", "sim-fw-01", "port2"}}},
		device{name: "sim-ups-01", ciType: "ups", source: "snmp", vendor: "APC",
			model: "Smart-UPS SRT 10000", os: "AOS", osVersion: "2.5.0.8", outlets: []string{"sim-pdu-a in1", "sim-pdu-b in1"}})
	var feedA, feedB []string
	for i := 1; i <= 16; i++ {
		feedA = append(feedA, fmt.Sprintf("sim-srv-%02d PSU1", i))
		feedB = append(feedB, fmt.Sprintf("sim-srv-%02d PSU2", i))
	}
	devs = append(devs,
		device{name: "sim-pdu-a", ciType: "pdu", source: "snmp", vendor: "APC", model: "AP8959EU3",
			os: "AOS", osVersion: "7.1.0", outlets: feedA, pdu: true},
		device{name: "sim-pdu-b", ciType: "pdu", source: "snmp", vendor: "APC", model: "AP8959EU3",
			os: "AOS", osVersion: "7.1.0", outlets: feedB, pdu: true})
	for i := 1; i <= 16; i++ {
		devs = append(devs, device{name: fmt.Sprintf("sim-srv-%02d", i), ciType: "server", source: "redfish",
			vendor: "Dell Inc.", model: "PowerEdge R650", os: "Ubuntu", osVersion: "22.04",
			uplinks: []link{{"eno1", fmt.Sprintf("sim-access-%02d", (i-1)%4+1), fmt.Sprintf("Gi1/0/%d", (i-1)/4+1)}}})
	}
	for i := 1; i <= 2; i++ {
		devs = append(devs, device{name: fmt.Sprintf("sim-nas-%02d", i), ciType: "nas", source: "snmp",
			vendor: "Synology", model: "RS3621xs+", os: "DSM", osVersion: "7.2.1",
			uplinks: []link{{"eth0", fmt.Sprintf("sim-access-%02d", i), "Gi1/0/40"}}})
	}
	return devs
}

// index of a device name in the fleet.
func indexOf(name string) int {
	for i := range fleet {
		if fleet[i].name == name {
			return i
		}
	}
	return -1
}

// IP returns the address of the i-th device (198.51.100.1 …).
func IP(i int) string { return fmt.Sprintf("198.51.100.%d", i+1) }

// MAC returns the primary MAC of the i-th device.
func MAC(i int) string { return fmt.Sprintf("00:1b:21:5e:00:%02x", i+1) }

// recordID derives the record ID from the device and the observation time,
// so the same run yields the same records and every run new ones.
func recordID(name string, at time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("reticora-simulate|%s|%d", name, at.UnixNano())))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// record builds the DeviceRecord of the i-th device observed at at.
func record(i int, at time.Time) plugins.DeviceRecord {
	d := &fleet[i]
	at = at.UTC()
	rec := plugins.DeviceRecord{
		RecordID:   recordID(d.name, at),
		ObservedAt: at,
		Source:     d.source,
		TargetIP:   IP(i),
		Identity: plugins.DeviceIdentity{
			SerialNumber: fmt.Sprintf("SIM%05d", i+1),
			PrimaryMAC:   MAC(i),
			Hostname:     d.name,
			FQDN:         d.name + ".sim.example",
		},
		Classification: plugins.DeviceClassification{
			CIType: d.ciType, Manufacturer: d.vendor, Model: d.model, Profile: "simulate",
		},
		System: plugins.DeviceSystem{
			Name: d.name, Description: d.vendor + " " + d.model, OSName: d.os, OSVersion: d.osVersion,
			// A fixed boot time keeps the uptime deterministic for a given at.
			UptimeSeconds: int64(at.Sub(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)).Seconds()) % (90 * 24 * 3600),
		},
		Attributes: map[string]any{"simulated": true},
		Interfaces: []plugins.DeviceInterface{{
			Name: "mgmt0", IfIndex: 1, MAC: MAC(i), SpeedMbps: 1000, MTU: 1500, AdminStatus: "up", OperStatus: "up",
			IPs: []plugins.DeviceIP{{Address: IP(i), PrefixLength: 27}},
		}},
	}
	if d.ciType == "server" {
		rec.Identity.HardwareUUID = fmt.Sprintf("4c4c4544-0053-4910-8000-%012x", i+1)
	}
	for _, l := range d.uplinks {
		peer := indexOf(l.peer)
		rec.Interfaces = append(rec.Interfaces, plugins.DeviceInterface{Name: l.port, AdminStatus: "up", OperStatus: "up", SpeedMbps: 10000})
		rec.Neighbors = append(rec.Neighbors, plugins.DeviceNeighbor{
			Protocol: "lldp", LocalInterface: l.port, RemoteMAC: MAC(peer), RemoteIP: IP(peer),
			RemoteHostname: l.peer, RemoteInterface: l.peerPort,
		})
	}
	if len(d.outlets) > 0 {
		rec.Power = &plugins.DevicePower{DeviceType: d.ciType, DrawWatts: float64(150 * len(d.outlets))}
		for _, o := range d.outlets {
			rec.Power.Outlets = append(rec.Power.Outlets, plugins.DeviceOutlet{Name: o, State: "on"})
		}
	}
	return rec
}

// Generate returns the DeviceRecords of the whole fleet observed at at.
func Generate(at time.Time) []plugins.DeviceRecord {
	out := make([]plugins.DeviceRecord, len(fleet))
	for i := range fleet {
		out[i] = record(i, at)
	}
	return out
}

// Plugin reports the simulated fleet through the collector's discovery
// cycle like any protocol plugin.
type Plugin struct {
	// Now is the clock of the observations; nil uses time.Now.
	Now func() time.Time
}

// New returns the simulation plugin.
func New() *Plugin { return &Plugin{} }

// Name implements plugins.Plugin.
func (p *Plugin) Name() string { return "simulate" }

func (p *Plugin) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Probe answers for every simulated device among the targets; without
// targets the whole fleet answers.
func (p *Plugin) Probe(ctx context.Context, targets []string, _ map[string]string) ([]plugins.Result, error) {
	wanted := map[string]bool{}
	for _, t := range targets {
		wanted[t] = true
	}
	var out []plugins.Result
	for i := range fleet {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if len(wanted) > 0 && !wanted[IP(i)] {
			continue
		}
		out = append(out, plugins.Result{CIType: fleet[i].ciType, Name: fleet[i].name, IP: IP(i), MAC: MAC(i)})
	}
	return out, nil
}

// Collect returns the DeviceRecord of the simulated device at target.
func (p *Plugin) Collect(_ context.Context, target string, _ map[string]string) (*plugins.DeviceRecord, error) {
	ip := net.ParseIP(target)
	for i := range fleet {
		if ip != nil && ip.Equal(net.ParseIP(IP(i))) {
			rec := record(i, p.now())
			return &rec, nil
		}
	}
	return nil, fmt.Errorf("simulate: no simulated device at %s", target)
}
