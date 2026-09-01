// Package desk implements the desk booking module (spec §6.9): bookable
// shared desks with time-window bookings.
package desk

import "time"

// Desk is a bookable shared workplace.
type Desk struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	RoomID         string         `json:"room_id,omitempty"`
	Name           string         `json:"name"`
	Status         string         `json:"status"` // available | occupied | maintenance
	Attributes     map[string]any `json:"attributes"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// Booking is a time-window reservation of a desk.
type Booking struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	DeskID         string     `json:"desk_id"`
	UserID         string     `json:"user_id"`
	StartsAt       time.Time  `json:"starts_at"`
	EndsAt         time.Time  `json:"ends_at"`
	Status         string     `json:"status"` // active | cancelled | completed
	CreatedAt      time.Time  `json:"created_at"`
}

// CreateDeskRequest is the payload for creating a desk.
type CreateDeskRequest struct {
	RoomID     string         `json:"room_id,omitempty"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// UpdateDeskRequest is the payload for updating a desk.
type UpdateDeskRequest struct {
	RoomID     *string         `json:"room_id,omitempty"`
	Name       *string         `json:"name,omitempty"`
	Status     *string         `json:"status,omitempty"`
	Attributes map[string]any  `json:"attributes,omitempty"`
}

// CreateBookingRequest books a desk for a time window.
type CreateBookingRequest struct {
	UserID   string `json:"user_id,omitempty"`
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

// FilterParams scopes desk list queries.
type FilterParams struct {
	RoomID string
	Status string
	Search string
}
