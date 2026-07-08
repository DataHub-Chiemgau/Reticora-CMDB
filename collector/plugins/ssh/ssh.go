// Package ssh provides an SSH-driven command collection plugin scaffold.
package ssh

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Plugin collects inventory from systems reachable over SSH.
type Plugin struct{}

var _ plugins.Plugin = (*Plugin)(nil)

// New creates an SSH plugin instance.
func New() *Plugin { return &Plugin{} }

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "ssh" }

// Discover determines which targets accept the configured SSH credentials.
func (p *Plugin) Discover(ctx context.Context, targets []string, creds map[string]string) ([]plugins.Result, error) {
	_ = ctx
	_ = creds
	// TODO: Add SSH banner checks and authentication probing with safe backoff behavior.
	results := make([]plugins.Result, 0, len(targets))
	for _, target := range targets {
		results = append(results, plugins.Result{
			CIType:     "server",
			Name:       target,
			IP:         target,
			Attributes: map[string]any{"transport": p.Name()},
		})
	}
	return results, nil
}

// Collect executes profile-defined commands on a single target.
func (p *Plugin) Collect(ctx context.Context, target string, creds map[string]string) (*plugins.Result, error) {
	_ = ctx
	_ = creds
	// TODO: Execute vendor command sets and parse the returned facts into CMDB fields.
	return &plugins.Result{
		CIType:     "server",
		Name:       target,
		IP:         target,
		Attributes: map[string]any{"transport": p.Name()},
	}, nil
}
