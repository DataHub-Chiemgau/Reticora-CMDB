// Package plugins defines the shared collector plugin contracts and result models.
package plugins

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Result represents a single discovered or collected item.
type Result struct {
	CIType       string
	Name         string
	IP           string
	MAC          string
	Manufacturer string
	Model        string
	Serial       string
	Firmware     string
	Attributes   map[string]any
	Interfaces   []NetworkInterface
	Metrics      []MetricSample
}

// NetworkInterface captures network port information for a result.
type NetworkInterface struct {
	Name    string
	MAC     string
	Speed   int
	MTU     int
	AdminUp bool
	OperUp  bool
	IfIndex int
	VlanID  int
}

// MetricSample holds a point-in-time metric emitted during collection.
type MetricSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Plugin is the interface every collector plugin implements (DIS-03): Name,
// Probe and Collect. The discovery cycle probes all targets of a scope and
// collects the full DeviceRecord of every target that answered.
type Plugin interface {
	// Name is the protocol the plugin speaks ("snmp", "redfish" …).
	Name() string
	// Probe checks cheaply which targets answer the protocol; each hit
	// carries what probing learned.
	Probe(ctx context.Context, targets []string, creds map[string]string) ([]Result, error)
	// Collect gathers the DeviceRecord of one target (DIS-05).
	Collect(ctx context.Context, target string, creds map[string]string) (*DeviceRecord, error)
}

// CollectRecord turns the result of a plugin's collection into the
// DeviceRecord it returns from Collect.
func CollectRecord(protocol string, res *Result, err error, observedAt time.Time) (*DeviceRecord, error) {
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("%s: no data collected", protocol)
	}
	rec := RecordFromResult(protocol, res, observedAt)
	return &rec, nil
}

// credentialAliases maps protocols that share the credentials of another
// protocol: SNMP-based power polling uses the SNMP community. No other
// protocol borrows a namespace; in particular WMI and NAS never receive the
// SSH account (F-028).
var credentialAliases = map[string]string{
	"power": "snmp",
}

// CredentialsFor selects the credentials a protocol plugin may receive (COL-02).
// Collector credentials are namespaced by protocol ("ssh.username",
// "redfish.password", "snmp.community", "wmi.password"); the plugin gets only
// the entries of its own namespace with the prefix removed. A protocol never
// receives the credentials of another protocol unless credentialAliases says
// so; without entries of its namespace it gets none, never a fallback.
func CredentialsFor(protocol string, all map[string]string) map[string]string {
	namespace := protocol
	if alias, ok := credentialAliases[protocol]; ok {
		namespace = alias
	}
	prefix := namespace + "."
	out := make(map[string]string)
	for key, value := range all {
		if name, ok := strings.CutPrefix(key, prefix); ok && name != "" {
			out[name] = value
		}
	}
	return out
}

// OctetText renders an SNMP OctetString value as text: NUL padding and
// surrounding space are dropped; a value that still holds a NUL byte is
// binary and stays empty (PostgreSQL refuses NUL in text and JSON).
func OctetText(b []byte) string {
	s := strings.TrimFunc(string(b), func(r rune) bool { return r == 0 || unicode.IsSpace(r) })
	if strings.IndexByte(s, 0) >= 0 {
		return ""
	}
	return s
}
