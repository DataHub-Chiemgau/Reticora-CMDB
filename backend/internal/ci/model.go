// Package ci provides the CI domain model, repository, and HTTP handler for Reticora CMDB.
package ci

import "time"

// Item represents a Configuration Item instance.
type Item struct {
	ID              string         `json:"id"`
	OrganizationID  string         `json:"organization_id"`
	ClientID        string         `json:"client_id,omitempty"`
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
	DeletedAt       *time.Time     `json:"deleted_at,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

// CreateRequest is the payload for creating a CI.
type CreateRequest struct {
	CITypeID        string         `json:"ci_type_id"`
	ClientID        string         `json:"client_id,omitempty"`
	SiteID          string         `json:"site_id,omitempty"`
	RoomID          string         `json:"room_id,omitempty"`
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
}

// FilterParams holds query filter parameters for listing CIs.
type FilterParams struct {
	Status   string
	TypeID   string
	ClientID string
	SiteID   string
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
