// Package search provides tenant-scoped full-text search backends.
package search

import "time"

const MaxQueryLength = 512

type Document struct {
	ID             string            `json:"id"`
	OrganizationID string            `json:"organization_id"`
	EntityType     string            `json:"entity_type"`
	EntityID       string            `json:"entity_id"`
	Title          string            `json:"title"`
	Summary        string            `json:"summary,omitempty"`
	URL            string            `json:"url"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type Query struct {
	OrganizationID string
	UserID         string
	Text           string
	EntityTypes    []string
	Limit          int
	Offset         int
	Highlight      bool
}

type Hit struct {
	Document
	Score      float64  `json:"score"`
	Highlights []string `json:"highlights,omitempty"`
}

type Result struct {
	Data    []Hit `json:"data"`
	Total   int   `json:"total"`
	Limit   int   `json:"limit"`
	Offset  int   `json:"offset"`
	HasMore bool  `json:"has_more"`
}

type ReindexResult struct {
	Indexed int `json:"indexed"`
}

type Backend interface {
	Ping() error
	IndexDocument(doc Document) error
	Delete(orgID, entityType, entityID string) error
	Query(q Query) (Result, error)
	ReindexTenant(orgID string) (ReindexResult, error)
}
