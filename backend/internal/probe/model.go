// Package probe holds the monitoring probe definitions of phase 3 (PRB-01,
// Epic E): probes, monitoring groups (the device groups of PRB-01 and
// CRD-04, E-178) and monitoring profiles with the overrides per level.
//
// The tables come with migration 000307_probe_definition (WP-935); the
// repository, the resolution of the effective values (WP-937) and the REST
// API (WP-939, WP-946) build on these types.
package probe

import (
	"encoding/json"
	"time"
)

// Levels of the override hierarchy (PRB-01): organization → client →
// monitoring group → CI → probe. A monitoring profile is stored for one of
// the first four levels; the level probe is Probe.Params.
const (
	LevelOrg    = "org"
	LevelClient = "client"
	LevelGroup  = "group"
	LevelCI     = "ci"
	LevelProbe  = "probe"
)

// ProfileLevels are the levels a monitoring profile can have, from the
// widest to the narrowest.
var ProfileLevels = []string{LevelOrg, LevelClient, LevelGroup, LevelCI}

// Levels are all override levels from the widest to the narrowest; the type
// default of the register (api/probe-types.json) lies below them.
var Levels = []string{LevelOrg, LevelClient, LevelGroup, LevelCI, LevelProbe}

// Probe is a central probe definition (table probe). With CIID the client
// and the site are those of the CI; without one ClientID is required.
// Params holds only the overrides of the level probe (interval, timeout,
// down and up count, thresholds and type parameters).
type Probe struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	ClientID       string          `json:"client_id,omitempty"`
	SiteID         string          `json:"site_id,omitempty"`
	CIID           string          `json:"ci_id,omitempty"`
	Type           string          `json:"type"`
	Target         string          `json:"target"`
	Params         json.RawMessage `json:"params"`
	Enabled        bool            `json:"enabled"`
	ParentProbeID  string          `json:"parent_probe_id,omitempty"`
	// CreatedBySurrogate is the creator as surrogate (Konvention 16).
	CreatedBySurrogate string    `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// MonitoringGroup groups CIs for the monitoring overrides and the
// credential bindings (device group). ClientID empty is org-wide (E-09).
// Priority decides between groups of one CI: the higher wins, on a tie the
// lexicographically smaller name (WP-937, E-178).
type MonitoringGroup struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Name           string    `json:"name"`
	Priority       int       `json:"priority"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MonitoringGroupMember is the membership of a CI in a group; client and
// site are those of the CI.
type MonitoringGroupMember struct {
	GroupID        string `json:"group_id"`
	CIID           string `json:"ci_id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	SiteID         string `json:"site_id,omitempty"`
}

// MonitoringProfile holds the overrides of one level for one probe type.
// The target of the level is ClientID for client, GroupID for group, CIID
// for ci and none for org. ClientID and SiteID are also the scope of the
// row: at level group the group's client, at level ci client and site of
// the CI (trigger monitoring_profile_scope).
type MonitoringProfile struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	Level          string          `json:"level"`
	ClientID       string          `json:"client_id,omitempty"`
	GroupID        string          `json:"group_id,omitempty"`
	CIID           string          `json:"ci_id,omitempty"`
	SiteID         string          `json:"site_id,omitempty"`
	ProbeType      string          `json:"probe_type"`
	Params         json.RawMessage `json:"params"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// TargetID returns the ID of the profile's level (empty for org).
func (p *MonitoringProfile) TargetID() string {
	switch p.Level {
	case LevelClient:
		return p.ClientID
	case LevelGroup:
		return p.GroupID
	case LevelCI:
		return p.CIID
	}
	return ""
}
