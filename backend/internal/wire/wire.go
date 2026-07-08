// Package wire defines shared ingest types used by both the server and collector
// modules. These types are the contract for discovery data exchange and MUST
// remain free of circular dependencies.
//
// This package is referenced by:
// - backend/internal/discovery (server-side ingest processing)
// - collector/plugins (collector-side data production)
package wire

import "time"

// IngestPayload is the top-level payload sent from collector to server.
type IngestPayload struct {
	CollectorID string       `json:"collector_id"`
	Timestamp   time.Time    `json:"timestamp"`
	Items       []IngestItem `json:"items"`
}

// IngestItem represents a single discovered or updated CI from a collector.
type IngestItem struct {
	// Identity fields for matching
	CITypeName   string `json:"ci_type_name"`
	Name         string `json:"name"`
	SerialNumber string `json:"serial_number,omitempty"`
	HardwareUUID string `json:"hardware_uuid,omitempty"`
	ManagementIP string `json:"management_ip,omitempty"`
	PrimaryMAC   string `json:"primary_mac,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	FQDN         string `json:"fqdn,omitempty"`

	// Metadata
	Manufacturer    string `json:"manufacturer,omitempty"`
	Model           string `json:"model,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	OSName          string `json:"os_name,omitempty"`
	OSVersion       string `json:"os_version,omitempty"`
	SysObjectID     string `json:"sys_object_id,omitempty"`

	// Discovery source
	Source string `json:"source"` // snmp|ssh|redfish|ipmi|wmi|api|agent|sweep

	// Flexible attributes
	Attributes map[string]any `json:"attributes,omitempty"`

	// Network interfaces discovered
	Interfaces []IngestInterface `json:"interfaces,omitempty"`

	// Metrics collected
	Metrics []IngestMetric `json:"metrics,omitempty"`

	// Relationships discovered
	Relationships []IngestRelationship `json:"relationships,omitempty"`
}

// IngestInterface represents a discovered network interface.
type IngestInterface struct {
	Name        string `json:"name"`
	IfIndex     int    `json:"if_index,omitempty"`
	MAC         string `json:"mac,omitempty"`
	SpeedMbps   int    `json:"speed_mbps,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
	AdminStatus string `json:"admin_status,omitempty"` // up|down
	OperStatus  string `json:"oper_status,omitempty"`  // up|down|unknown
	IfType      string `json:"if_type,omitempty"`
	Description string `json:"description,omitempty"`
	VlanID      int    `json:"vlan_id,omitempty"`
	Management  bool   `json:"is_management,omitempty"`
}

// IngestMetric represents a collected metric sample.
type IngestMetric struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// IngestRelationship represents a discovered relationship between CIs.
type IngestRelationship struct {
	TargetIP       string  `json:"target_ip,omitempty"`
	TargetMAC      string  `json:"target_mac,omitempty"`
	TargetHostname string  `json:"target_hostname,omitempty"`
	TypeKey        string  `json:"type_key"` // connected_to, powered_by, etc.
	Confidence     float64 `json:"confidence,omitempty"`
}
