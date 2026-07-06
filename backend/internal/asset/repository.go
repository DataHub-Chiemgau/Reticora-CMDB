package asset

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for assets.
type Repository interface {
	List(orgID string, filter FilterParams, page api.PaginationParams) ([]Asset, int, error)
	GetByID(orgID, id string) (*Asset, error)
	Create(a *Asset) error
	Update(orgID, id string, req UpdateRequest) (*Asset, error)
	Delete(orgID, id string) error
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu     sync.RWMutex
	assets map[string]*Asset
	nextID int
}

// NewMemoryRepository creates a new in-memory asset repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{assets: make(map[string]*Asset)}
}

func (r *MemoryRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Asset, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Asset
	for _, a := range r.assets {
		if a.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && a.Status != filter.Status {
			continue
		}
		if filter.Category != "" && a.Category != filter.Category {
			continue
		}
		if filter.ClientID != "" && a.ClientID != filter.ClientID {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(a.Name), strings.ToLower(filter.Search)) &&
			!strings.Contains(strings.ToLower(a.AssetTag), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *a)
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Asset, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	a, ok := r.assets[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("asset not found")
	}
	return a, nil
}

func (r *MemoryRepository) Create(a *Asset) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	a.ID = fmt.Sprintf("asset-%d", r.nextID)
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	r.assets[a.ID] = a
	return nil
}

func (r *MemoryRepository) Update(orgID, id string, req UpdateRequest) (*Asset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.assets[id]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("asset not found")
	}

	if req.Name != nil {
		a.Name = *req.Name
	}
	if req.Category != nil {
		a.Category = *req.Category
	}
	if req.Status != nil {
		a.Status = *req.Status
	}
	if req.PurchaseDate != nil {
		a.PurchaseDate = *req.PurchaseDate
	}
	if req.PurchaseCost != nil {
		a.PurchaseCost = *req.PurchaseCost
	}
	if req.Currency != nil {
		a.Currency = *req.Currency
	}
	if req.WarrantyEnd != nil {
		a.WarrantyEnd = *req.WarrantyEnd
	}
	if req.Supplier != nil {
		a.Supplier = *req.Supplier
	}
	if req.InvoiceNumber != nil {
		a.InvoiceNumber = *req.InvoiceNumber
	}
	if req.SerialNumber != nil {
		a.SerialNumber = *req.SerialNumber
	}
	if req.Location != nil {
		a.Location = *req.Location
	}
	if req.Notes != nil {
		a.Notes = *req.Notes
	}
	if req.CIID != nil {
		a.CIID = *req.CIID
	}
	if req.CustomFields != nil {
		a.CustomFields = req.CustomFields
	}
	a.UpdatedAt = time.Now().UTC()
	return a, nil
}

func (r *MemoryRepository) Delete(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	a, ok := r.assets[id]
	if !ok || a.OrganizationID != orgID {
		return fmt.Errorf("asset not found")
	}
	delete(r.assets, id)
	return nil
}
