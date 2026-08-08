package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type MemoryRepository struct {
	mu   sync.RWMutex
	docs map[string]Document
}

func NewMemoryRepository() *MemoryRepository             { return &MemoryRepository{docs: map[string]Document{}} }
func (r *MemoryRepository) Ping(_ context.Context) error { return nil }
func (r *MemoryRepository) IndexDocument(_ context.Context, doc Document) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if doc.OrganizationID == "" || doc.EntityType == "" || doc.EntityID == "" {
		return fmt.Errorf("organization_id, entity_type and entity_id are required")
	}
	if doc.UpdatedAt.IsZero() {
		doc.UpdatedAt = time.Now().UTC()
	}
	r.docs[key(doc.OrganizationID, doc.EntityType, doc.EntityID)] = doc
	return nil
}
func (r *MemoryRepository) Delete(_ context.Context, orgID, entityType, entityID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.docs, key(orgID, entityType, entityID))
	return nil
}

// ReindexTenant reports the documents already held for the tenant as indexed.
// The in-memory backend has no external index to rebuild: documents are
// searchable as soon as they are written, so reindexing is a no-op by design.
func (r *MemoryRepository) ReindexTenant(_ context.Context, orgID string) (ReindexResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, d := range r.docs {
		if d.OrganizationID == orgID {
			count++
		}
	}
	return ReindexResult{Indexed: count}, nil
}
func (r *MemoryRepository) Query(_ context.Context, q Query) (Result, error) {
	if err := validateQuery(q.Text); err != nil {
		return Result{}, err
	}
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	allowed := map[string]bool{}
	for _, t := range q.EntityTypes {
		allowed[t] = true
	}
	needle := strings.ToLower(strings.TrimSpace(q.Text))
	r.mu.RLock()
	defer r.mu.RUnlock()
	var hits []Hit
	for _, d := range r.docs {
		if d.OrganizationID != q.OrganizationID || (len(allowed) > 0 && !allowed[d.EntityType]) {
			continue
		}
		hay := strings.ToLower(d.Title + " " + d.Summary)
		if needle != "" && !strings.Contains(hay, needle) {
			continue
		}
		h := Hit{Document: d, Score: 1}
		if q.Highlight && needle != "" {
			h.Highlights = []string{highlight(d.Title+" — "+d.Summary, needle)}
		}
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].UpdatedAt.After(hits[j].UpdatedAt)
		}
		return hits[i].Score > hits[j].Score
	})
	total := len(hits)
	start := min(q.Offset, total)
	end := min(start+limit, total)
	return Result{Data: hits[start:end], Total: total, Limit: limit, Offset: q.Offset, HasMore: end < total}, nil
}
func key(org, typ, id string) string { return org + ":" + typ + ":" + id }
func validateQuery(s string) error {
	if len([]rune(s)) > MaxQueryLength {
		return fmt.Errorf("search query is too long")
	}
	return nil
}
func highlight(s, needle string) string {
	if s == "" {
		return ""
	}
	return s
}
