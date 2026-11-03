// Package document provides the Document Management domain.
package document

import "time"

// Document represents a stored document with metadata.
type Document struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	FileName       string    `json:"file_name"`
	FileSize       int64     `json:"file_size"`
	MimeType       string    `json:"mime_type"`
	StorageKey     string    `json:"storage_key"`
	Version        int       `json:"version"`
	Category       string    `json:"category"`
	Tags           []string  `json:"tags"`
	UploadedBy     string    `json:"uploaded_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DocumentLink links a document to another entity (CI, asset, ticket, etc.).
type DocumentLink struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	DocumentID     string    `json:"document_id"`
	EntityType     string    `json:"entity_type"`
	EntityID       string    `json:"entity_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateRequest is the payload for creating a document record.
type CreateRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	FileName    string   `json:"file_name"`
	FileSize    int64    `json:"file_size"`
	MimeType    string   `json:"mime_type,omitempty"`
	StorageKey  string   `json:"storage_key"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Links are created with the document in one transaction (E-61,
	// option 1). A principal restricted in the client, site or team scope
	// needs at least one; each target is checked like POST …/links.
	Links []LinkRequest `json:"links,omitempty"`
}

// UpdateRequest is the payload for updating a document.
type UpdateRequest struct {
	Title       *string  `json:"title,omitempty"`
	Description *string  `json:"description,omitempty"`
	Category    *string  `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// LinkRequest is the payload for linking a document to an entity.
type LinkRequest struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
}

// FilterParams holds filter parameters for listing documents.
type FilterParams struct {
	Category string
	Search   string
	SortBy   string
	SortDir  string
	// EntityType and EntityID restrict the list to the documents linked to
	// that target (WP-811); both are set or both are empty.
	EntityType string
	EntityID   string
}

// ScanPending is the scan status of a new document version (SEC-09). This
// package stores it only; WP-733 and WP-734 evaluate and change the status
// (clean, infected, scan_failed).
const ScanPending = "pending_scan"

// Version is one stored content of a document (WP-731, MGT-01). The
// document mirrors its highest version in version, storage_key, file_size
// and mime_type.
type Version struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	DocumentID     string `json:"document_id"`
	Version        int    `json:"version"`
	// StorageKey is org/<org>/documents/<document>/<version id> (legacy:
	// documents/<org>/<document>/…); it is served only as a presigned URL.
	StorageKey string `json:"-"`
	Size       int64  `json:"size"`
	MimeType   string `json:"mime_type"`
	// ChecksumSHA256 is the hex sha256 of the content; nil only for the
	// versions of the backfill of migration 000220.
	ChecksumSHA256 *string    `json:"checksum_sha256"`
	ScanStatus     string     `json:"scan_status"`
	ScannedAt      *time.Time `json:"scanned_at,omitempty"`
	// UploadedBy is the user id without foreign key (uploaded_by_surrogate).
	UploadedBy string    `json:"uploaded_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Usage is the document storage of the organization (GET
// /api/v1/documents/usage): the bytes of every stored version and the limit
// of max_storage_mb in MiB, nil when unlimited.
type Usage struct {
	StorageBytes int64  `json:"storage_bytes"`
	LimitMB      *int64 `json:"limit_mb"`
}
