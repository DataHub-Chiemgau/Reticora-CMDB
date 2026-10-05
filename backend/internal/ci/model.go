// Package ci provides the CI domain model, repository, and HTTP handler for Reticora CMDB.
package ci

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Item represents a Configuration Item instance.
type Item struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	// LocationID is the location of the CI in the canonical location tree
	// (DB-05). SiteID and RoomID are derived from it by the database and are
	// read-only.
	LocationID      string         `json:"location_id,omitempty"`
	SiteID          string         `json:"site_id,omitempty"`
	RoomID          string         `json:"room_id,omitempty"`
	CITypeID        string         `json:"ci_type_id"`
	Name            string         `json:"name"`
	Status          string         `json:"status"` // active|inactive|maintenance|decommissioned|unknown
	Manufacturer    string         `json:"manufacturer,omitempty"`
	Model           string         `json:"model,omitempty"`
	SerialNumber    string         `json:"serial_number,omitempty"`
	HardwareUUID    string         `json:"hardware_uuid,omitempty"`
	ManagementIP    string         `json:"management_ip,omitempty"`
	PrimaryMAC      string         `json:"primary_mac,omitempty"`
	Hostname        string         `json:"hostname,omitempty"`
	FQDN            string         `json:"fqdn,omitempty"`
	OSName          string         `json:"os_name,omitempty"`
	OSVersion       string         `json:"os_version,omitempty"`
	FirmwareVersion string         `json:"firmware_version,omitempty"`
	SysObjectID     string         `json:"sys_object_id,omitempty"`
	Attributes      map[string]any `json:"attributes"`
	DiscoverySource string         `json:"discovery_source,omitempty"` // snmp|ssh|redfish|ipmi|wmi|api|agent|sweep|manual
	FirstSeenAt     *time.Time     `json:"first_seen_at,omitempty"`
	LastSeenAt      *time.Time     `json:"last_seen_at,omitempty"`
	IsManual        bool           `json:"is_manual"`
	// Version is the CI's ETag; it rises only with writes of rank >= 92
	// (manual, import, workflow), not with observed updates (API-07).
	Version   int64      `json:"version"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

// CreateRequest is the payload for creating a CI.
type CreateRequest struct {
	CITypeID        string         `json:"ci_type_id"`
	ClientID        string         `json:"client_id,omitempty"`
	LocationID      string         `json:"location_id,omitempty"`
	Name            string         `json:"name"`
	Status          string         `json:"status,omitempty"`
	Manufacturer    string         `json:"manufacturer,omitempty"`
	Model           string         `json:"model,omitempty"`
	SerialNumber    string         `json:"serial_number,omitempty"`
	HardwareUUID    string         `json:"hardware_uuid,omitempty"`
	ManagementIP    string         `json:"management_ip,omitempty"`
	PrimaryMAC      string         `json:"primary_mac,omitempty"`
	Hostname        string         `json:"hostname,omitempty"`
	FQDN            string         `json:"fqdn,omitempty"`
	OSName          string         `json:"os_name,omitempty"`
	OSVersion       string         `json:"os_version,omitempty"`
	FirmwareVersion string         `json:"firmware_version,omitempty"`
	SysObjectID     string         `json:"sys_object_id,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	DiscoverySource string         `json:"discovery_source,omitempty"`
}

// UpdateRequest is the payload for updating a CI.
type UpdateRequest struct {
	Name            *string        `json:"name,omitempty"`
	Status          *string        `json:"status,omitempty"`
	ClientID        *string        `json:"client_id,omitempty"`
	LocationID      *string        `json:"location_id,omitempty"`
	Manufacturer    *string        `json:"manufacturer,omitempty"`
	Model           *string        `json:"model,omitempty"`
	SerialNumber    *string        `json:"serial_number,omitempty"`
	HardwareUUID    *string        `json:"hardware_uuid,omitempty"`
	ManagementIP    *string        `json:"management_ip,omitempty"`
	PrimaryMAC      *string        `json:"primary_mac,omitempty"`
	Hostname        *string        `json:"hostname,omitempty"`
	FQDN            *string        `json:"fqdn,omitempty"`
	OSName          *string        `json:"os_name,omitempty"`
	OSVersion       *string        `json:"os_version,omitempty"`
	FirmwareVersion *string        `json:"firmware_version,omitempty"`
	SysObjectID     *string        `json:"sys_object_id,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	DiscoverySource *string        `json:"discovery_source,omitempty"`
	LastSeenAt      *string        `json:"last_seen_at,omitempty"`
	// ChangeReason is stored with the overrides a manual change creates.
	ChangeReason string `json:"change_reason,omitempty"`
	// IfMatch is the version the writer last read (If-Match); nil when the
	// request carried none, api.IfMatchAny for "*". Checked for manual
	// changes only.
	IfMatch *int64 `json:"-"`
	// Authoritative marks an automated write of rank >= 92 (workflow,
	// import), which raises the version like a manual change does.
	Authoritative bool `json:"-"`
	// Manual marks a change made by a person through the API (not by
	// discovery or automation): every changed field gets a protected
	// override in the same transaction (OVR-01). Set by the handler only.
	Manual *ManualChange `json:"-"`
}

// ManualChange identifies the author of a manual CI change.
type ManualChange struct {
	Author string
}

// DefaultChangeReason is the override reason of a manual change without one.
const DefaultChangeReason = "manual change"

// FilterParams holds query filter parameters for listing CIs.
type FilterParams struct {
	Status   string
	TypeID   string
	ClientID string
	SiteID   string
	RoomID   string
	Search   string
	SortBy   string
	SortDir  string
}

// Change represents one persisted CI change history row.
type Change struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	CIID           string    `json:"ci_id"`
	ActorID        string    `json:"actor_id,omitempty"`
	ChangeType     string    `json:"change_type"`
	FieldName      string    `json:"field_name,omitempty"`
	OldValue       any       `json:"old_value,omitempty"`
	NewValue       any       `json:"new_value,omitempty"`
	Comment        string    `json:"comment,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Errors of the optimistic concurrency check (API-07, API-03).
var (
	// ErrPreconditionRequired: the organization requires If-Match and the
	// request carried none (412).
	ErrPreconditionRequired = errors.New("If-Match is required for CI changes")
	// ErrPreconditionFailed: the If-Match version is unknown (412).
	ErrPreconditionFailed = errors.New("If-Match does not name a version of the CI")
)

// ConflictError reports fields of a PATCH that were changed by a manual or
// other rank >= 92 write since the If-Match version (409).
type ConflictError struct {
	Fields  []string
	Current int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("fields changed since the If-Match version: %s", strings.Join(e.Fields, ", "))
}
