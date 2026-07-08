// Package plugins defines the shared collector plugin contracts and result models.
package plugins

import "context"

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
