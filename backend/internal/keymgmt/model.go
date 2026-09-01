// Package keymgmt implements the key management module (spec §6.9):
// physical/digital key items with issue/return tracking.
package keymgmt

import "time"

// Item is a managed key (physical or digital).
type Item struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Name           string    `json:"name"`
	KeyType        string    `json:"key_type"` // physical | digital
	Identifier     string    `json:"identifier,omitempty"`
	Status         string    `json:"status"`   // available | issued | lost | retired
	Location       string    `json:"location,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Assignment is the issue/return record of a key item to a user.
type Assignment struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	KeyItemID      string     `json:"key_item_id"`
	AssignedTo     string     `json:"assigned_to"`
	IssuedAt       time.Time  `json:"issued_at"`
	ReturnedAt     *time.Time `json:"returned_at,omitempty"`
	IssuedBy       string     `json:"issued_by,omitempty"`
	Notes          string     `json:"notes,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// CreateItemRequest is the payload for creating a key item.
type CreateItemRequest struct {
	ClientID   string `json:"client_id,omitempty"`
	Name       string `json:"name"`
	KeyType    string `json:"key_type,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	Location   string `json:"location,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// UpdateItemRequest is the payload for updating a key item.
type UpdateItemRequest struct {
	ClientID   *string `json:"client_id,omitempty"`
	Name       *string `json:"name,omitempty"`
	KeyType    *string `json:"key_type,omitempty"`
	Identifier *string `json:"identifier,omitempty"`
	Status     *string `json:"status,omitempty"`
	Location   *string `json:"location,omitempty"`
	Notes      *string `json:"notes,omitempty"`
}

// IssueRequest issues a key to a user.
type IssueRequest struct {
	AssignedTo string `json:"assigned_to"`
	Notes      string `json:"notes,omitempty"`
}

// FilterParams scopes key item list queries.
type FilterParams struct {
	KeyType  string
	Status   string
	ClientID string
	Search   string
}
