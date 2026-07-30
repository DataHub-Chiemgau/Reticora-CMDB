// Package rack provides REST APIs for racks, rack mounts, and cables.
package rack

import "time"

// Rack represents a physical equipment rack in a room.
type Rack struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	RoomID         string    `json:"room_id"`
	Name           string    `json:"name"`
	HeightU        int       `json:"height_u"`
	WidthMM        int       `json:"width_mm"`
	DepthMM        int       `json:"depth_mm"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateRackRequest is the payload for creating a rack.
type CreateRackRequest struct {
	RoomID  string `json:"room_id"`
	Name    string `json:"name"`
	HeightU int    `json:"height_u,omitempty"`
	WidthMM int    `json:"width_mm,omitempty"`
	DepthMM int    `json:"depth_mm,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// UpdateRackRequest is the payload for updating a rack.
type UpdateRackRequest struct {
	Name    *string `json:"name,omitempty"`
	HeightU *int    `json:"height_u,omitempty"`
	WidthMM *int    `json:"width_mm,omitempty"`
	DepthMM *int    `json:"depth_mm,omitempty"`
	Notes   *string `json:"notes,omitempty"`
}

// RackMount represents a CI mounted into a rack at a given position.
type RackMount struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	RackID         string    `json:"rack_id"`
	CIID           string    `json:"ci_id"`
	PositionU      int       `json:"position_u"`
	HeightU        int       `json:"height_u"`
	Face           string    `json:"face"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateMountRequest is the payload for mounting a CI into a rack.
type CreateMountRequest struct {
	CIID      string `json:"ci_id"`
	PositionU int    `json:"position_u"`
	HeightU   int    `json:"height_u,omitempty"`
	Face      string `json:"face,omitempty"`
}

// UpdateMountRequest is the payload for updating a rack mount.
type UpdateMountRequest struct {
	PositionU *int    `json:"position_u,omitempty"`
	HeightU   *int    `json:"height_u,omitempty"`
	Face      *string `json:"face,omitempty"`
}

// Cable represents a physical connection between two network interfaces.
type Cable struct {
	ID                string    `json:"id"`
	OrganizationID    string    `json:"organization_id"`
	Label             string    `json:"label,omitempty"`
	CableType         string    `json:"cable_type"`
	LengthM           *float64  `json:"length_m,omitempty"`
	Color             string    `json:"color,omitempty"`
	SourceInterfaceID string    `json:"source_interface_id,omitempty"`
	TargetInterfaceID string    `json:"target_interface_id,omitempty"`
	Status            string    `json:"status"`
	InstalledAt       *string   `json:"installed_at,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// CreateCableRequest is the payload for creating a cable.
type CreateCableRequest struct {
	Label             string   `json:"label,omitempty"`
	CableType         string   `json:"cable_type,omitempty"`
	LengthM           *float64 `json:"length_m,omitempty"`
	Color             string   `json:"color,omitempty"`
	SourceInterfaceID string   `json:"source_interface_id,omitempty"`
	TargetInterfaceID string   `json:"target_interface_id,omitempty"`
	Status            string   `json:"status,omitempty"`
	InstalledAt       *string  `json:"installed_at,omitempty"`
}

// UpdateCableRequest is the payload for updating a cable.
type UpdateCableRequest struct {
	Label             *string  `json:"label,omitempty"`
	CableType         *string  `json:"cable_type,omitempty"`
	LengthM           *float64 `json:"length_m,omitempty"`
	Color             *string  `json:"color,omitempty"`
	SourceInterfaceID *string  `json:"source_interface_id,omitempty"`
	TargetInterfaceID *string  `json:"target_interface_id,omitempty"`
	Status            *string  `json:"status,omitempty"`
	InstalledAt       *string  `json:"installed_at,omitempty"`
}

// ValidFaces lists allowed rack mount faces.
var ValidFaces = map[string]bool{"front": true, "rear": true, "both": true}

// ValidCableTypes lists allowed cable types.
var ValidCableTypes = map[string]bool{
	"copper": true, "fiber_sm": true, "fiber_mm": true, "coaxial": true, "power": true, "other": true,
}

// ValidCableStatuses lists allowed cable statuses.
var ValidCableStatuses = map[string]bool{"connected": true, "planned": true, "decommissioned": true}
