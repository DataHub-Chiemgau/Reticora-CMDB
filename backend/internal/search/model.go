// Package search provides tenant-scoped full-text search backends.
package search

import (
	"context"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
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
	// ClientID and SiteID are the scope of the entity (empty: org-wide). The
	// PostgreSQL index derives them itself; remote indexes store what the
	// writer passes, and every query filters on them (CH11, CH25).
	ClientID  string    `json:"client_id,omitempty"`
	SiteID    string    `json:"site_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Fields are the summary columns by field key (SummaryFields, RBA-07,
	// WP-266). The PostgreSQL index stores them as field rows; they are
	// never serialized, so neither a hit nor a remote index carries them.
	Fields map[string]string `json:"-"`
}

type Query struct {
	OrganizationID string
	UserID         string
	Text           string
	EntityTypes    []string
	Limit          int
	Offset         int
	Highlight      bool
	// Policy is the field policy of the principal (RBA-07, WP-266): with a
	// restriction of ci, asset, document or ticket, a hit of that type
	// matches, ranks and shows only its title and the summary fields
	// readable for its type and scope. The REST handler and the GraphQL
	// resolver always set it. nil is reserved for internal callers (the
	// copy of HybridBackend.ReindexTenant, the AI retriever) and answers
	// without field permissions.
	Policy *permission.FieldPolicy
}

// SummaryFields are the field keys of the summary columns per entity type,
// in the order of the summary (WP-266). The title (name or title) is a
// structural field and no field row. Other entity types have no field
// permissions: their summary is their only hit basis.
var SummaryFields = map[string][]string{
	"ci": {"core.hostname", "core.manufacturer", "core.model", "core.serial_number", "core.os_name", "core.os_version"},
	"asset": {"core.asset_tag", "core.category", "core.status", "core.supplier", "core.serial_number", "core.location",
		"core.notes"},
	"document": {"core.description", "core.file_name", "core.category", "core.tags"},
	"ticket":   {"core.description", "core.status", "core.priority", "core.category", "core.tags"},
}

// fieldTypes are the entity types with field rows, in a fixed order.
var fieldTypes = []string{"ci", "asset", "document", "ticket"}

// restrictedTypes returns the queried entity types with field rows whose
// fields the policy restricts; none without a policy.
func (q *Query) restrictedTypes() []string {
	if q.Policy == nil {
		return nil
	}
	var out []string
	for _, t := range fieldTypes {
		if len(q.EntityTypes) > 0 && !slices.Contains(q.EntityTypes, t) {
			continue
		}
		if q.Policy.Restricted(permission.ObjectKind(t)) {
			out = append(out, t)
		}
	}
	return out
}

// readableFields returns the summary fields of the entity type that the
// policy lets the principal read for an object of the type and scope, in
// summary order.
func readableFields(policy *permission.FieldPolicy, entityType, typeID string, ref *permission.ObjectRef) []string {
	var out []string
	for _, key := range SummaryFields[entityType] {
		if policy.Level(permission.ObjectKind(entityType), typeID, key, *ref).Readable() {
			out = append(out, key)
		}
	}
	return out
}

type Hit struct {
	Document
	Score      float64  `json:"score"`
	Highlights []string `json:"highlights,omitempty"`
}

type Result struct {
	Data  []Hit `json:"data"`
	Total int   `json:"total"`
	// TotalCapped marks a Total of the PostgreSQL index that stopped at
	// api.TotalCap (E-104): more hits match. The handler answers total as
	// the hits of the page the caller may read, so it never reaches the
	// cap and the response does not carry the mark.
	TotalCapped bool `json:"-"`
	Limit       int  `json:"limit"`
	Offset      int  `json:"offset"`
	HasMore     bool `json:"has_more"`
	// NextCursor continues after this page (API-02).
	NextCursor string `json:"next_cursor,omitempty"`
}

type ReindexResult struct {
	Indexed int `json:"indexed"`
}

type Backend interface {
	Ping(ctx context.Context) error
	IndexDocument(ctx context.Context, doc *Document) error
	Delete(ctx context.Context, orgID, entityType, entityID string) error
	Query(ctx context.Context, q *Query) (Result, error)
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
	"location":    "location:read",
	"reservation": "reservation:read", // RBA-05, E-106 (WP-245)
}
