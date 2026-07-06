// Package ci provides the CI domain model, repository, and HTTP handler for Reticora CMDB.
package ci

// Item represents a Configuration Item instance.
type Item struct {
	ID              string         `json:"id"`
	OrganizationID  string         `json:"organization_id"`
	ClientID        string         `json:"client_id,omitempty"`
	CITypeID        string         `json:"ci_type_id"`
	Name            string         `json:"name"`
	Status          string         `json:"status"`
	Manufacturer    string         `json:"manufacturer,omitempty"`
	Model           string         `json:"model,omitempty"`
	SerialNumber    string         `json:"serial_number,omitempty"`
	ManagementIP    string         `json:"management_ip,omitempty"`
	FirmwareVersion string         `json:"firmware_version,omitempty"`
	Attributes      map[string]any `json:"attributes"`
	Source          string         `json:"source,omitempty"`
	LastSeen        string         `json:"last_seen,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

// CreateRequest is the payload for creating a CI.
type CreateRequest struct {
	CITypeID        string         `json:"ci_type_id"`
	ClientID        string         `json:"client_id,omitempty"`
	Name            string         `json:"name"`
	Status          string         `json:"status,omitempty"`
	Manufacturer    string         `json:"manufacturer,omitempty"`
	Model           string         `json:"model,omitempty"`
	SerialNumber    string         `json:"serial_number,omitempty"`
	ManagementIP    string         `json:"management_ip,omitempty"`
	FirmwareVersion string         `json:"firmware_version,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	Source          string         `json:"source,omitempty"`
}

// UpdateRequest is the payload for updating a CI.
type UpdateRequest struct {
	Name            *string        `json:"name,omitempty"`
	Status          *string        `json:"status,omitempty"`
	Manufacturer    *string        `json:"manufacturer,omitempty"`
	Model           *string        `json:"model,omitempty"`
	SerialNumber    *string        `json:"serial_number,omitempty"`
	ManagementIP    *string        `json:"management_ip,omitempty"`
	FirmwareVersion *string        `json:"firmware_version,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	Source          *string        `json:"source,omitempty"`
	LastSeen        *string        `json:"last_seen,omitempty"`
}

// FilterParams holds query filter parameters for listing CIs.
type FilterParams struct {
	Status   string
	TypeID   string
	ClientID string
	Search   string
	SortBy   string
	SortDir  string
}
