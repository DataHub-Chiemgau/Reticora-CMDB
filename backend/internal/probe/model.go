// Package probe holds the monitoring probe definitions of phase 3 (PRB-01,
// PRB-03, Epic E): probes, monitoring groups (the device groups of PRB-01
// and CRD-04, E-178), monitoring profiles with the overrides per level,
// collector groups with their members and the probe assignments, and the
// probe state with its history and the ingest cursor (PRB-05, PRB-07).
//
// The tables come with the migrations 000307_probe_definition (WP-935),
// 000308_collector_group (WP-936), 000311_probe_state (WP-944) and
// 000325_probe_result (WP-945, numbers provisional); the repository, the resolution of the
// effective values (WP-937), the assignment engine (WP-941) and the REST API
// (WP-939, WP-946, WP-947) build on these types.
package probe

import (
	"encoding/json"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
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
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	SiteID         string `json:"site_id,omitempty"`
	CIID           string `json:"ci_id,omitempty"`
	// CollectorGroupID is the collector group that runs the probe: one of
	// the probe's client or an org-wide group (WP-936).
	CollectorGroupID string          `json:"collector_group_id"`
	Type             string          `json:"type"`
	Target           string          `json:"target"`
	Params           json.RawMessage `json:"params"`
	Enabled          bool            `json:"enabled"`
	ParentProbeID    string          `json:"parent_probe_id,omitempty"`
	// CreatedBySurrogate is the creator as surrogate (Konvention 16).
	CreatedBySurrogate string    `json:"-"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	// SuspendedReason is set (entitlement) while the feature of the probe
	// type is not active (PKG-09, WP-942); independent of Enabled, read
	// only for the API.
	SuspendedReason string     `json:"suspended_reason,omitempty"`
	SuspendedAt     *time.Time `json:"suspended_at,omitempty"`
	// Origin is manual (the API) or auto (the job probe.autocreate,
	// WP-949); read only for the API.
	Origin string `json:"origin"`
}

// Origins of a probe (probe.origin, WP-949).
const (
	ProbeOriginManual = "manual"
	ProbeOriginAuto   = "auto"
)

// SuspendedEntitlement is the suspended_reason of a probe whose feature
// was lost (PKG-09, WP-942).
const SuspendedEntitlement = "entitlement"

// Active reports whether the probe runs: enabled and not suspended.
func (p *Probe) Active() bool { return p.Enabled && p.SuspendedReason == "" }

// MonitoringGroup groups CIs for the monitoring overrides and the
// credential bindings (device group). ClientID empty is org-wide (E-09).
// Priority decides between groups of one CI: the higher wins, on a tie the
// lexicographically smaller name (WP-937, E-178).
type MonitoringGroup struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	Name           string `json:"name"`
	Priority       int    `json:"priority"`
	// MemberCount is the number of member CIs in the caller's client and
	// site scope (F-414); the API fills it.
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MonitoringGroupMember is the membership of a CI in a group; client and
// site are those of the CI.
type MonitoringGroupMember struct {
	GroupID        string    `json:"group_id"`
	CIID           string    `json:"ci_id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
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

// Modes of a collector group (E-174): distributed spreads the probes over
// the healthy members, failover runs them on the member with the best
// priority.
const (
	ModeDistributed = "distributed"
	ModeFailover    = "failover"
)

// Statuses of a collector group: unreachable when no member is reachable
// (network down, PRB-03).
const (
	GroupReachable   = "reachable"
	GroupUnreachable = "unreachable"
)

// Statuses of a collector group member (PRB-10).
const (
	MemberHealthy  = "healthy"
	MemberDegraded = "degraded"
	MemberDown     = "down"
)

// Liveness timeout of a collector group (E-176, PRB-10): default 30 s,
// configurable from 20 s to 300 s.
const (
	DefaultLivenessTimeout = 30 * time.Second
	MinLivenessTimeout     = 20 * time.Second
	MaxLivenessTimeout     = 300 * time.Second
)

// CollectorGroup is a group of collectors in a customer network that share
// the probes of the group (PRB-03, COL-06). ClientID empty is org-wide; a
// site belongs to the group's client. Every collector gets a group of its
// own when it is created.
type CollectorGroup struct {
	ID              string        `json:"id"`
	OrganizationID  string        `json:"organization_id"`
	ClientID        string        `json:"client_id,omitempty"`
	SiteID          string        `json:"site_id,omitempty"`
	Name            string        `json:"name"`
	Mode            string        `json:"mode"`
	LivenessTimeout time.Duration `json:"-"`
	AnchorProbeID   string        `json:"anchor_probe_id,omitempty"`
	Status          string        `json:"status"`
	StatusChangedAt time.Time     `json:"status_changed_at"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

// CollectorGroupMember is the membership of a collector in its group (a
// collector is in exactly one group); client and site are the group's.
// ConfigVersion is the version of the assignment set the member applied
// (PRB-11).
type CollectorGroupMember struct {
	GroupID        string     `json:"group_id"`
	CollectorID    string     `json:"collector_id"`
	OrganizationID string     `json:"organization_id"`
	ClientID       string     `json:"client_id,omitempty"`
	SiteID         string     `json:"site_id,omitempty"`
	Priority       int        `json:"priority"`
	Weight         int        `json:"weight"`
	MemberStatus   string     `json:"member_status"`
	LastAliveAt    *time.Time `json:"last_alive_at,omitempty"`
	ClockOffsetMS  *int       `json:"clock_offset_ms,omitempty"`
	CapacityHint   *int       `json:"capacity_hint,omitempty"`
	ConfigVersion  int64      `json:"config_version"`
}

// Assignment assigns a probe to one member of its collector group under an
// epoch, the fencing token of PRB-11: a collector checks only probes
// assigned to it with a valid epoch. Client and site are the probe's.
type Assignment struct {
	ProbeID        string    `json:"probe_id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	CollectorID    string    `json:"collector_id"`
	Epoch          int64     `json:"epoch"`
	AssignedAt     time.Time `json:"assigned_at"`
}

// States of a probe (PRB-05, Konvention 32); the reason tells why, e.g.
// collector_unreachable or parent_down. Unreachable is no state.
const (
	StateUp       = "up"
	StateDegraded = "degraded"
	StateDown     = "down"
	StateUnknown  = "unknown"
)

// States lists the probe states.
var States = []string{StateUp, StateDegraded, StateDown, StateUnknown}

// ValidState reports whether s is a probe state.
func ValidState(s string) bool {
	for _, v := range States {
		if s == v {
			return true
		}
	}
	return false
}

// State is the current state of a probe (table probe_state, WP-944).
// Client and site are the probe's. LastSeq and Epoch are those of the last
// applied state change; CollectorID is the collector that reported it.
type State struct {
	ProbeID         string     `json:"probe_id"`
	OrganizationID  string     `json:"organization_id"`
	ClientID        string     `json:"client_id,omitempty"`
	SiteID          string     `json:"site_id,omitempty"`
	State           string     `json:"state"`
	Reason          string     `json:"reason"`
	Since           time.Time  `json:"since"`
	LastResultAt    *time.Time `json:"last_result_at,omitempty"`
	LastSeq         int64      `json:"last_seq"`
	Epoch           int64      `json:"epoch"`
	CollectorID     string     `json:"collector_id,omitempty"`
	ConsecutiveFail int        `json:"consecutive_fail"`
	ConsecutiveOK   int        `json:"consecutive_ok"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// StateChange is one state change of a probe (table probe_state_change,
// PRB-05, PRB-07). ObservedAt is the business time on the collector
// (E-175), ReceivedAt the arrival on the server. (ProbeID, CollectorID,
// Epoch, Seq) is the idempotency key. Late marks a change delivered after
// the state had already ended, Notify whether it may notify (E-180); the
// ingest of WP-961 sets both. Client, site and (when empty) the CI are the
// probe's.
type StateChange struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	ProbeID        string    `json:"probe_id"`
	CIID           string    `json:"ci_id,omitempty"`
	FromState      string    `json:"from_state"`
	ToState        string    `json:"to_state"`
	Reason         string    `json:"reason"`
	ObservedAt     time.Time `json:"observed_at"`
	ReceivedAt     time.Time `json:"received_at"`
	Epoch          int64     `json:"epoch"`
	Seq            int64     `json:"seq"`
	CollectorID    string    `json:"collector_id,omitempty"`
	Late           bool      `json:"late"`
	Notify         bool      `json:"notify"`
	RootEventID    string    `json:"root_event_id,omitempty"`
}

// Status codes of probe_result.status (WP-945): the encoding of
// probewire.Result.status in the hypertable. Ingest (WP-960) and the
// aggregates (WP-972) use only these values; the CHECK of the column admits
// nothing else.
const (
	ResultStatusOK    int16 = 0
	ResultStatusFail  int16 = 1
	ResultStatusError int16 = 2
)

// ResultStatusCode returns the code of a result status of the wire
// contract; ok false for an unknown status.
func ResultStatusCode(s probewire.Status) (code int16, ok bool) {
	switch s {
	case probewire.StatusOK:
		return ResultStatusOK, true
	case probewire.StatusFail:
		return ResultStatusFail, true
	case probewire.StatusError:
		return ResultStatusError, true
	}
	return 0, false
}

// Streams of the ingest cursor: state changes and results.
const (
	StreamState   = "state"
	StreamResults = "results"
)

// IngestCursor is the position of the ingest per collector and stream
// (table probe_ingest_cursor): the epoch and the last applied sequence
// number. No API route reads it; the ingest worker finds cursors across
// organizations with the system flag (read only).
type IngestCursor struct {
	OrganizationID string    `json:"organization_id"`
	CollectorID    string    `json:"collector_id"`
	Stream         string    `json:"stream"`
	Epoch          int64     `json:"epoch"`
	LastSeq        int64     `json:"last_seq"`
	UpdatedAt      time.Time `json:"updated_at"`
}
