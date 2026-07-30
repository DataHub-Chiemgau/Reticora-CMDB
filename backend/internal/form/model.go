// Package form provides JSON-Schema backed form definitions and submissions.
package form

import "time"

type JSONMap map[string]any

// Definition describes an active, tenant-scoped form definition.
type Definition struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Schema         JSONMap   `json:"schema"`
	UIHints        JSONMap   `json:"ui_hints"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Submission stores values submitted against a definition.
type Submission struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	FormID         string    `json:"form_id"`
	Values         JSONMap   `json:"values"`
	SubmittedBy    string    `json:"submitted_by,omitempty"`
	CIID           string    `json:"ci_id,omitempty"`
	TicketID       string    `json:"ticket_id,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateDefinitionRequest struct {
	ClientID    string  `json:"client_id,omitempty"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Schema      JSONMap `json:"schema"`
	UIHints     JSONMap `json:"ui_hints,omitempty"`
	Active      *bool   `json:"active,omitempty"`
}

type UpdateDefinitionRequest struct {
	ClientID    *string `json:"client_id,omitempty"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Schema      JSONMap `json:"schema,omitempty"`
	UIHints     JSONMap `json:"ui_hints,omitempty"`
	Active      *bool   `json:"active,omitempty"`
}

type CreateSubmissionRequest struct {
	Values   JSONMap `json:"values"`
	CIID     string  `json:"ci_id,omitempty"`
	TicketID string  `json:"ticket_id,omitempty"`
	Status   string  `json:"status,omitempty"`
}

type SubmissionFilter struct {
	FormID   string
	Status   string
	TicketID string
	CIID     string
}
