// Package maintenance implements maintenance windows with customer
// notification (spec §6.9): a window references the affected CIs; notifying
// derives the affected clients via the CIs' client assignment.
package maintenance

import "time"

// Window is a scheduled maintenance time window.
type Window struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CIIDs          []string  `json:"ci_ids,omitempty"`
}

// Notification is a per-client customer notification for a window.
type Notification struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	WindowID       string     `json:"maintenance_window_id"`
	ClientID       string     `json:"client_id,omitempty"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// CreateWindowRequest is the payload for creating a maintenance window.
type CreateWindowRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	StartsAt    string   `json:"starts_at"`
	EndsAt      string   `json:"ends_at"`
	CIIDs       []string `json:"ci_ids,omitempty"`
}

// UpdateWindowRequest is the payload for updating a maintenance window.
type UpdateWindowRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

// FilterParams scopes window list queries.
type FilterParams struct {
	Status string
}
