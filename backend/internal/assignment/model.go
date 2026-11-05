// Package assignment provides the Assignment domain for asset/CI allocation and transfer.
package assignment

import "time"

// Signature is a captured digital signature for handover/return. The image is
// either inline base64 or an S3 object key; metadata records signer and time.
type Signature struct {
	ImageBase64 string `json:"image_base64,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	SignerName  string `json:"signer_name,omitempty"`
	SignedAt    string `json:"signed_at,omitempty"`
	Method      string `json:"method,omitempty"` // touchscreen | drawn | typed
}

// Assignment is the assignment of an asset (or CI) to a user or team. The
// assignment service (WP-275) answers it with the id of the assignment
// movement; assigned_to names a user holder, assigned_team_id a team
// holder, due_at the due date (due_date its day in organization.timezone).
type Assignment struct {
	ID                string     `json:"id"`
	OrganizationID    string     `json:"organization_id"`
	AssetID           string     `json:"asset_id,omitempty"`
	CIID              string     `json:"ci_id,omitempty"`
	AssignedTo        string     `json:"assigned_to,omitempty"`
	AssignedTeamID    string     `json:"assigned_team_id,omitempty"`
	AssignedBy        string     `json:"assigned_by"`
	AssignmentType    string     `json:"assignment_type"`
	Status            string     `json:"status"`
	AssignedAt        time.Time  `json:"assigned_at"`
	DueDate           string     `json:"due_date,omitempty"`
	DueAt             *time.Time `json:"due_at,omitempty"`
	ReturnedAt        string     `json:"returned_at,omitempty"`
	ReturnCondition   string     `json:"return_condition,omitempty"`
	Notes             string     `json:"notes,omitempty"`
	CheckoutSignature *Signature `json:"checkout_signature,omitempty"`
	ReturnSignature   *Signature `json:"return_signature,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	// checkoutSigner and returnSigner are the vault surrogates of the
	// signer names the table keeps (SEC-05); the API shows the names.
	checkoutSigner, returnSigner string
}

// FilterParams holds filter parameters for listing assignments.
type FilterParams struct {
	Status     string
	AssignedTo string
	AssignedBy string
	AssetID    string
	Search     string
	SortBy     string
	SortDir    string
}

// AssignAssetRequest is the payload of POST /api/v1/assets/{id}/assign:
// exactly one holder, optionally the due date and notes.
type AssignAssetRequest struct {
	AssignedUserID string     `json:"assigned_user_id,omitempty"`
	AssignedTeamID string     `json:"assigned_team_id,omitempty"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	Notes          string     `json:"notes,omitempty"`
}

// ReturnAssetRequest is the payload of POST /api/v1/assets/{id}/return.
type ReturnAssetRequest struct {
	ExpectedHolder  string `json:"expected_holder"`
	ReturnCondition string `json:"return_condition,omitempty"`
	Notes           string `json:"notes,omitempty"`
}

// TransferAssetRequest is the payload of POST
// /api/v1/assets/{id}/transfer: the current and the new holder.
type TransferAssetRequest struct {
	ExpectedHolder string     `json:"expected_holder"`
	AssignedUserID string     `json:"assigned_user_id,omitempty"`
	AssignedTeamID string     `json:"assigned_team_id,omitempty"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	Notes          string     `json:"notes,omitempty"`
}
