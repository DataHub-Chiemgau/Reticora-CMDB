// Package assignment provides the Assignment domain for asset/CI allocation and transfer.
package assignment

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
)

// Signature is the legacy signature of the table assignment (migration
// 000047): inline base64 or an object key, signer surrogate, client time
// and method. It is only read until the table is dropped (WP-843); new
// signatures are SignatureInput and movement.SignatureRef (WP-276).
type Signature struct {
	ImageBase64 string `json:"image_base64,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	SignerName  string `json:"signer_name,omitempty"`
	SignedAt    string `json:"signed_at,omitempty"`
	Method      string `json:"method,omitempty"` // touchscreen | drawn | typed
}

// Assignment is the assignment of an asset to a user or team. The
// assignment service (WP-275) answers it with the id of the assignment
// movement; assigned_to names a user holder, assigned_team_id a team
// holder, due_at the due date (due_date its day in organization.timezone).
// Since WP-747 the reads come from the projection of the ledger: ci_id and
// the legacy signatures stay empty (the signatures of the table assignment
// are secured by WP-843). Signature references the signature of the
// assignment movement (WP-276) in the answers of assign and transfer.
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

	Signature *movement.SignatureRef `json:"signature,omitempty"`
}

// SignatureInput is a signature captured with an assignment, return or
// transfer (AST-02, E-51, WP-276): the PNG of the canvas in base64 and
// optionally the signer. The image becomes a document of the category
// signature bound to the movement by signature_hash.
type SignatureInput struct {
	ImagePNGBase64 string       `json:"image_png_base64"`
	Signer         *SignerInput `json:"signer,omitempty"`
}

// SignerInput names the signer by exactly one of a user of the
// organization, a contact in the caller's scope or the name of an external
// person (stored only as a vault surrogate). Without it the holder of the
// movement signs.
type SignerInput struct {
	UserID    string `json:"user_id,omitempty"`
	ContactID string `json:"contact_id,omitempty"`
	Name      string `json:"name,omitempty"`
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
// exactly one holder, optionally the due date, notes and the signature
// (WP-276).
type AssignAssetRequest struct {
	AssignedUserID string          `json:"assigned_user_id,omitempty"`
	AssignedTeamID string          `json:"assigned_team_id,omitempty"`
	DueAt          *time.Time      `json:"due_at,omitempty"`
	Notes          string          `json:"notes,omitempty"`
	Signature      *SignatureInput `json:"signature,omitempty"`
}

// ReturnAssetRequest is the payload of POST /api/v1/assets/{id}/return.
type ReturnAssetRequest struct {
	ExpectedHolder  string          `json:"expected_holder"`
	ReturnCondition string          `json:"return_condition,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	Signature       *SignatureInput `json:"signature,omitempty"`
}

// TransferAssetRequest is the payload of POST
// /api/v1/assets/{id}/transfer: the current and the new holder; signature
// is that of the new holder (bound to the assignment), return_signature
// that of the previous one (bound to the return).
type TransferAssetRequest struct {
	ExpectedHolder  string          `json:"expected_holder"`
	AssignedUserID  string          `json:"assigned_user_id,omitempty"`
	AssignedTeamID  string          `json:"assigned_team_id,omitempty"`
	DueAt           *time.Time      `json:"due_at,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	Signature       *SignatureInput `json:"signature,omitempty"`
	ReturnSignature *SignatureInput `json:"return_signature,omitempty"`
}
