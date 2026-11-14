// Package form provides JSON-Schema backed form definitions and submissions.
package form

import "time"

type JSONMap map[string]any

// Definition describes an active, tenant-scoped form definition.
type Definition struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organization_id"`
	ClientID       string  `json:"client_id,omitempty"`
	Name           string  `json:"name"`
	Description    string  `json:"description,omitempty"`
	Schema         JSONMap `json:"schema"`
	UIHints        JSONMap `json:"ui_hints"`
	Active         bool    `json:"active"`
	// Version counts every change of the definition; it is the ETag
	// (If-Match, API-07).
	Version int64 `json:"version"`
	// CurrentVersionID names the stored schema version that Schema and
	// UIHints copy (WFL-01, E-228); SchemaVersion is its number.
	CurrentVersionID string    `json:"current_version_id,omitempty"`
	SchemaVersion    int       `json:"schema_version,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DefinitionVersion is one immutable revision of the schema and UI hints
// of a form definition (WFL-01, E-228 option 1). A submission is bound to
// the version it was captured against.
type DefinitionVersion struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	FormID         string    `json:"form_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Version        int       `json:"version"`
	Schema         JSONMap   `json:"schema"`
	UIHints        JSONMap   `json:"ui_hints"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Submission stores values submitted against a definition.
type Submission struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	FormID         string `json:"form_id"`
	// FormVersionID is the schema version the values were captured against
	// (WFL-01); FormVersion is its number.
	FormVersionID string  `json:"form_version_id"`
	FormVersion   int     `json:"form_version"`
	Values        JSONMap `json:"values"`
	SubmittedBy   string  `json:"submitted_by,omitempty"`
	CIID          string  `json:"ci_id,omitempty"`
	TicketID      string  `json:"ticket_id,omitempty"`
	// RunID is the workflow run in waiting_input this submission continued
	// (request_form, WP-1102); empty for a submission of its own.
	RunID     string    `json:"run_id,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ExpectedVersionID, when set, is the schema version the caller
	// validated the values against: CreateSubmission refuses the
	// submission with ErrVersionChanged when the current version differs.
	ExpectedVersionID string `json:"-"`
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
	// IfMatch is the version of the If-Match header (API-07); nil without
	// the header, api.IfMatchAny for "*".
	IfMatch *int64 `json:"-"`
	// CreatedBy is the user a new schema version names (empty: none).
	CreatedBy string `json:"-"`
}

type CreateSubmissionRequest struct {
	Values   JSONMap `json:"values"`
	CIID     string  `json:"ci_id,omitempty"`
	TicketID string  `json:"ticket_id,omitempty"`
	Status   string  `json:"status,omitempty"`
	// RunID names the workflow run whose request_form step waits for this
	// form (WP-1102); the submission continues it.
	RunID string `json:"run_id,omitempty"`
}

type SubmissionFilter struct {
	FormID   string
	Status   string
	TicketID string
	CIID     string
}
