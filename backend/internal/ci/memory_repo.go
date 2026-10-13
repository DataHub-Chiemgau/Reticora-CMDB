package ci

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// MemoryRepository is an in-memory CI repository for development and testing.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Item
	seq   int
	// deleted keeps what a restore brings back (CI-05).
	deleted map[string]deletedState
}

type deletedState struct{ status, assetID string }

// NewMemoryRepository creates a new in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		items:   make(map[string]*Item),
		deleted: make(map[string]deletedState),
	}
}

func (r *MemoryRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Item
	for _, item := range r.items {
		if item.OrganizationID != orgID || item.DeletedAt != nil {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.TypeID != "" && item.CITypeID != filter.TypeID {
			continue
		}
		if filter.ClientID != "" && item.ClientID != filter.ClientID {
			continue
		}
		if filter.Search != "" && !containsIgnoreCase(item.Name, filter.Search) &&
			!containsIgnoreCase(item.SerialNumber, filter.Search) &&
			!containsIgnoreCase(item.ManagementIP, filter.Search) {
			continue
		}
		if !matchesFilters(item, filter.Filters) {
			continue
		}
		result = append(result, *item)
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(result[i], column), sortValue(result[j], column)
		if vi == vj {
			if direction == "asc" {
				return result[i].ID < result[j].ID
			}
			return result[i].ID > result[j].ID
		}
		if direction == "asc" {
			return vi < vj
		}
		return vi > vj
	})

	if page.Cursor != nil {
		if !page.Cursor.Matches(column, direction) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		remaining := make([]Item, 0, len(result))
		for _, item := range result {
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, item)
			}
		}
		if len(remaining) > page.Limit {
			remaining = remaining[:page.Limit]
		}
		return remaining, total, nil
	}

	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}

	return result[start:end], total, nil
}

func (r *MemoryRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return nil, fmt.Errorf("not found")
	}
	return item, nil
}

func (r *MemoryRepository) Create(ctx context.Context, item *Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	item.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	item.CreatedAt = now
	item.UpdatedAt = now
	item.Version = 1
	if item.Health == "" {
		item.Health = "unknown"
	}
	r.items[item.ID] = item
	return nil
}

func (r *MemoryRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return nil, fmt.Errorf("not found")
	}

	if req.Name != nil {
		item.Name = *req.Name
	}
	if req.Status != nil {
		item.Status = *req.Status
	}
	if req.Manufacturer != nil {
		item.Manufacturer = *req.Manufacturer
	}
	if req.Model != nil {
		item.Model = *req.Model
	}
	if req.SerialNumber != nil {
		item.SerialNumber = *req.SerialNumber
	}
	if req.ManagementIP != nil {
		item.ManagementIP = *req.ManagementIP
	}
	if req.FirmwareVersion != nil {
		item.FirmwareVersion = *req.FirmwareVersion
	}
	if req.AssetID != nil {
		item.AssetID = *req.AssetID
	}
	if req.ParentCIID != nil {
		item.ParentCIID = *req.ParentCIID
	}
	if req.LocationID != nil {
		// Without the location tree, site and room cannot be derived.
		item.LocationID, item.SiteID, item.RoomID = *req.LocationID, "", ""
	}
	if req.Attributes != nil {
		item.Attributes = MergePatch(item.Attributes, req.Attributes)
	}
	if req.DiscoverySource != nil {
		item.DiscoverySource = *req.DiscoverySource
	}
	if req.LastSeenAt != nil {
		t, err := time.Parse(time.RFC3339, *req.LastSeenAt)
		if err == nil {
			item.LastSeenAt = &t
		}
	}
	if req.Manual != nil || req.Authoritative {
		item.Version++
	}
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return item, nil
}

func (r *MemoryRepository) Delete(ctx context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt != nil {
		return fmt.Errorf("not found")
	}
	// A deleted CI is kept for the retention like in PostgreSQL (CI-05):
	// decommissioned, without its asset link.
	now := time.Now().UTC()
	r.deleted[id] = deletedState{status: item.Status, assetID: item.AssetID}
	item.DeletedAt = &now
	item.Status = StatusDecommissioned
	item.AssetID = ""
	return nil
}

// RestoreDeleted implements Restorer.
func (r *MemoryRepository) RestoreDeleted(_ context.Context, orgID, id string, retention time.Duration) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID || item.DeletedAt == nil {
		return nil, fmt.Errorf("not found")
	}
	if time.Since(*item.DeletedAt) > retention {
		return nil, ErrRetentionExpired
	}
	item.DeletedAt = nil
	if prev, ok := r.deleted[id]; ok {
		item.Status, item.AssetID = prev.status, prev.assetID
		if item.Status == "" || item.Status == StatusDecommissioned {
			item.Status = StatusActive
		}
		delete(r.deleted, id)
	}
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	item.Version++
	return item, nil
}

// ListChanges returns no persisted history for the in-memory repository.
func (r *MemoryRepository) ListChanges(ctx context.Context, orgID, ciID string, page api.PaginationParams) ([]Change, int, error) {
	return []Change{}, 0, nil
}

func containsIgnoreCase(s, sub string) bool {
	if s == "" || sub == "" {
		return false
	}
	ls := []rune(s)
	lsub := []rune(sub)
	if len(lsub) > len(ls) {
		return false
	}
	for i := 0; i <= len(ls)-len(lsub); i++ {
		match := true
		for j := range lsub {
			if toLower(ls[i+j]) != toLower(lsub[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// matchesFilters applies the filter[field][op] conditions in memory. The
// memory repository has no attribute definitions: an attribute compares as
// a number when the filter value is one, otherwise as text.
func matchesFilters(item *Item, filters []api.Filter) bool {
	for _, f := range filters {
		if f.Kind == "" {
			f.Kind = api.KindText
			if _, err := strconv.ParseFloat(f.Values[0], 64); err == nil {
				f.Kind = api.KindNumber
			}
		}
		if !f.Match(fieldValue(item, f.Field)) {
			return false
		}
	}
	return true
}
