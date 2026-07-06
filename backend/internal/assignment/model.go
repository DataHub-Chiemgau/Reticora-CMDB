// Package assignment provides the Assignment domain for asset/CI allocation and transfer.
package assignment

import "time"

// Assignment represents the assignment of an asset or CI to a user.
type Assignment struct {
	ID              string    `json:"id"`
	OrganizationID  string    `json:"organization_id"`
	AssetID         string    `json:"asset_id,omitempty"`
	CIID            string    `json:"ci_id,omitempty"`
	AssignedTo      string    `json:"assigned_to"`
	AssignedBy      string    `json:"assigned_by"`
	AssignmentType  string    `json:"assignment_type"`
	Status          string    `json:"status"`
	AssignedAt      time.Time `json:"assigned_at"`
	DueDate         string    `json:"due_date,omitempty"`
	ReturnedAt      string    `json:"returned_at,omitempty"`
	ReturnCondition string    `json:"return_condition,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CreateRequest is the payload for creating an assignment.
type CreateRequest struct {
	AssetID        string `json:"asset_id,omitempty"`
	CIID           string `json:"ci_id,omitempty"`
	AssignedTo     string `json:"assigned_to"`
	AssignmentType string `json:"assignment_type,omitempty"`
	DueDate        string `json:"due_date,omitempty"`
	Notes          string `json:"notes,omitempty"`
}

// ReturnRequest is the payload for returning an assignment.
type ReturnRequest struct {
	ReturnCondition string `json:"return_condition,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

// TransferRequest is the payload for transferring an assignment.
type TransferRequest struct {
	NewAssignee string `json:"new_assignee"`
	Notes       string `json:"notes,omitempty"`
}

// FilterParams holds filter parameters for listing assignments.
type FilterParams struct {
	Status     string
	AssignedTo string
	AssetID    string
	Search     string
	SortBy     string
	SortDir    string
}
