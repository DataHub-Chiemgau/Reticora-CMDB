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
}
