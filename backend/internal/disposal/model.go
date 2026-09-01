// Package disposal implements revision-safe disposal records (spec §6.9):
// append-only documentation of asset/CI disposal incl. data-carrier
// destruction (BSI/ISO). No update/delete — enforced by a database trigger.
package disposal

import "time"

// Record is an immutable disposal record.
type Record struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	AssetID        string    `json:"asset_id,omitempty"`
	CIID           string    `json:"ci_id,omitempty"`
	Method         string    `json:"method"`
	CertificateRef string    `json:"certificate_ref,omitempty"`
	DataCarrier    string    `json:"data_carrier,omitempty"`
	PerformedBy    string    `json:"performed_by,omitempty"`
	PerformedAt    time.Time `json:"performed_at"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateRecordRequest is the payload for recording a disposal.
type CreateRecordRequest struct {
	AssetID        string `json:"asset_id,omitempty"`
	CIID           string `json:"ci_id,omitempty"`
	Method         string `json:"method"`
	CertificateRef string `json:"certificate_ref,omitempty"`
	DataCarrier    string `json:"data_carrier,omitempty"`
	PerformedBy    string `json:"performed_by,omitempty"`
	PerformedAt    string `json:"performed_at,omitempty"`
	Notes          string `json:"notes,omitempty"`
}

// FilterParams scopes disposal list queries.
type FilterParams struct {
	Method  string
	AssetID string
	CIID    string
}
