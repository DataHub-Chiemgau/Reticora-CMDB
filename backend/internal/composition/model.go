// Package composition implements the serialized parent-asset / child-CI model
// (spec §5, §14). A parent asset owns the shared inventory identity (asset
// number, serial, purchase, warranty, location, lifecycle); child CIs/assets
// carry only component- or configuration-specific attributes and declare via
// independence flags whether they are tracked on their own.
package composition

import "time"

// Composition links a parent asset to one child CI or child asset.
type Composition struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ParentAssetID  string    `json:"parent_asset_id"`
	ChildCIID      string    `json:"child_ci_id,omitempty"`
	ChildAssetID   string    `json:"child_asset_id,omitempty"`
	Role           string    `json:"role,omitempty"`
	Position       string    `json:"position,omitempty"`
	// Independence flags (spec §14): children default to configuration-only,
	// i.e. not independently tracked by inventory.
	ConfigurationOnly             bool `json:"configuration_only"`
	IndependentlySerialized       bool `json:"independently_serialized"`
	IndependentlyAssignable       bool `json:"independently_assignable"`
	IndependentlyLocatable        bool `json:"independently_locatable"`
	IndependentlyLifecycleManaged bool `json:"independently_lifecycle_managed"`
	CreatedAt                     time.Time `json:"created_at"`
	UpdatedAt                     time.Time `json:"updated_at"`
}

// CreateRequest is the payload for creating a composition link.
type CreateRequest struct {
	ParentAssetID string `json:"parent_asset_id"`
	ChildCIID     string `json:"child_ci_id,omitempty"`
	ChildAssetID  string `json:"child_asset_id,omitempty"`
	Role          string `json:"role,omitempty"`
	Position      string `json:"position,omitempty"`
	ConfigurationOnly             *bool `json:"configuration_only,omitempty"`
	IndependentlySerialized       bool  `json:"independently_serialized,omitempty"`
	IndependentlyAssignable       bool  `json:"independently_assignable,omitempty"`
	IndependentlyLocatable        bool  `json:"independently_locatable,omitempty"`
	IndependentlyLifecycleManaged bool  `json:"independently_lifecycle_managed,omitempty"`
}

// UpdateRequest is the payload for updating a composition link.
type UpdateRequest struct {
	Role                          *string `json:"role,omitempty"`
	Position                      *string `json:"position,omitempty"`
	ConfigurationOnly             *bool   `json:"configuration_only,omitempty"`
	IndependentlySerialized       *bool   `json:"independently_serialized,omitempty"`
	IndependentlyAssignable       *bool   `json:"independently_assignable,omitempty"`
	IndependentlyLocatable        *bool   `json:"independently_locatable,omitempty"`
	IndependentlyLifecycleManaged *bool   `json:"independently_lifecycle_managed,omitempty"`
}

// Shared inventory properties that live only on the parent asset (spec §5).
// Setting them on a child with a parent is rejected by validation.
var ParentOwnedFields = []string{
	"asset_tag", "serial_number", "barcode", "rfid_tag",
	"purchase_date", "purchase_cost", "supplier", "invoice_number",
	"warranty_end", "location", "lifecycle_state",
}
