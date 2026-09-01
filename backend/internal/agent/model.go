// Package agent implements the server-side endpoint-agent surface (spec §9.3):
// telemetry ingest into the metric store, endpoint → CI reconciliation with
// source "agent", heartbeat/registration, and per-agent config policy +
// kill-switch.
package agent

import "time"

// TelemetryPayload is one report from an endpoint agent.
type TelemetryPayload struct {
	AgentID    string             `json:"agent_id"`
	Hostname   string             `json:"hostname"`
	Version    string             `json:"version,omitempty"`
	OS         string             `json:"os,omitempty"`
	Arch       string             `json:"arch,omitempty"`
	Metrics    map[string]float64 `json:"metrics,omitempty"`
	SystemInfo map[string]string  `json:"system_info,omitempty"`
	// Software is the installed-software inventory feeding patch posture.
	Software   []SoftwareItem `json:"software,omitempty"`
	CollectedAt time.Time     `json:"collected_at,omitempty"`
}

// SoftwareItem is one installed package/application.
type SoftwareItem struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Vendor  string `json:"vendor,omitempty"`
}

// Agent is a registered endpoint agent (an enrolled endpoint).
type Agent struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	AgentID        string     `json:"agent_id"`
	Hostname       string     `json:"hostname"`
	Version        string     `json:"version,omitempty"`
	OS             string     `json:"os,omitempty"`
	Arch           string     `json:"arch,omitempty"`
	CIID           string     `json:"ci_id,omitempty"`
	Status         string     `json:"status"` // online | offline | disabled (kill-switch)
	LastHeartbeat  *time.Time `json:"last_heartbeat,omitempty"`
	Policy         Policy     `json:"policy"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Policy is the central config pushed to the agent (interval, telemetry on/off).
type Policy struct {
	IntervalSeconds int  `json:"interval_seconds"`
	MetricsEnabled  bool `json:"metrics_enabled"`
	InventoryEnabled bool `json:"inventory_enabled"`
}

// DefaultPolicy is the policy applied to newly registered agents.
func DefaultPolicy() Policy {
	return Policy{IntervalSeconds: 60, MetricsEnabled: true, InventoryEnabled: true}
}

// EnrollRequest registers an agent (after edge enrollment authenticated it).
type EnrollRequest struct {
	AgentID  string `json:"agent_id"`
	Hostname string `json:"hostname"`
	Version  string `json:"version,omitempty"`
	OS       string `json:"os,omitempty"`
	Arch     string `json:"arch,omitempty"`
}

// UpdatePolicyRequest replaces an agent's policy.
type UpdatePolicyRequest struct {
	IntervalSeconds *int  `json:"interval_seconds,omitempty"`
	MetricsEnabled  *bool `json:"metrics_enabled,omitempty"`
	InventoryEnabled *bool `json:"inventory_enabled,omitempty"`
}
