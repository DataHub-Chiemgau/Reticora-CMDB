// Package disposal implements revision-safe disposal records (spec §6.9):
// append-only documentation of asset/CI disposal incl. data-carrier
// destruction after DIN 66399 and the BSI recommendation (E-55, WP-751).
// No update/delete — enforced by a database trigger.
package disposal

import "time"

// Record is an immutable disposal record. PerformedBy and Witness are the
// names of the persons; the database keeps them only as vault surrogates
// (performer_surrogate, witness_surrogate) and the repository resolves them
// on read. RecordHash is SHA-256 over the canonical JSON of the business
// fields (migration 000245); the database sets it.
type Record struct {
	ID                 string    `json:"id"`
	OrganizationID     string    `json:"organization_id"`
	AssetID            string    `json:"asset_id,omitempty"`
	CIID               string    `json:"ci_id,omitempty"`
	Method             string    `json:"method"`
	ProtectionClass    *int      `json:"protection_class,omitempty"`
	SecurityLevel      string    `json:"security_level,omitempty"`
	DataCarrier        string    `json:"data_carrier,omitempty"`
	SupplierID         string    `json:"supplier_id,omitempty"`
	EvidenceDocumentID string    `json:"evidence_document_id,omitempty"`
	CertificateRef     string    `json:"certificate_ref,omitempty"`
	PerformedBy        string    `json:"performed_by,omitempty"`
	Witness            string    `json:"witness,omitempty"`
	PerformedAt        time.Time `json:"performed_at"`
	Notes              string    `json:"notes,omitempty"`
	RecordHash         string    `json:"record_hash,omitempty"`
	CreatedAt          time.Time `json:"created_at"`

	// performerSurrogate and witnessSurrogate are the vault subjects of
	// PerformedBy and Witness.
	performerSurrogate, witnessSurrogate string
}

// CreateRecordRequest is the payload for recording a disposal. The required
// fields depend on the method (RequiredFields, E-55).
type CreateRecordRequest struct {
	AssetID            string `json:"asset_id,omitempty"`
	CIID               string `json:"ci_id,omitempty"`
	Method             string `json:"method"`
	ProtectionClass    *int   `json:"protection_class,omitempty"`
	SecurityLevel      string `json:"security_level,omitempty"`
	DataCarrier        string `json:"data_carrier,omitempty"`
	SupplierID         string `json:"supplier_id,omitempty"`
	EvidenceDocumentID string `json:"evidence_document_id,omitempty"`
	CertificateRef     string `json:"certificate_ref,omitempty"`
	PerformedBy        string `json:"performed_by,omitempty"`
	Witness            string `json:"witness,omitempty"`
	PerformedAt        string `json:"performed_at,omitempty"`
	Notes              string `json:"notes,omitempty"`
}

// FilterParams scopes disposal list queries.
type FilterParams struct {
	Method  string
	AssetID string
	CIID    string
}
