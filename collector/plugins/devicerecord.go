package plugins

import (
	"crypto/rand"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// DeviceRecord is the contract between collector and server for one observed
// device (DIS-05). The JSON form is specified by api/devicerecord.schema.json;
// collector and server share this Go type, so both sides decode and validate
// the same fields.
type DeviceRecord struct {
	// RecordID identifies the observation; the server deduplicates on it.
	RecordID string `json:"record_id"`
	// ObservedAt is the source time of the observation (GLO-12), not the
	// upload or arrival time.
	ObservedAt time.Time `json:"observed_at"`
	// Source is the discovery protocol or connector that produced the record.
	Source   string `json:"source"`
	TargetIP string `json:"target_ip"`
	// VRFID, ScopeID, SiteID and ClientID come from the discovery scope the
	// target belongs to; ClientID and SiteID are those of the scope.
	VRFID    string `json:"vrf_id,omitempty"`
	ScopeID  string `json:"scope_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`

	Identity       DeviceIdentity       `json:"identity"`
	Classification DeviceClassification `json:"classification"`
	System         DeviceSystem         `json:"system"`
	Attributes     map[string]any       `json:"attributes,omitempty"`
	Interfaces     []DeviceInterface    `json:"interfaces,omitempty"`
	Neighbors      []DeviceNeighbor     `json:"neighbors,omitempty"`
	Power          *DevicePower         `json:"power,omitempty"`
}

// DeviceIdentity holds the values the server matches CIs on (REC-01).
type DeviceIdentity struct {
	SerialNumber string `json:"serial_number,omitempty"`
	HardwareUUID string `json:"hardware_uuid,omitempty"`
	PrimaryMAC   string `json:"primary_mac,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	FQDN         string `json:"fqdn,omitempty"`
}

// DeviceClassification says what the device is.
type DeviceClassification struct {
	CIType       string `json:"ci_type"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SysObjectID  string `json:"sys_object_id,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

// DeviceSystem describes the running system.
type DeviceSystem struct {
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	OSName          string `json:"os_name,omitempty"`
	OSVersion       string `json:"os_version,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	UptimeSeconds   int64  `json:"uptime_seconds,omitempty"`
}

// DeviceInterface is a network interface with its addresses.
type DeviceInterface struct {
	Name        string     `json:"name"`
	IfIndex     int        `json:"if_index,omitempty"`
	MAC         string     `json:"mac,omitempty"`
	SpeedMbps   int        `json:"speed_mbps,omitempty"`
	MTU         int        `json:"mtu,omitempty"`
	AdminStatus string     `json:"admin_status,omitempty"`
	OperStatus  string     `json:"oper_status,omitempty"`
	VlanID      int        `json:"vlan_id,omitempty"`
	IPs         []DeviceIP `json:"ips,omitempty"`
}

// DeviceIP is an address configured on an interface.
type DeviceIP struct {
	Address      string `json:"address"`
	PrefixLength int    `json:"prefix_length,omitempty"`
}

// DeviceNeighbor is a device seen next to this one (LLDP, CDP, ARP, FDB).
type DeviceNeighbor struct {
	Protocol        string `json:"protocol"`
	LocalInterface  string `json:"local_interface,omitempty"`
	RemoteMAC       string `json:"remote_mac,omitempty"`
	RemoteIP        string `json:"remote_ip,omitempty"`
	RemoteHostname  string `json:"remote_hostname,omitempty"`
	RemoteInterface string `json:"remote_interface,omitempty"`
}

// DevicePower describes power devices (UPS, PDU) and power readings.
type DevicePower struct {
	DeviceType string         `json:"device_type,omitempty"`
	DrawWatts  float64        `json:"draw_watts,omitempty"`
	Outlets    []DeviceOutlet `json:"outlets,omitempty"`
}

// DeviceOutlet is an outlet of a PDU or UPS.
type DeviceOutlet struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

// DeviceRecordBatch is the ingest request body of the DeviceRecord contract.
type DeviceRecordBatch struct {
	Records []DeviceRecord `json:"records"`
}

// DeviceRecordSources are the values of DeviceRecord.Source.
var DeviceRecordSources = []string{"redfish", "ipmi", "agent", "snmp", "api", "integration", "wmi", "ssh", "nas", "sweep"}

// NeighborProtocols are the values of DeviceNeighbor.Protocol.
var NeighborProtocols = []string{"lldp", "cdp", "arp", "fdb"}

// ReservedAttributeKeys are the attribute namespaces of the server's CI
// storage (CI-10); a record must not use them (GLO-13).
var ReservedAttributeKeys = map[string]bool{"_overrides": true, "_observed": true, "_provenance": true, "_instance": true}

// InterfaceStatuses are the values of admin_status and oper_status.
var InterfaceStatuses = []string{"up", "down", "unknown"}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	macPattern  = regexp.MustCompile(`^[0-9a-fA-F]{2}([:-][0-9a-fA-F]{2}){5}$`)
)

// NewRecordID returns a random (version 4) UUID for DeviceRecord.RecordID.
func NewRecordID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("device record id: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Violation is one field of a DeviceRecord that breaks the contract.
type Violation struct {
	Field   string
	Message string
}

func (v Violation) String() string { return v.Field + ": " + v.Message }

// Validate checks the record against the contract of
// api/devicerecord.schema.json and returns every violation.
func (r *DeviceRecord) Validate() []Violation {
	var out []Violation
	add := func(field, format string, args ...any) {
		out = append(out, Violation{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	uuidField := func(field, value string, required bool) {
		switch {
		case value == "" && required:
			add(field, "is required")
		case value != "" && !uuidPattern.MatchString(value):
			add(field, "must be a UUID")
		}
	}
	ipField := func(field, value string, required bool) {
		switch {
		case value == "" && required:
			add(field, "is required")
		case value != "" && net.ParseIP(value) == nil:
			add(field, "must be an IPv4 or IPv6 address")
		}
	}
	macField := func(field, value string) {
		if value != "" && !macPattern.MatchString(value) {
			add(field, "must be a MAC address")
		}
	}
	oneOf := func(field, value string, allowed []string, required bool) {
		if value == "" {
			if required {
				add(field, "is required")
			}
			return
		}
		for _, a := range allowed {
			if value == a {
				return
			}
		}
		add(field, "must be one of %s", strings.Join(allowed, ", "))
	}

	uuidField("record_id", r.RecordID, true)
	if r.ObservedAt.IsZero() {
		add("observed_at", "is required")
	}
	oneOf("source", r.Source, DeviceRecordSources, true)
	ipField("target_ip", r.TargetIP, true)
	uuidField("vrf_id", r.VRFID, false)
	uuidField("scope_id", r.ScopeID, false)
	uuidField("site_id", r.SiteID, false)
	uuidField("client_id", r.ClientID, false)
	uuidField("identity.hardware_uuid", r.Identity.HardwareUUID, false)
	macField("identity.primary_mac", r.Identity.PrimaryMAC)
	if strings.TrimSpace(r.Classification.CIType) == "" {
		add("classification.ci_type", "is required")
	}
	for key := range r.Attributes {
		if ReservedAttributeKeys[key] {
			add("attributes."+key, "is a reserved namespace")
		}
	}
	if r.System.UptimeSeconds < 0 {
		add("system.uptime_seconds", "must not be negative")
	}
	for i, ifc := range r.Interfaces {
		prefix := fmt.Sprintf("interfaces[%d]", i)
		if strings.TrimSpace(ifc.Name) == "" {
			add(prefix+".name", "is required")
		}
		macField(prefix+".mac", ifc.MAC)
		oneOf(prefix+".admin_status", ifc.AdminStatus, InterfaceStatuses, false)
		oneOf(prefix+".oper_status", ifc.OperStatus, InterfaceStatuses, false)
		if ifc.VlanID < 0 || ifc.VlanID > 4094 {
			add(prefix+".vlan_id", "must be between 1 and 4094")
		}
		for j, ip := range ifc.IPs {
			ipField(fmt.Sprintf("%s.ips[%d].address", prefix, j), ip.Address, true)
			if ip.PrefixLength < 0 || ip.PrefixLength > 128 {
				add(fmt.Sprintf("%s.ips[%d].prefix_length", prefix, j), "must be between 0 and 128")
			}
		}
	}
	for i, n := range r.Neighbors {
		prefix := fmt.Sprintf("neighbors[%d]", i)
		oneOf(prefix+".protocol", n.Protocol, NeighborProtocols, true)
		macField(prefix+".remote_mac", n.RemoteMAC)
		ipField(prefix+".remote_ip", n.RemoteIP, false)
		if n.RemoteMAC == "" && n.RemoteIP == "" && n.RemoteHostname == "" {
			add(prefix, "needs remote_mac, remote_ip or remote_hostname")
		}
	}
	if r.Power != nil {
		if r.Power.DrawWatts < 0 {
			add("power.draw_watts", "must not be negative")
		}
		for i, o := range r.Power.Outlets {
			if strings.TrimSpace(o.Name) == "" {
				add(fmt.Sprintf("power.outlets[%d].name", i), "is required")
			}
		}
	}
	return out
}

// protocolSources maps collector protocols to the record source they report
// as: SNMP power polling is SNMP.
var protocolSources = map[string]string{"power": "snmp"}

// RecordFromResult turns a plugin result into a DeviceRecord observed at
// observedAt by the given protocol plugin.
func RecordFromResult(protocol string, res *Result, observedAt time.Time) DeviceRecord {
	source := protocol
	if mapped, ok := protocolSources[protocol]; ok {
		source = mapped
	}
	attrs := map[string]any{}
	for k, v := range res.Attributes {
		attrs[k] = v
	}
	take := func(key string) string {
		v, ok := attrs[key].(string)
		if ok {
			delete(attrs, key)
		}
		return v
	}

	rec := DeviceRecord{
		RecordID:   NewRecordID(),
		ObservedAt: observedAt.UTC(),
		Source:     source,
		TargetIP:   res.IP,
		Identity: DeviceIdentity{
			SerialNumber: res.Serial,
			PrimaryMAC:   res.MAC,
			Hostname:     take("hostname"),
			FQDN:         take("fqdn"),
		},
		Classification: DeviceClassification{
			CIType:       res.CIType,
			Manufacturer: res.Manufacturer,
			Model:        res.Model,
			SysObjectID:  take("sysObjectID"),
			Profile:      take("profile"),
		},
		System: DeviceSystem{
			Name:            res.Name,
			Description:     take("sysDescr"),
			FirmwareVersion: res.Firmware,
		},
	}
	if net.ParseIP(res.IP) == nil {
		// Targets configured by name: the name identifies the host, the
		// collector resolves the address before upload.
		if rec.Identity.Hostname == "" {
			rec.Identity.Hostname = res.IP
		}
		rec.TargetIP = ""
	}
	if attrs["category"] == "power" {
		rec.Power = &DevicePower{DeviceType: take("deviceType")}
	}
	for _, ifc := range res.Interfaces {
		rec.Interfaces = append(rec.Interfaces, DeviceInterface{
			Name:        ifc.Name,
			IfIndex:     ifc.IfIndex,
			MAC:         ifc.MAC,
			SpeedMbps:   ifc.Speed,
			MTU:         ifc.MTU,
			AdminStatus: upDown(ifc.AdminUp),
			OperStatus:  upDown(ifc.OperUp),
			VlanID:      ifc.VlanID,
		})
	}
	if len(attrs) > 0 {
		rec.Attributes = attrs
	}
	return rec
}

func upDown(up bool) string {
	if up {
		return "up"
	}
	return "down"
}
