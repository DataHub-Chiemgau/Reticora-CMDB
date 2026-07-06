// Package ticket provides the Ticket System (Essential) domain.
package ticket

import "time"

// Ticket represents a support/task ticket.
type Ticket struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	TicketNumber   int       `json:"ticket_number"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	Status         string    `json:"status"`
	Priority       string    `json:"priority"`
	Category       string    `json:"category"`
	ReporterID     string    `json:"reporter_id"`
	AssigneeID     string    `json:"assignee_id,omitempty"`
	TeamID         string    `json:"team_id,omitempty"`
	RelatedCIID    string    `json:"related_ci_id,omitempty"`
	RelatedAssetID string    `json:"related_asset_id,omitempty"`
	DueDate        string    `json:"due_date,omitempty"`
	ResolvedAt     string    `json:"resolved_at,omitempty"`
	ClosedAt       string    `json:"closed_at,omitempty"`
	Tags           []string  `json:"tags"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Comment represents a comment on a ticket.
type Comment struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	TicketID       string    `json:"ticket_id"`
	AuthorID       string    `json:"author_id"`
	Content        string    `json:"content"`
	IsInternal     bool      `json:"is_internal"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateRequest is the payload for creating a ticket.
type CreateRequest struct {
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	Category       string   `json:"category,omitempty"`
	AssigneeID     string   `json:"assignee_id,omitempty"`
	TeamID         string   `json:"team_id,omitempty"`
	RelatedCIID    string   `json:"related_ci_id,omitempty"`
	RelatedAssetID string   `json:"related_asset_id,omitempty"`
	DueDate        string   `json:"due_date,omitempty"`
	Tags           []string `json:"tags,omitempty"`
}

// UpdateRequest is the payload for updating a ticket.
type UpdateRequest struct {
	Title          *string  `json:"title,omitempty"`
	Description    *string  `json:"description,omitempty"`
	Status         *string  `json:"status,omitempty"`
	Priority       *string  `json:"priority,omitempty"`
	Category       *string  `json:"category,omitempty"`
	AssigneeID     *string  `json:"assignee_id,omitempty"`
	TeamID         *string  `json:"team_id,omitempty"`
	RelatedCIID    *string  `json:"related_ci_id,omitempty"`
	RelatedAssetID *string  `json:"related_asset_id,omitempty"`
	DueDate        *string  `json:"due_date,omitempty"`
	Tags           []string `json:"tags,omitempty"`
}

// CommentRequest is the payload for adding a comment.
type CommentRequest struct {
	Content    string `json:"content"`
	IsInternal bool   `json:"is_internal,omitempty"`
}

// FilterParams holds filter parameters for listing tickets.
type FilterParams struct {
	Status     string
	Priority   string
	Category   string
	AssigneeID string
	TeamID     string
	Search     string
	SortBy     string
	SortDir    string
}
