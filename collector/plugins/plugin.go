// Package plugins defines the shared collector plugin contracts and result models.
package plugins

import (
	"context"
	"strings"
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

// Plugin is the interface every collector plugin must implement.
type Plugin interface {
	Name() string
	Discover(ctx context.Context, targets []string, creds map[string]string) ([]Result, error)
	Collect(ctx context.Context, target string, creds map[string]string) (*Result, error)
}

// credentialAliases maps protocols that share the credentials of another
// protocol: SNMP-based power polling uses the SNMP community, WMI and NAS keep
// using the SSH account they used before credentials were namespaced.
var credentialAliases = map[string]string{
	"power": "snmp",
	"nas":   "ssh",
	"wmi":   "ssh",
}

// CredentialsFor selects the credentials a protocol plugin may receive (COL-02).
// Collector credentials are namespaced by protocol ("ssh.username",
// "redfish.password", "snmp.community"); the plugin gets only the entries of
// its own namespace with the prefix removed. A protocol never receives the
// credentials of another protocol unless credentialAliases says so; in
// particular SSH credentials never reach Redfish targets.
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
