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
	// IPAddress is the agent's current network fingerprint; a site differing
	// from the confirmed one is suggested, never applied (AGT-06).
	IPAddress string `json:"ip_address,omitempty"`
	// Software is the installed-software inventory feeding patch posture.
	Software    []SoftwareItem `json:"software,omitempty"`
	CollectedAt time.Time      `json:"collected_at,omitempty"`
}

// SoftwareItem is one installed package/application.
type SoftwareItem struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Vendor  string `json:"vendor,omitempty"`
}

// Agent is a registered endpoint agent (an enrolled endpoint).
type Agent struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	AgentID        string `json:"agent_id"`
	Hostname       string `json:"hostname"`
	Version        string `json:"version,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	CIID           string `json:"ci_id,omitempty"`
	// ClientID and SiteID come from the enrollment token (AGT-06). A
	// roaming agent (token without site) gets SuggestedSiteID from its
	// network fingerprint; SiteID is set only on manual confirmation.
	ClientID           string     `json:"client_id,omitempty"`
	SiteID             string     `json:"site_id,omitempty"`
	SuggestedSiteID    string     `json:"suggested_site_id,omitempty"`
	NetworkFingerprint string     `json:"network_fingerprint,omitempty"`
	SiteConfirmedAt    *time.Time `json:"site_confirmed_at,omitempty"`
	Status             string     `json:"status"` // online | offline | disabled (kill-switch)
	LastHeartbeat      *time.Time `json:"last_heartbeat,omitempty"`
	Policy             Policy     `json:"policy"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// Policy is the central config pushed to the agent (interval, telemetry on/off).
type Policy struct {
	IntervalSeconds  int  `json:"interval_seconds"`
	MetricsEnabled   bool `json:"metrics_enabled"`
	InventoryEnabled bool `json:"inventory_enabled"`
}

// DefaultPolicy is the policy applied to newly registered agents.
func DefaultPolicy() Policy {
	return Policy{IntervalSeconds: 60, MetricsEnabled: true, InventoryEnabled: true}
}

// EnrollRequest registers an agent with a single-use enrollment token that
// binds it to a client and optionally a site (AGT-06). IPAddress is the
// agent's network fingerprint; the server suggests a site from it.
type EnrollRequest struct {
	AgentID         string `json:"agent_id"`
	Hostname        string `json:"hostname"`
	Version         string `json:"version,omitempty"`
	OS              string `json:"os,omitempty"`
	Arch            string `json:"arch,omitempty"`
	EnrollmentToken string `json:"enrollment_token"`
	IPAddress       string `json:"ip_address,omitempty"`
}

// EnrollmentToken binds the agents enrolled with it to a client and, unless
// SiteID is empty (roaming devices), a site. The secret is returned once on
// creation and stored only as a hash.
type EnrollmentToken struct {
	ID          string     `json:"id"`
	ClientID    string     `json:"client_id"`
	SiteID      string     `json:"site_id,omitempty"`
	Description string     `json:"description,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	// Token is the secret, present only in the creation response.
	Token string `json:"token,omitempty"`
}

// CreateEnrollmentTokenRequest creates an enrollment token.
type CreateEnrollmentTokenRequest struct {
	ClientID    string `json:"client_id"`
	SiteID      string `json:"site_id,omitempty"`
	Description string `json:"description,omitempty"`
	// TTLHours defaults to 24 and is capped at 720 (30 days).
	TTLHours int `json:"ttl_hours,omitempty"`
}

// ConfirmSiteRequest confirms the site of a roaming agent.
type ConfirmSiteRequest struct {
	SiteID string `json:"site_id"`
}

// UpdatePolicyRequest replaces an agent's policy.
type UpdatePolicyRequest struct {
	IntervalSeconds  *int  `json:"interval_seconds,omitempty"`
	MetricsEnabled   *bool `json:"metrics_enabled,omitempty"`
	InventoryEnabled *bool `json:"inventory_enabled,omitempty"`
}
