package reviews

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// MemoryRepository keeps review items in memory (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Item
	seq   int
}

// NewMemoryRepository creates an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: map[string]*Item{}}
}

// CreateReviewItem implements Repository.
func (r *MemoryRepository) CreateReviewItem(_ context.Context, item *Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = map[string]*Item{}
	}
	if item.ObjectKey != "" {
		for _, existing := range r.items {
			if existing.OrganizationID == item.OrganizationID && existing.Kind == item.Kind &&
				existing.ObjectKey == item.ObjectKey && existing.Status == StatusOpen {
				*item = *existing
				item.Deduplicated = true
				return nil
			}
		}
	}
	r.seq++
	item.ID = fmt.Sprintf("%08d-0000-4000-8000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if item.Status == "" {
		item.Status = StatusOpen
	}
	if item.Payload == nil {
		item.Payload = map[string]any{}
	}
	item.CreatedAt, item.UpdatedAt = now, now
	stored := *item
	r.items[item.ID] = &stored
	return nil
}

// ListReviewItems implements Repository.
func (r *MemoryRepository) ListReviewItems(_ context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Item, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	var result []Item
	for _, stored := range r.items {
		item := *stored
		item.markEscalated(now)
		if item.OrganizationID != orgID || (filter.Status != "" && item.Status != filter.Status) ||
			(filter.Kind != "" && item.Kind != filter.Kind) || (filter.Escalated && !item.Escalated) {
			continue
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	total := len(result)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return result[start:end], total, nil
}

// GetReviewItem implements Repository.
func (r *MemoryRepository) GetReviewItem(_ context.Context, orgID, id string) (*Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	clone := *item
	clone.markEscalated(time.Now())
	return &clone, nil
}

// ResolveReviewItem implements Repository.
func (r *MemoryRepository) ResolveReviewItem(_ context.Context, orgID, id string, resolution *Resolution) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	item.Status = resolution.Status
	item.Action = resolution.Action
	item.Resolution = resolution.Note
	item.ResolvedBy = resolution.ResolvedBy
	item.ResolvedAt = time.Now().UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = item.ResolvedAt
	clone := *item
	return &clone, nil
}

// SetCreatedAt backdates an item (tests of the escalation).
func (r *MemoryRepository) SetCreatedAt(id string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item, ok := r.items[id]; ok {
		item.CreatedAt = at.UTC().Format(time.RFC3339Nano)
	}
}
