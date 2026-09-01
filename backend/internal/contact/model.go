// Package contact provides REST APIs for contacts and CI-contact links.
package contact

import "time"

// Contact represents a person or organization associated with CIs.
type Contact struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	DisplayName    string    `json:"display_name"`
	Email          string    `json:"email,omitempty"`
	Phone          string    `json:"phone,omitempty"`
	Role           string    `json:"role,omitempty"`
	Department     string    `json:"department,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateContactRequest is the payload for creating a contact.
type CreateContactRequest struct {
	ClientID    string `json:"client_id,omitempty"`
	DisplayName string `json:"display_name"`
	// Name is accepted as a display_name alias for integrations that send the
	// generic contact field instead of the canonical one.
	Name       string `json:"name,omitempty"`
	Email      string `json:"email,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Role       string `json:"role,omitempty"`
	Department string `json:"department,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// UpdateContactRequest is the payload for updating a contact.
type UpdateContactRequest struct {
	ClientID    *string `json:"client_id,omitempty"`
	DisplayName *string `json:"display_name,omitempty"`
	Email       *string `json:"email,omitempty"`
	Phone       *string `json:"phone,omitempty"`
	Role        *string `json:"role,omitempty"`
	Department  *string `json:"department,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

// CIContact links a contact to a configuration item.
type CIContact struct {
	ID               string    `json:"id"`
	OrganizationID   string    `json:"organization_id"`
	CIID             string    `json:"ci_id"`
	ContactID        string    `json:"contact_id"`
	RelationshipType string    `json:"relationship_type"`
	CreatedAt        time.Time `json:"created_at"`
}

// LinkContactRequest is the payload for linking a contact to a CI.
type LinkContactRequest struct {
	ContactID        string `json:"contact_id"`
	RelationshipType string `json:"relationship_type,omitempty"`
}

// ValidRelationshipTypes lists allowed ci_contact relationship types.
var ValidRelationshipTypes = map[string]bool{
	"responsible": true,
	"owner":       true,
	"operator":    true,
	"vendor":      true,
	"escalation":  true,
}
