// Package search provides tenant-scoped full-text search backends.
package search

import (
	"context"
	"time"
)

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
	Ping(ctx context.Context) error
	IndexDocument(ctx context.Context, doc Document) error
	Delete(ctx context.Context, orgID, entityType, entityID string) error
	Query(ctx context.Context, q Query) (Result, error)
	ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error)
}

// ReadPermission maps each indexed entity type to the permission a caller
// needs to see its hits. Types missing here are never returned.
var ReadPermission = map[string]string{
	"ci":          "ci:read",
	"asset":       "asset:read",
	"document":    "document:read",
	"ticket":      "ticket:read",
	"contact":     "contact:read",
	"compliance":  "compliance:read",
	"location":    "site:read",
	"reservation": "asset:read",
}
