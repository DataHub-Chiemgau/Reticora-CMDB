package ci

import (
	"fmt"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// MemoryRepository is an in-memory CI repository for development and testing.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string]*Item
	seq   int
}

// NewMemoryRepository creates a new in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		items: make(map[string]*Item),
	}
}

func (r *MemoryRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Item
	for _, item := range r.items {
		if item.OrganizationID != orgID {
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
		result = append(result, *item)
	}

	total := len(result)
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Item, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	return item, nil
}

func (r *MemoryRepository) Create(item *Item) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	item.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	item.CreatedAt = now
	item.UpdatedAt = now
	r.items[item.ID] = item
	return nil
}

func (r *MemoryRepository) Update(orgID, id string, req UpdateRequest) (*Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
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
	if req.Attributes != nil {
		if item.Attributes == nil {
			item.Attributes = make(map[string]any)
		}
		for k, v := range req.Attributes {
			item.Attributes[k] = v
		}
	}
	if req.Source != nil {
		item.Source = *req.Source
	}
	if req.LastSeen != nil {
		t, err := time.Parse(time.RFC3339, *req.LastSeen)
		if err == nil {
			item.LastSeenAt = &t
		}
	}
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return item, nil
}

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.items[id]
	if !ok || item.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.items, id)
	return nil
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
