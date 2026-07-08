// Package ipmi provides an IPMI collection plugin scaffold.
package ipmi

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Plugin collects hardware information over IPMI.
type Plugin struct{}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates an IPMI plugin instance.
func New() *Plugin { return &Plugin{} }

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "ipmi" }

// Discover finds IPMI-enabled controllers from candidate targets.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	_ = ctx
	_ = creds
	// TODO: Probe RMCP/IPMI reachability and negotiate supported authentication methods.
	results := make([]plugins.Result, 0, len(targets))
	for _, target := range targets {
		results = append(results, plugins.Result{
			CIType:     "bmc",
			Name:       target,
			IP:         target,
			Attributes: map[string]any{"protocol": p.Name()},
		})
	}
	return results, nil
}

// Collect queries inventory and sensor data from a single IPMI endpoint.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	_ = ctx
	_ = creds
	// TODO: Gather FRU, SEL, and sensor data and map it to CMDB resources and metrics.
	return &plugins.Result{
		CIType:     "bmc",
		Name:       target,
		IP:         target,
		Attributes: map[string]any{"protocol": p.Name()},
	}, nil
}
