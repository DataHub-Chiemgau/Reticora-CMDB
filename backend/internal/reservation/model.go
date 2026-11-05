// Package reservation implements inventory reservations and availability
// (spec §10) for serialized assets and quantity-based items. Conflicting
// reservations are prevented (serialized assets admit at most one active
// reservation — enforced by a partial unique index), and expired reservations
// release automatically via the sweeper.
package reservation

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
)

// Reservation states.
var States = map[string]bool{
	"active": true, "released": true, "expired": true, "consumed": true,
}

// Reservation reserves an asset or a quantity of an item for a period.
type Reservation struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	ItemKind       string     `json:"item_kind"` // asset | quantity_item
	AssetID        string     `json:"asset_id,omitempty"`
	QuantityItemID string     `json:"quantity_item_id,omitempty"`
	Quantity       float64    `json:"quantity"`
	State          string     `json:"state"`
	ReservedFrom   time.Time  `json:"reserved_from"`
	ReservedUntil  *time.Time `json:"reserved_until,omitempty"`
	AssigneeID     string     `json:"assignee_id,omitempty"`
	ProjectRef     string     `json:"project_ref,omitempty"`
	OrderID        string     `json:"order_id,omitempty"`
	TicketID       string     `json:"ticket_id,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	CreatedBy      string     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// CreateRequest is the payload for creating a reservation.
type CreateRequest struct {
	ItemKind       string   `json:"item_kind,omitempty"`
	AssetID        string   `json:"asset_id,omitempty"`
	QuantityItemID string   `json:"quantity_item_id,omitempty"`
	Quantity       *float64 `json:"quantity,omitempty"`
	ReservedFrom   string   `json:"reserved_from,omitempty"`
	ReservedUntil  string   `json:"reserved_until,omitempty"`
	AssigneeID     string   `json:"assignee_id,omitempty"`
	ProjectRef     string   `json:"project_ref,omitempty"`
	OrderID        string   `json:"order_id,omitempty"`
	TicketID       string   `json:"ticket_id,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

// ReserveRequest is the payload of POST /api/v1/assets/{id}/reserve: the
// asset comes from the path; a reservation is for a user (assignee_id) or
// a project (project_ref). Without expires_at the reservation expires at
// reserved_until.
type ReserveRequest struct {
	ReservedUntil string `json:"reserved_until"`
	AssigneeID    string `json:"assignee_id,omitempty"`
	ProjectRef    string `json:"project_ref,omitempty"`
	Reason        string `json:"reason,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// ConvertRequest is the payload of POST /api/v1/reservations/{id}/convert:
// the holder of the assignment, a user (assignee_id) or a team (team_id);
// without both the user of the reservation. A project reservation needs
// one of them (422).
type ConvertRequest struct {
	AssigneeID string     `json:"assignee_id,omitempty"`
	TeamID     string     `json:"team_id,omitempty"`
	DueAt      *time.Time `json:"due_at,omitempty"`
	Notes      string     `json:"notes,omitempty"`
}

// ConvertResult is the answer of a conversion: the consumed reservation
// and the assignment of its asset.
type ConvertResult struct {
	Reservation *Reservation           `json:"reservation"`
	Assignment  *assignment.Assignment `json:"assignment"`
}

// Availability is the stock projection for an item (spec §10).
type Availability struct {
	ItemKind    string  `json:"item_kind"`
	ItemID      string  `json:"item_id"`
	Total       float64 `json:"total"`
	Available   float64 `json:"available"`
	Reserved    float64 `json:"reserved"`
	Assigned    float64 `json:"assigned"`
	Repair      float64 `json:"repair"`
	Unavailable float64 `json:"unavailable"`
}

// AvailabilityFilter scopes the availability query.
type AvailabilityFilter struct {
	ItemKind string
	ItemID   string
}
