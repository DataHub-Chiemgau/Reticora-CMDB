// Package ticket provides the Ticket System (Essential) domain.
package ticket

import (
	"encoding/json"
	"time"
)

// Ticket represents a support/task ticket.
type Ticket struct {
	ID             string   `json:"id"`
	OrganizationID string   `json:"organization_id"`
	TicketNumber   int      `json:"ticket_number"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Status         string   `json:"status"`
	Priority       string   `json:"priority"`
	Category       string   `json:"category"`
	ReporterID     string   `json:"reporter_id"`
	AssigneeID     string   `json:"assignee_id,omitempty"`
	TeamID         string   `json:"team_id,omitempty"`
	RelatedCIID    string   `json:"related_ci_id,omitempty"`
	RelatedAssetID string   `json:"related_asset_id,omitempty"`
	DueDate        string   `json:"due_date,omitempty"`
	ResolvedAt     string   `json:"resolved_at,omitempty"`
	ClosedAt       string   `json:"closed_at,omitempty"`
	Tags           []string `json:"tags"` // keys of the catalog tags (ticket_tag, E-127)
	// ExternalProvider and ExternalRef are the foreign reference of the
	// ticket (TKT-03, WP-293): set together by a ticket.Provider, read-only
	// in the API.
	ExternalProvider string    `json:"external_provider,omitempty"`
	ExternalRef      string    `json:"external_ref,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	// ClientID and SiteID are the scope of the ticket (000064) for its
	// field permissions (WP-765); they are no members of the DTO. The
	// memory repository knows no scope.
	ClientID string `json:"-"`
	SiteID   string `json:"-"`
}

// Author types of a ticket comment (WP-766): a comment of a user carries
// its author_id, a comment of the system none.
const (
	AuthorUser   = "user"
	AuthorSystem = "system"
)

// Comment represents a comment on a ticket.
type Comment struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	TicketID       string `json:"ticket_id"`
	// AuthorID is the user who wrote the comment, "" for a system comment
	// (author_id null in the DTO).
	AuthorID string `json:"author_id"`
	// AuthorType is user or system ("" counts as user).
	AuthorType string `json:"author_type"`
	Content    string `json:"content"`
	IsInternal bool   `json:"is_internal"`
	// ExternalProvider and ExternalRef are the foreign reference of the
	// comment (TKT-03, WP-293), read-only in the API.
	ExternalProvider string    `json:"external_provider,omitempty"`
	ExternalRef      string    `json:"external_ref,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// MarshalJSON writes the comment with author_id null when it has no author
// (system comment, WP-766).
func (c Comment) MarshalJSON() ([]byte, error) { //nolint:gocritic // value receiver: comments are marshaled as values and pointers
	type plain Comment
	var author *string
	if c.AuthorID != "" {
		author = &c.AuthorID
	}
	return json.Marshal(struct {
		plain
		AuthorID *string `json:"author_id"`
	}{plain: plain(c), AuthorID: author})
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
	ReporterID string
	TeamID     string
	// TeamMemberOf is the user id of the principal for the filter value
	// team_id=mine: tickets of every team the user is a member of
	// (team_member, E-143). It replaces TeamID.
	TeamMemberOf string
	Tags         []string // tags: every key (catalog tags, WP-764)
	Search       string
	SortBy       string
	SortDir      string
}

// Entity types of a ticket link (WP-764): the targets of ticket_link
// besides the primary object (related_ci_id, related_asset_id).
const (
	LinkCI         = "ci"
	LinkAsset      = "asset"
	LinkLocation   = "location"
	LinkContract   = "contract"
	LinkReviewItem = "review_item"
)

// LinkEntityTypes are the entity types of a ticket link, sorted.
var LinkEntityTypes = []string{LinkAsset, LinkCI, LinkContract, LinkLocation, LinkReviewItem}

// Link is an object linked to a ticket (ticket_link, WP-764).
type Link struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	TicketID       string    `json:"ticket_id"`
	EntityType     string    `json:"entity_type"`
	EntityID       string    `json:"entity_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// LinkRequest is the payload of POST /api/v1/tickets/{id}/links.
type LinkRequest struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}
